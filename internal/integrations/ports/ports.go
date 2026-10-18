// Package ports is the integration surface's published interfaces: what it serves, what it
// stores and what it reads of the workspace tree (Tech Design §5.2).
package ports

import (
	"context"

	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/integrations/domain"
	wsdomain "github.com/ecrespo/umbral/internal/workspaces/domain"
)

// Integrations is the inbound port, served as `pane.report_state` and `pane.release_state`
// (API Spec §5.31).
type Integrations interface {
	// ReportState records a source's state for a pane. A stale report is accepted and
	// changes nothing (REQ-INT-003); a pane hosting a thread of Umbral's own agent answers
	// domain.ErrPermissionDenied (REQ-INT-006).
	ReportState(ctx context.Context, report domain.Report) error
	// ReleaseState drops a source's authority over a pane (REQ-INT-005). Releasing a pane the
	// source never reported for changes nothing and is not an error.
	ReleaseState(ctx context.Context, paneID, source string) error
}

// Store is where reports live: `pane_state_reports`, which Data Model §6 step 8 clears at
// start.
type Store interface {
	// Upsert writes a report unless it is stale (domain.Stale), in one transaction, and
	// reports whether it was written. A report without a seq keeps the stored one. Each
	// write of a pane's reports gets an `updated_at` past every other report of that pane,
	// so the latest report is never a tie.
	Upsert(ctx context.Context, report domain.Report, nowMillis int64) (bool, error)
	// Delete removes a source's report for a pane and reports whether there was one.
	Delete(ctx context.Context, paneID, source string) (bool, error)
}

// Panes is what the module reads of the workspace tree, which resolves a pane's aliases
// (REQ-WS-007) and decides its effective state (DD-012).
type Panes interface {
	GetPane(ctx context.Context, id string) (wsdomain.Pane, error)
	Snapshot(ctx context.Context) (wsdomain.Snapshot, error)
}

// KindPaneStateChanged is API Spec §6's `pane.state_changed`.
const KindPaneStateChanged bus.Kind = "pane.state_changed"

// PaneStateChanged is published after a pane's effective state or its source changed, once
// what decided it is written (DD-007).
type PaneStateChanged struct {
	PaneID         string
	AttentionState wsdomain.AttentionState
	// StateSource is "" when nothing decides the pane's state, which the wire writes `null`.
	StateSource string
}

// EventKind implements bus.Event.
func (PaneStateChanged) EventKind() bus.Kind { return KindPaneStateChanged }
