package integrations

import (
	"context"
	"errors"
	"log/slog"
	"path"
	"sync"
	"time"

	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/integrations/domain"
	"github.com/ecrespo/umbral/internal/integrations/ports"
	wsdomain "github.com/ecrespo/umbral/internal/workspaces/domain"
	wsports "github.com/ecrespo/umbral/internal/workspaces/ports"
)

// DetectionInterval is how often every live pane is scanned for a third-party agent.
const DetectionInterval = 2 * time.Second

// AgentLabels is what detection last found in each session, read by the workspace tree for
// `Pane.detected_agent`. It is a value of its own so the tree and the detector can be built in
// either order.
// It keeps the label only: the matching process's argv can carry a secret and is not kept.
type AgentLabels struct {
	mu        sync.RWMutex
	bySession map[string]wsdomain.DetectedAgent
}

// NewAgentLabels returns an empty set.
func NewAgentLabels() *AgentLabels {
	return &AgentLabels{bySession: map[string]wsdomain.DetectedAgent{}}
}

// AgentLabel implements workspaces/ports.AgentLabels.
func (l *AgentLabels) AgentLabel(sessionID string) (wsdomain.DetectedAgent, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	a, ok := l.bySession[sessionID]
	return a, ok
}

// set records a session's detection and reports whether its label changed.
func (l *AgentLabels) set(sessionID string, d domain.Detection, found bool) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	old, had := l.bySession[sessionID]
	if !found {
		delete(l.bySession, sessionID)
		return had
	}
	label := wsdomain.DetectedAgent{Name: d.Manifest.Name, StateSource: d.Manifest.StateSource}
	l.bySession[sessionID] = label
	return !had || old != label
}

// forget drops every session not in live, which have exited or left the tree.
func (l *AgentLabels) forget(live map[string]bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for id := range l.bySession {
		if !live[id] {
			delete(l.bySession, id)
		}
	}
}

// DetectorConfig is what the detector needs.
type DetectorConfig struct {
	// Manifests are the built-in ones with the local ones laid over them; Skipped the local
	// files that could not be used (REQ-XAG-002).
	Manifests []domain.Manifest
	Skipped   []domain.SkippedManifest
	Labels    *AgentLabels
	Shells    ports.Shells
	Processes ports.ProcessTree
	Panes     ports.Panes
	Bus       *bus.Bus
	Logger    *slog.Logger
}

// Detector labels panes with the third-party agent running in them (REQ-XAG-001) and explains
// its answer (REQ-XAG-003). It decides no state: nothing it finds is an input to DD-012.
type Detector struct {
	cfg DetectorConfig
	// mu serialises scans, so two never interleave their announcements.
	mu sync.Mutex
}

// NewDetector builds the detector and reports every skipped manifest file in the log.
func NewDetector(cfg DetectorConfig) (*Detector, error) {
	switch {
	case cfg.Labels == nil, cfg.Shells == nil, cfg.Processes == nil, cfg.Panes == nil, cfg.Bus == nil:
		return nil, errors.New("integrations: the detector needs labels, shells, processes, panes and a bus")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	for _, s := range cfg.Skipped {
		cfg.Logger.Warn("agent manifest skipped", "file", s.File, "reason", s.Reason)
	}
	return &Detector{cfg: cfg}, nil
}

// Run scans every interval until ctx ends.
func (d *Detector) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := d.Scan(ctx); err != nil && ctx.Err() == nil {
			d.cfg.Logger.Warn("agent detection scan failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Scan labels every live pane and announces each pane whose label changed.
func (d *Detector) Scan(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	snap, err := d.cfg.Panes.Snapshot(ctx)
	if err != nil {
		return err
	}
	table := d.cfg.Processes.Snapshot()
	live := map[string]bool{}
	for _, p := range snap.Panes {
		if p.SessionID == "" || p.ClosedAt != nil {
			continue
		}
		live[p.SessionID] = true
		d.scanPane(ctx, table, p)
	}
	d.cfg.Labels.forget(live)
	return nil
}

// scanPane labels one pane and announces it if the label changed. Called with mu held.
func (d *Detector) scanPane(ctx context.Context, table ports.ProcessSnapshot, p wsdomain.Pane) (domain.Detection, bool) {
	var det domain.Detection
	found := false
	if pid, ok := d.cfg.Shells.ShellPID(p.SessionID); ok {
		det, found = domain.Detect(d.cfg.Manifests, table.Subtree(pid))
	}
	if d.cfg.Labels.set(p.SessionID, det, found) {
		// Read the pane again so the announcement carries the new label.
		if updated, err := d.cfg.Panes.GetPane(ctx, p.ID); err == nil {
			d.cfg.Bus.Publish(wsports.PaneEvent{Kind: wsports.KindPaneUpdated, Pane: updated})
		}
	}
	return det, found
}

// Explain scans a pane afresh and says what matched and who decides its state (REQ-XAG-003).
func (d *Detector) Explain(ctx context.Context, paneID string) (domain.Explanation, error) {
	pane, err := d.cfg.Panes.GetPane(ctx, paneID)
	if err != nil {
		return domain.Explanation{}, err
	}
	out := domain.Explanation{PaneID: pane.ID, Skipped: d.cfg.Skipped}
	if pane.SessionID != "" && pane.ClosedAt == nil {
		d.mu.Lock()
		det, found := d.scanPane(ctx, d.cfg.Processes.Snapshot(), pane)
		d.mu.Unlock()
		if found {
			argv0 := ""
			if len(det.Process.Argv) > 0 {
				argv0 = path.Base(det.Process.Argv[0])
			}
			out.Detection = &domain.ExplainedDetection{
				Manifest: det.Manifest, Rule: det.Rule, Pattern: det.Pattern,
				PID: det.Process.PID, Executable: argv0,
			}
		}
		if pane, err = d.cfg.Panes.GetPane(ctx, pane.ID); err != nil {
			return domain.Explanation{}, err
		}
	}
	out.AttentionState, out.StateSource = string(pane.AttentionState), pane.StateSource
	return out, nil
}
