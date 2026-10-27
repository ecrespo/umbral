package desktop

import "context"

// ThreadRow is one thread of the threads view: what REQ-ORC-003 asks the desktop client to
// show without opening it — its attention state, whether its turn is `queued`, its worktree
// and how many approvals wait on it.
type ThreadRow struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	State      string `json:"state"`
	Attention  string `json:"attention_state"`
	Mode       string `json:"mode"`
	Agent      string `json:"agent,omitempty"`
	WorktreeID string `json:"worktree_id,omitempty"`
	Queued     bool   `json:"queued"`
	Pending    int    `json:"pending_approvals"`
}

// snapshotThread is the part of a `session.snapshot` thread the view reads (API §4 `Thread`).
type snapshotThread struct {
	ID          string  `json:"id"`
	WorkspaceID *string `json:"workspace_id"`
	Title       string  `json:"title"`
	State       string  `json:"state"`
	Attention   string  `json:"attention_state"`
	Mode        string  `json:"mode"`
	Agent       *string `json:"agent"`
	WorktreeID  *string `json:"worktree_id"`
	Queued      bool    `json:"queued"`
}

// pendingApproval is the part of an `approval.list` item the view counts.
type pendingApproval struct {
	ThreadID string `json:"thread_id"`
}

// threadRows is the view's list: the threads whose workspace is workspaceID — a thread of no
// workspace or of another is not listed, and no workspace lists nothing — in the snapshot's
// order, each with its pending approvals counted.
func threadRows(threads []snapshotThread, approvals []pendingApproval, workspaceID string) []ThreadRow {
	pending := map[string]int{}
	for _, a := range approvals {
		pending[a.ThreadID]++
	}
	rows := []ThreadRow{}
	for _, t := range threads {
		if workspaceID == "" || t.WorkspaceID == nil || *t.WorkspaceID != workspaceID {
			continue
		}
		rows = append(rows, ThreadRow{
			ID: t.ID, Title: t.Title, State: t.State, Attention: t.Attention, Mode: t.Mode,
			Agent: deref(t.Agent), WorktreeID: deref(t.WorktreeID), Queued: t.Queued, Pending: pending[t.ID],
		})
	}
	return rows
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Threads is the threads view of one workspace, read from one `session.snapshot` and the
// pending approvals (REQ-ORC-003).
func (b *Bridge) Threads(workspaceID string) ([]ThreadRow, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	var snap struct {
		Threads []snapshotThread `json:"threads"`
	}
	if err := b.stream.Call(ctx, "session.snapshot", map[string]any{}, &snap); err != nil {
		return nil, err
	}
	var list struct {
		Items []pendingApproval `json:"items"`
	}
	if err := b.stream.Call(ctx, "approval.list", map[string]any{}, &list); err != nil {
		return nil, err
	}
	return threadRows(snap.Threads, list.Items, workspaceID), nil
}
