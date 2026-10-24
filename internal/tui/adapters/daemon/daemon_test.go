package daemon

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/ecrespo/umbral/internal/client"
	"github.com/ecrespo/umbral/internal/tui/ports"
)

// This file exists because this package is the only place in the client that knows method
// names, parameter shapes and notification names — and the model's tests all use a fake
// Daemon, so they walk straight past it. That is exactly the blind spot the `client_kind`
// bug lived in: every fake answered the handshake without reading it.

// TestToEventMapsTheNotificationsTheSpecDefines_REQ_TERM_004 feeds literal frames of the
// shape API Spec §6 documents and checks what the model would be told.
//
// The `session.unsubscribed` row is the one that matters most: it is one word apart in the
// notification table from an event that is fatal, and mapping it wrong strands a working
// connection on a frozen screen.
func TestToEventMapsTheNotificationsTheSpecDefines_REQ_TERM_004(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		method string
		params string
		want   ports.EventKind
		wantID string
		data   string
		seq    uint64
		drop   bool
	}{
		{
			name: "output", method: "session.output",
			params: `{"session_id":"ses_x","seq":9,"data_b64":"aGk="}`,
			want:   ports.EventOutput, wantID: "ses_x", data: "hi", seq: 9,
		},
		{
			name: "block closed", method: "block.closed",
			params: `{"id":"blk_x","session_id":"ses_x","state":"finished"}`,
			want:   ports.EventBlockClosed, wantID: "ses_x",
		},
		{
			name: "session exited", method: "session.exited",
			params: `{"session_id":"ses_x","exit_code":0,"exited_at":1757592000000}`,
			want:   ports.EventSessionExited, wantID: "ses_x",
		},
		{
			// API Spec §6: the connection is still up and the client re-subscribes.
			name: "subscription dropped", method: "session.unsubscribed",
			params: `{"session_id":"ses_x","reason":"slow_client"}`,
			want:   ports.EventUnsubscribed, wantID: "ses_x",
		},
		{
			// §9 makes a new notification an additive change, so one this client does
			// not know must be ignored rather than treated as an error.
			name: "unknown method", method: "thread.stalled",
			params: `{"thread_id":"thr_x"}`, drop: true,
		},
		{
			name: "malformed params", method: "session.output",
			params: `{"session_id":"ses_x","data_b64":"not base64!!"}`, drop: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ev, ok := toEvent(client.Notification{
				Method: tc.method, Params: json.RawMessage(tc.params),
			})
			if tc.drop {
				if ok {
					t.Fatalf("%s produced an event; it should be ignored", tc.method)
				}
				return
			}
			if !ok {
				t.Fatalf("%s produced no event", tc.method)
			}
			if ev.Kind != tc.want {
				t.Errorf("kind = %v, want %v", ev.Kind, tc.want)
			}
			if ev.SessionID != tc.wantID {
				t.Errorf("session_id = %q, want %q", ev.SessionID, tc.wantID)
			}
			if tc.data != "" && string(ev.Data) != tc.data {
				t.Errorf("data = %q, want %q", ev.Data, tc.data)
			}
			if tc.seq != 0 && ev.Seq != tc.seq {
				t.Errorf("seq = %d, want %d", ev.Seq, tc.seq)
			}
		})
	}
}

// TestUnsubscribedIsNotADisconnection pins the distinction on its own, because the two
// map to different recoveries and the compiler cannot tell them apart.
func TestUnsubscribedIsNotADisconnection(t *testing.T) {
	t.Parallel()

	ev, ok := toEvent(client.Notification{
		Method: "session.unsubscribed",
		Params: json.RawMessage(`{"session_id":"ses_x","reason":"slow_client"}`),
	})
	if !ok {
		t.Fatal("session.unsubscribed produced no event")
	}
	if ev.Kind == ports.EventDisconnected {
		t.Fatal("a dropped subscription was reported as losing the daemon; API Spec §6 says re-subscribe")
	}
	if ev.Err == nil {
		t.Error("the event carries no reason; the status line would say nothing useful")
	}
}

