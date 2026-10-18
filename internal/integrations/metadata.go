package integrations

import (
	"context"
	"log/slog"

	"github.com/ecrespo/umbral/internal/integrations/domain"
	wsports "github.com/ecrespo/umbral/internal/workspaces/ports"
)

// ReportMetadata implements ports.Integrations (REQ-INT-004).
//
// Metadata is a separate channel (DD-013): this path never refreshes the pane states, so it
// can publish no `pane.state_changed` and move no rollup. What changes is how the pane is
// drawn, and that is announced as `pane.updated` with the pane, metadata included.
func (s *Service) ReportMetadata(ctx context.Context, fields domain.MetadataFields) error {
	report, err := fields.Build()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	pane, err := s.cfg.Panes.GetPane(ctx, report.PaneID)
	if err != nil {
		return err
	}
	// No own-agent check: REQ-INT-006 leaves an agent's pane open to display metadata.
	report.PaneID = pane.ID
	written, err := s.cfg.Metadata.WriteMetadata(ctx, report, s.cfg.Now().UnixMilli())
	if err != nil || !written {
		return err
	}
	// Written, so the answer is `{}` whatever happens next; the next read shows it.
	updated, err := s.cfg.Panes.GetPane(ctx, pane.ID)
	if err != nil {
		s.cfg.Logger.WarnContext(ctx, "pane metadata was written and could not be announced", slog.Any("error", err))
		return nil
	}
	s.cfg.Bus.Publish(wsports.PaneEvent{Kind: wsports.KindPaneUpdated, Pane: updated})
	return nil
}
