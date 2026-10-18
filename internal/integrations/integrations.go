// Package integrations is the integration surface of Tech Design §5.2: external reporters'
// semantic state for a pane (REQ-INT-002, REQ-INT-003, REQ-INT-005, REQ-INT-006), and the
// `pane.state_changed` notifications that follow every change of a pane's effective state,
// whoever decided it (DD-012).
//
// It decides nothing about precedence itself. The workspace tree resolves a pane's state
// from every authority in DD-012's order (`workspaces/domain.ResolveAttention`); this module
// writes the external reports it reads, and tells clients when the answer changed.
package integrations

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/integrations/domain"
	"github.com/ecrespo/umbral/internal/integrations/ports"
	wsdomain "github.com/ecrespo/umbral/internal/workspaces/domain"
	wsports "github.com/ecrespo/umbral/internal/workspaces/ports"
)

// Config is what the service needs.
type Config struct {
	Store ports.Store
	// Metadata is where display metadata goes (REQ-INT-004).
	Metadata ports.MetadataStore
	Panes    ports.Panes
	Bus      *bus.Bus
	Logger   *slog.Logger
	Now      func() time.Time
}

// Service implements ports.Integrations.
type Service struct {
	cfg Config

	// mu serialises every write and every refresh, so the notifications go out in the order
	// the changes were written and a refresh never compares against a half-updated map.
	mu sync.Mutex
	// panes and rollups are what was last told to clients. A pane or workspace not in them
	// was last told `unknown`, the state everything starts in (API Spec §4).
	panes   map[string]published
	rollups map[string]wsdomain.AttentionState
}

type published struct {
	state  wsdomain.AttentionState
	source string
}

// New builds the service.
func New(cfg Config) (*Service, error) {
	switch {
	case cfg.Store == nil:
		return nil, errors.New("integrations: a store is required")
	case cfg.Metadata == nil:
		return nil, errors.New("integrations: a metadata store is required")
	case cfg.Panes == nil:
		return nil, errors.New("integrations: the workspace tree is required")
	case cfg.Bus == nil:
		return nil, errors.New("integrations: a bus is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{
		cfg: cfg, panes: map[string]published{}, rollups: map[string]wsdomain.AttentionState{},
	}, nil
}

var _ ports.Integrations = (*Service)(nil)

// ReportState implements ports.Integrations.
func (s *Service) ReportState(ctx context.Context, report domain.Report) error {
	if err := report.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	pane, err := s.cfg.Panes.GetPane(ctx, report.PaneID)
	if err != nil {
		return err
	}
	if pane.ThreadID != "" {
		return fmt.Errorf("%w: pane %s hosts thread %s of Umbral's own agent, which owns its state",
			domain.ErrPermissionDenied, pane.ID, pane.ThreadID)
	}
	// An alias names the pane it was moved into (REQ-WS-007): a hook started before the move
	// still holds the old UMBRAL_PANE_ID.
	report.PaneID = pane.ID
	written, err := s.cfg.Store.Upsert(ctx, report, s.cfg.Now().UnixMilli())
	if err != nil || !written {
		return err
	}
	s.announce(ctx)
	return nil
}

// ReleaseState implements ports.Integrations.
func (s *Service) ReleaseState(ctx context.Context, paneID, source string) error {
	if err := domain.ValidatePane(paneID); err != nil {
		return err
	}
	if err := domain.ValidateSource(source); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	pane, err := s.cfg.Panes.GetPane(ctx, paneID)
	if err != nil {
		return err
	}
	deleted, err := s.cfg.Store.Delete(ctx, pane.ID, source)
	if err != nil || !deleted {
		return err
	}
	s.announce(ctx)
	return nil
}

// announce refreshes after a write that has committed. A failure is logged, not returned:
// the report is stored, and a hook told otherwise would retry with the same seq and be
// discarded as stale. The next event's refresh catches the announcement up.
func (s *Service) announce(ctx context.Context) {
	if err := s.refreshLocked(ctx); err != nil {
		s.cfg.Logger.WarnContext(ctx, "a pane state was written and could not be announced", slog.Any("error", err))
	}
}

// Refresh re-reads every pane's effective state and tells clients about each one that
// changed. The daemon calls it after an event that can move one — a block starting or
// ending, a session's integration verdict or exit, a thread's attention — because those
// authorities are written by modules this one may not import.
func (s *Service) Refresh(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshLocked(ctx)
}

// Follow calls Refresh for every event of sub until ctx ends or the subscription closes. A
// refresh re-reads everything, so events the bus dropped under pressure are caught up by
// the next one; a failed refresh is logged and the next event retries it.
func (s *Service) Follow(ctx context.Context, sub *bus.Subscription) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-sub.C():
			if !ok {
				return
			}
			if err := s.Refresh(ctx); err != nil && ctx.Err() == nil {
				s.cfg.Logger.Warn("could not refresh the pane states", slog.Any("error", err))
			}
		}
	}
}

func (s *Service) refreshLocked(ctx context.Context) error {
	snapshot, err := s.cfg.Panes.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("integrations: read the tree: %w", err)
	}

	seen := make(map[string]bool, len(snapshot.Panes))
	for _, pane := range snapshot.Panes {
		seen[pane.ID] = true
		now := published{state: pane.AttentionState, source: pane.StateSource}
		before, ok := s.panes[pane.ID]
		if !ok {
			before = published{state: wsdomain.AttentionUnknown}
		}
		if now == before {
			continue
		}
		s.panes[pane.ID] = now
		s.cfg.Bus.Publish(ports.PaneStateChanged{
			PaneID: pane.ID, AttentionState: now.state, StateSource: now.source,
		})
	}
	for id := range s.panes {
		if !seen[id] {
			delete(s.panes, id)
		}
	}

	open := make(map[string]bool, len(snapshot.Workspaces))
	for _, ws := range snapshot.Workspaces {
		open[ws.ID] = true
		before, ok := s.rollups[ws.ID]
		if !ok {
			before = wsdomain.AttentionUnknown
		}
		if ws.RollupState == before {
			continue
		}
		s.rollups[ws.ID] = ws.RollupState
		// REQ-WS-006's rollup is a workspace field, and `workspace.updated` is how a client
		// learns it moved (API Spec §6).
		s.cfg.Bus.Publish(wsports.WorkspaceEvent{Kind: wsports.KindWorkspaceUpdated, Workspace: ws})
	}
	for id := range s.rollups {
		if !open[id] {
			delete(s.rollups, id)
		}
	}
	return nil
}