// TestSubscribeRefusesASnapshotItCannotReplay: the daemon promises `format: "vt"`
// (API Spec §5.11). Feeding something else to a VT parser would draw garbage rather than
// report a mismatch, so the format is checked instead of assumed.
func TestSubscribeRefusesASnapshotItCannotReplay(t *testing.T) {
	t.Parallel()

	err := checkSnapshotFormat("png")
	if err == nil {
		t.Fatal("a snapshot in an unknown format was accepted")
	}
	if !errors.Is(err, errUnsupportedSnapshot) {
		t.Errorf("error = %v, want one that names the format mismatch", err)
	}
	if err := checkSnapshotFormat("vt"); err != nil {
		t.Errorf("the documented format was refused: %v", err)
	}
}

// TestToEventMapsTheAgentsNotifications_REQ_TUI_001: the four notifications the agent panel
// draws from, in the shapes API Spec §4 and §6 give them.
func TestToEventMapsTheAgentsNotifications_REQ_TUI_001(t *testing.T) {
	t.Parallel()

	ev, ok := toEvent(client.Notification{Method: "thread.delta", Params: json.RawMessage(
		`{"thread_id":"thr_x","turn_id":"trn_x","kind":"reasoning","text":"hmm"}`)})
	if !ok || ev.Kind != ports.EventThreadDelta || ev.ThreadID != "thr_x" || ev.Text != "hmm" || !ev.Reasoning {
		t.Fatalf("thread.delta = %+v %v", ev, ok)
	}
	ev, ok = toEvent(client.Notification{Method: "thread.tool_call", Params: json.RawMessage(
		`{"id":"tc_x","thread_id":"thr_x","message_id":"msg_x","tool":"run_command","risk":"Exec","args":{"command":"ls"},"status":"ok"}`)})
	if !ok || ev.Kind != ports.EventToolCall || ev.ThreadID != "thr_x" || ev.Tool != "run_command" || ev.Status != "ok" || ev.ToolCallID != "tc_x" {
		t.Fatalf("thread.tool_call = %+v %v", ev, ok)
	}
	ev, ok = toEvent(client.Notification{Method: "approval.requested", Params: json.RawMessage(
		`{"id":"apr_x","thread_id":"thr_x","tool_call_id":"tc_x","tool":"write_file","risk":"WriteFS","reason":"policy","summary":"a.go","diff":"+x\n","state":"pending"}`)})
	want := ports.Approval{ID: "apr_x", ThreadID: "thr_x", Tool: "write_file", Risk: "WriteFS", Reason: "policy", Summary: "a.go", Diff: "+x\n"}
	if !ok || ev.Kind != ports.EventApprovalRequested || ev.ThreadID != "thr_x" || ev.Approval != want {
		t.Fatalf("approval.requested = %+v %v", ev, ok)
	}
	ev, ok = toEvent(client.Notification{Method: "thread.turn_finished", Params: json.RawMessage(
		`{"thread_id":"thr_x","turn_id":"trn_x","stop_reason":"end_turn","usage":{"in_tokens":1,"out_tokens":2,"cost_micro_usd":0}}`)})
	if !ok || ev.Kind != ports.EventTurnFinished || ev.ThreadID != "thr_x" || ev.StopReason != "end_turn" {
		t.Fatalf("thread.turn_finished = %+v %v", ev, ok)
	}
	// A closed block says where its command ran: the panel's thread starts there.
	ev, ok = toEvent(client.Notification{Method: "block.closed", Params: json.RawMessage(
		`{"id":"blk_x","session_id":"ses_x","command":"ls","cwd":"/work/scratch","state":"closed"}`)})
	if !ok || ev.Kind != ports.EventBlockClosed || ev.SessionID != "ses_x" || ev.CWD != "/work/scratch" {
		t.Fatalf("block.closed = %+v %v", ev, ok)
	}
}

