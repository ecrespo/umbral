package desktop

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ecrespo/umbral/internal/client"
	"github.com/ecrespo/umbral/internal/desktop/ports"
	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
)

// fakeScreen records what it was fed.
type fakeScreen struct{ fed strings.Builder }

func (f *fakeScreen) Write(p []byte) (int, error)  { return f.fed.Write(p) }
func (f *fakeScreen) Resize(sessdomain.Size) error { return nil }
func (f *fakeScreen) Frame() (ports.Frame, error)  { return ports.Frame{}, nil }
func (f *fakeScreen) Reset() error                 { f.fed.Reset(); return nil }
func (f *fakeScreen) Close() error                 { return nil }
func output(t *testing.T, session string, seq uint64, data string) client.Notification {
	t.Helper()
	params, err := json.Marshal(map[string]any{
		"session_id": session, "seq": seq, "data_b64": base64.StdEncoding.EncodeToString([]byte(data)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return client.Notification{Method: "session.output", Params: params}
}

// TestOutputTheSnapshotHeldIsNotAppliedAgain_REQ_DSK_001: a terminal attached at a snapshot's
// seq applies only the output above it, in the order it came, so bytes the snapshot already
// held are not drawn twice (API §5.11); output for a session not attached is dropped.
func TestOutputTheSnapshotHeldIsNotAppliedAgain_REQ_DSK_001(t *testing.T) {
	t.Parallel()
	screen := &fakeScreen{}
	b := &Bridge{screens: map[string]*terminal{"ses_1": {screen: screen, seq: 7}}, wake: make(chan struct{}, 1)}
	for _, n := range []client.Notification{
		output(t, "ses_1", 6, "old-"),
		output(t, "ses_1", 7, "held-"),
		output(t, "ses_1", 8, "new-"),
		output(t, "ses_1", 8, "again-"),
		output(t, "ses_2", 9, "other-"),
		output(t, "ses_1", 9, "next"),
	} {
		b.output(n)
	}
	if got := screen.fed.String(); got != "new-next" {
		t.Errorf("the screen was fed %q, want %q", got, "new-next")
	}
	if tm := b.screens["ses_1"]; tm.seq != 9 || !tm.dirty {
		t.Errorf("terminal at seq %d dirty %v, want 9 and dirty", tm.seq, tm.dirty)
	}
}

// TestTheThreadsViewIsTheWorkspacesThreads_REQ_ORC_003: the desktop client's threads view
// lists every thread of the focused workspace — and none of another or of no workspace — with
// its attention state, its `queued` mark, its worktree and its pending approvals counted.
func TestTheThreadsViewIsTheWorkspacesThreads_REQ_ORC_003(t *testing.T) {
	t.Parallel()
	// Decoded from the wire, so every field is read by its API §4 name.
	var snap struct {
		Threads []snapshotThread `json:"threads"`
	}
	if err := json.Unmarshal([]byte(`{"threads":[
		{"id":"thr_a","workspace_id":"w1","worktree_id":"wtr_a","attention_state":"blocked","state":"awaiting_approval","title":"fix parser","mode":"normal","agent":null,"queued":false},
		{"id":"thr_b","workspace_id":"w1","worktree_id":null,"attention_state":"working","state":"running","title":"","mode":"auto-edit","agent":"opencode","queued":true},
		{"id":"thr_c","workspace_id":"w2","attention_state":"done","state":"idle"},
		{"id":"thr_d","workspace_id":null,"attention_state":"idle","state":"idle"}]}`), &snap); err != nil {
		t.Fatal(err)
	}
	var list struct {
		Items []pendingApproval `json:"items"`
	}
	if err := json.Unmarshal([]byte(`{"items":[{"id":"apr_1","thread_id":"thr_a"},{"id":"apr_2","thread_id":"thr_a"},{"id":"apr_3","thread_id":"thr_c"}]}`), &list); err != nil {
		t.Fatal(err)
	}
	threads, approvals := snap.Threads, list.Items

	rows := threadRows(threads, approvals, "w1")
	if len(rows) != 2 || rows[0].ID != "thr_a" || rows[1].ID != "thr_b" {
		t.Fatalf("w1 lists %+v, want thr_a and thr_b", rows)
	}
	if a := rows[0]; a.Attention != "blocked" || a.WorktreeID != "wtr_a" || a.Pending != 2 || a.Queued || a.Title != "fix parser" {
		t.Errorf("thr_a's row %+v", a)
	}
	if b := rows[1]; b.Attention != "working" || !b.Queued || b.Pending != 0 || b.WorktreeID != "" || b.Mode != "auto-edit" || b.Agent != "opencode" {
		t.Errorf("thr_b's row %+v", b)
	}
	if rows := threadRows(threads, approvals, "w2"); len(rows) != 1 || rows[0].ID != "thr_c" || rows[0].Pending != 1 {
		t.Errorf("w2 lists %+v", rows)
	}
	if rows := threadRows(threads, approvals, ""); len(rows) != 0 {
		t.Errorf("no workspace lists %+v", rows)
	}
}

// TestATerminalSizeIsOneTheDaemonTakes_REQ_DSK_001: a page's size is refused before it is
// sent when the daemon would refuse it (API §5.9), and a size that would wrap uint16 is not
// taken for a small one.
func TestATerminalSizeIsOneTheDaemonTakes_REQ_DSK_001(t *testing.T) {
	t.Parallel()
	for _, ok := range [][2]int{{sessdomain.MinCols, sessdomain.MinRows}, {sessdomain.MaxCols, sessdomain.MaxRows}, {90, 20}} {
		if _, err := termSize(ok[0], ok[1]); err != nil {
			t.Errorf("%dx%d refused: %v", ok[0], ok[1], err)
		}
	}
	for _, bad := range [][2]int{{sessdomain.MinCols - 1, 20}, {90, sessdomain.MinRows - 1}, {sessdomain.MaxCols + 1, 20}, {90, sessdomain.MaxRows + 1}, {65536 + 90, 20}, {-1, 20}} {
		if _, err := termSize(bad[0], bad[1]); err == nil {
			t.Errorf("%dx%d accepted", bad[0], bad[1])
		}
	}
}
