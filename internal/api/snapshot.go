package api

import (
	"context"
	"encoding/json"

	wsdomain "github.com/ecrespo/umbral/internal/workspaces/domain"
)

// `session.snapshot` (API Spec §5.3, REQ-API-001).
//
// It is the bootstrap for a client that keeps its own cache: subscribe first, buffer what
// arrives, take the snapshot, install it, then apply only the buffered events whose `seq` is
// greater than the one the snapshot reports. What makes that work is the order below, not
// the contents.

// handleSessionSnapshot returns the tree and the sequence number it contains.
//
// **The counter is read before the tree, and that ordering is the whole correctness
// argument.** The daemon persists before it notifies (DD-007), so an event that has been
// given a `seq` is already in the database. Reading the counter first therefore guarantees
// that everything at or below the number reported here is in this result. Reading it
// afterwards would admit the opposite: an event dispatched while the tree was being read
// would carry a number below the reported one, the client would discard it as already
// contained, and it would be in neither — a lost update with nothing to notice it.
//
// The result may reflect a few events *above* the reported number, because the tree is read
// after they landed. That makes the client apply something it already has, which every tree
// notification tolerates: they carry whole records, not deltas.
func handleSessionSnapshot(ctx context.Context, c *conn, _ json.RawMessage) (any, error) {
	tree, err := c.tree()
	if err != nil {
		return nil, err
	}

	seq := c.server.currentSeq()

	snapshot, err := tree.Snapshot(ctx)
	if err != nil {
		return nil, err
	}

	workspaces := make([]Workspace, 0, len(snapshot.Workspaces))
	for _, ws := range snapshot.Workspaces {
		workspaces = append(workspaces, toWireWorkspace(ws))
	}
	tabs := make([]Tab, 0, len(snapshot.Tabs))
	for _, tab := range snapshot.Tabs {
		tabs = append(tabs, toWireTab(tab))
	}
	panes := make([]Pane, 0, len(snapshot.Panes))
	for _, pane := range snapshot.Panes {
		panes = append(panes, toWirePane(pane))
	}
	layouts := make([]Layout, 0, len(snapshot.Layouts))
	for _, layout := range snapshot.Layouts {
		layouts = append(layouts, toWireLayout(layout))
	}

	return snapshotResult{
		Seq:        seq,
		Focused:    toWireFocus(snapshot.Focus),
		Workspaces: workspaces,
		Tabs:       tabs,
		Panes:      panes,
		Layouts:    layouts,
		// Threads arrive with F1. The member is present and empty rather than absent,
		// because §5.3 lists it and a client iterating the result should not have to
		// branch on which build of the daemon it is talking to.
		Threads: []any{},
	}, nil
}

// Focus is API Spec §5.3's `focused` object.
type Focus struct {
	WorkspaceID *string `json:"workspace_id"`
	TabID       *string `json:"tab_id"`
	// ThreadID is always null in F0 and typed `string | null` by §5.3. It is here rather
	// than omitted so the shape does not change when threads arrive.
	ThreadID *string `json:"thread_id"`
}

func toWireFocus(f wsdomain.Focus) Focus {
	return Focus{
		WorkspaceID: emptyAsNull(f.WorkspaceID),
		TabID:       emptyAsNull(f.TabID),
		ThreadID:    emptyAsNull(f.ThreadID),
	}
}