// TestEveryMessageGetsAFreshClientMsgID: REQ-AGT-015 needs a ULID per message, so a retry
// of the same call is recognised and two messages are not.
func TestEveryMessageGetsAFreshClientMsgID(t *testing.T) {
	t.Parallel()
	a, b := newClientMsgID(), newClientMsgID()
	if len(a) != 26 || len(b) != 26 || a == b {
		t.Fatalf("client_msg_ids %q and %q", a, b)
	}
}

// TestToEventMapsAPostedNotification_REQ_NTF_001: the daemon delivers a notification to the
// foreground client as `notification.posted` (API Spec §6); the TUI must see it.
func TestToEventMapsAPostedNotification_REQ_NTF_001(t *testing.T) {
	t.Parallel()
	ev, ok := toEvent(client.Notification{Method: "notification.posted", Params: json.RawMessage(
		`{"title":"claude needs you","body":"approve the edit?","source":"claude-code","sound":"request"}`)})
	if !ok || ev.Kind != ports.EventNotification || ev.Title != "claude needs you" ||
		ev.Text != "approve the edit?" || ev.Sound != "request" {
		t.Fatalf("notification.posted = %+v %v", ev, ok)
	}
}

// TestToEventMapsTheExtensionsNotifications_REQ_TUI_004: the extensions view lists again on
// `mcp.server_state` and `skill.changed`, and a turn's skills come from a `skill_load`
// call's own args; another tool's `name` argument is not a skill.
func TestToEventMapsTheExtensionsNotifications_REQ_TUI_004(t *testing.T) {
	t.Parallel()
	ev, ok := toEvent(client.Notification{Method: "mcp.server_state", Params: json.RawMessage(
		`{"name":"gitlab","state":"unavailable","last_error":"connection refused"}`)})
	if !ok || ev.Kind != ports.EventMcpServerState {
		t.Fatalf("mcp.server_state = %+v %v", ev, ok)
	}
	ev, ok = toEvent(client.Notification{Method: "skill.changed", Params: json.RawMessage(`{"name":"release-check","change":"disabled"}`)})
	if !ok || ev.Kind != ports.EventSkillChanged {
		t.Fatalf("skill.changed = %+v %v", ev, ok)
	}
	ev, ok = toEvent(client.Notification{Method: "thread.tool_call", Params: json.RawMessage(
		`{"id":"tc_x","thread_id":"thr_x","tool":"skill_load","risk":"ReadOnly","args":{"name":"release-check","file":"notes.md"},"status":"ok"}`)})
	if !ok || ev.Kind != ports.EventToolCall || ev.Skill != "release-check" {
		t.Fatalf("skill_load's tool_call = %+v %v", ev, ok)
	}
	ev, ok = toEvent(client.Notification{Method: "thread.tool_call", Params: json.RawMessage(
		`{"id":"tc_y","thread_id":"thr_x","tool":"mcp_gitlab_search","risk":"Network","args":{"name":"release-check"},"status":"ok"}`)})
	if !ok || ev.Skill != "" || ev.Tool != "mcp_gitlab_search" {
		t.Fatalf("an MCP tool's tool_call = %+v %v", ev, ok)
	}
}

// TestADaemonWithoutTheModuleListsNothing: NOT_IMPLEMENTED is a daemon run without its MCP
// client or skill store, which the panel shows as none rather than as an error; any other
// error is still one.
func TestADaemonWithoutTheModuleListsNothing(t *testing.T) {
	t.Parallel()
	if err := notImplementedIsEmpty(&client.Error{DomainCode: "NOT_IMPLEMENTED"}); err != nil {
		t.Errorf("NOT_IMPLEMENTED = %v, want none", err)
	}
	if err := notImplementedIsEmpty(&client.Error{DomainCode: "INTERNAL_ERROR"}); err == nil {
		t.Error("INTERNAL_ERROR was swallowed")
	}
}

// TestToEventMapsTheThreadListNotifications_REQ_ORC_003: the two notifications after which
// the panel lists its threads again, an attention change carrying its thread.
func TestToEventMapsTheThreadListNotifications_REQ_ORC_003(t *testing.T) {
	t.Parallel()

	ev, ok := toEvent(client.Notification{
		Method: "thread.attention_changed",
		Params: json.RawMessage(`{"thread_id":"thr_x","attention_state":"blocked"}`),
	})
	if !ok || ev.Kind != ports.EventAttentionChanged || ev.ThreadID != "thr_x" {
		t.Errorf("thread.attention_changed became %+v, %v", ev, ok)
	}
	ev, ok = toEvent(client.Notification{
		Method: "workspace.focused",
		Params: json.RawMessage(`{"workspace_id":"w2"}`),
	})
	if !ok || ev.Kind != ports.EventWorkspaceFocused {
		t.Errorf("workspace.focused became %+v, %v", ev, ok)
	}
	// Closing a workspace can move the focus too.
	ev, ok = toEvent(client.Notification{
		Method: "workspace.closed",
		Params: json.RawMessage(`{"id":"w2"}`),
	})
	if !ok || ev.Kind != ports.EventWorkspaceFocused {
		t.Errorf("workspace.closed became %+v, %v", ev, ok)
	}
}

// TestTheThreadListReadsTheSnapshotsNames_REQ_ORC_003: a `session.snapshot` result of API
// §5.3's shape, with §4's Thread fields, becomes the list the panel shows. A wrong field name
// would decode as empty and every fake-daemon test would pass through it.
func TestTheThreadListReadsTheSnapshotsNames_REQ_ORC_003(t *testing.T) {
	t.Parallel()

	var w wireSnapshot
	raw := `{"seq":42,"focused":{"workspace_id":"w1","tab_id":null,"thread_id":null},
		"threads":[{"id":"thr_a","title":"fix","workspace_id":"w1","worktree_id":"wtr_a",
		"attention_state":"blocked","state":"awaiting_approval","queued":true},
		{"id":"thr_b","workspace_id":null,"worktree_id":null,"attention_state":"idle","state":"idle","queued":false}]}`
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		t.Fatal(err)
	}
	got := w.list()
	want := ports.ThreadList{Seq: 42, WorkspaceID: "w1", Threads: []ports.Thread{
		{ID: "thr_a", Title: "fix", WorkspaceID: "w1", WorktreeID: "wtr_a", Attention: "blocked", State: "awaiting_approval", Queued: true},
		{ID: "thr_b", Attention: "idle", State: "idle"},
	}}
	if got.Seq != want.Seq || got.WorkspaceID != want.WorkspaceID || len(got.Threads) != 2 ||
		got.Threads[0] != want.Threads[0] || got.Threads[1] != want.Threads[1] {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	var none wireSnapshot
	if err := json.Unmarshal([]byte(`{"seq":1,"focused":{"workspace_id":null},"threads":[]}`), &none); err != nil {
		t.Fatal(err)
	}
	if none.list().WorkspaceID != "" {
		t.Fatal("a null focused workspace is not none")
	}
}

// TestTheThreadListReadsAHostedThread_REQ_ACP_007: a hosted thread's `agent` and
// `egress_visibility` reach the panel; Umbral's own thread has a null agent and `observed`.
func TestTheThreadListReadsAHostedThread_REQ_ACP_007(t *testing.T) {
	t.Parallel()
	var w wireSnapshot
	raw := `{"seq":1,"focused":{"workspace_id":"w1"},"threads":[
		{"id":"thr_h","agent":"opencode","egress_visibility":"unobserved","attention_state":"idle","state":"idle"},
		{"id":"thr_o","agent":null,"egress_visibility":"observed","attention_state":"idle","state":"idle"}]}`
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		t.Fatal(err)
	}
	got := w.list().Threads
	if len(got) != 2 || got[0].Agent != "opencode" || got[0].EgressVisibility != "unobserved" ||
		got[1].Agent != "" || got[1].EgressVisibility != "observed" {
		t.Fatalf("got %+v", got)
	}
}
