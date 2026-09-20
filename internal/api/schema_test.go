package api

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ecrespo/umbral/internal/bus"
	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
	sessports "github.com/ecrespo/umbral/internal/sessions/ports"
	wsports "github.com/ecrespo/umbral/internal/workspaces/ports"
)

// TestUnknownMethodKeepsConnection_REQ_API_003 is the criterion: a method name this build
// does not know is refused, and everything else on that connection still works.
//
// The refusal is the easy half and was already covered. The half that matters is the one
// this test is named for — that the connection survives — because a daemon that closed on an
// unknown name would make every client's version check fatal, which is the opposite of what
// REQ-API-003 asks for.
func TestUnknownMethodKeepsConnection_REQ_API_003(t *testing.T) {
	t.Parallel()

	sessions := newFakeSessions()
	s := testServerWithSessions(t, sessions)
	c := dial(t, s)
	if resp := c.hello(s.Token(), ClientTUI); resp.Error != nil {
		t.Fatalf("handshake: %+v", resp.Error)
	}

	// A name from a daemon newer than this one.
	resp := c.call(2, "thread.hibernate", map[string]any{})
	if resp.Error == nil || resp.Error.Code != codeMethodNotFound {
		t.Fatalf("unknown method = %+v, want METHOD_NOT_FOUND", resp)
	}
	if resp.Error.Data == nil || resp.Error.Data.DomainCode != "METHOD_NOT_FOUND" {
		t.Errorf("domain_code = %+v, want METHOD_NOT_FOUND", resp.Error.Data)
	}

	// The connection and every other capability are still usable. Three calls rather than
	// one, because "the socket did not close" and "the server still dispatches" are
	// different claims and only the second one is the requirement.
	if resp := c.call(3, "session.list", map[string]any{}); resp.Error != nil {
		t.Errorf("session.list after the unknown method: %+v", resp.Error)
	}
	if resp := c.call(4, "system.status", map[string]any{}); resp.Error != nil {
		t.Errorf("system.status after the unknown method: %+v", resp.Error)
	}
	if resp := c.call(5, "thread.hibernate", map[string]any{}); resp.Error == nil {
		t.Error("the second unknown method succeeded")
	}
	if resp := c.call(6, "session.list", map[string]any{}); resp.Error != nil {
		t.Errorf("session.list after two unknown methods: %+v", resp.Error)
	}
}

// TestUnservedMethodIsNotImplemented_REQ_API_003 is the requirement's second sentence, and
// the difference from the first is the whole point of having two codes.
//
// This daemon is built without the workspace module. `workspace.create` is a name it knows —
// the protocol has it, the method table has it — so refusing with METHOD_NOT_FOUND would
// tell a client the daemon is too old, when what is true is that this build lacks the
// module. The connection stays usable either way.
func TestUnservedMethodIsNotImplemented_REQ_API_003(t *testing.T) {
	t.Parallel()

	sessions := newFakeSessions()
	s := testServerWithSessions(t, sessions) // no Workspaces
	c := dial(t, s)
	if resp := c.hello(s.Token(), ClientTUI); resp.Error != nil {
		t.Fatalf("handshake: %+v", resp.Error)
	}

	resp := c.call(2, "workspace.create", map[string]any{"cwd": "/tmp"})
	if resp.Error == nil || resp.Error.Code != codeNotImplemented {
		t.Fatalf("workspace.create on a daemon with no tree = %+v, want NOT_IMPLEMENTED (%d)",
			resp.Error, codeNotImplemented)
	}
	if resp.Error.Data == nil || resp.Error.Data.DomainCode != "NOT_IMPLEMENTED" {
		t.Errorf("domain_code = %+v, want NOT_IMPLEMENTED", resp.Error.Data)
	}

	// REQ-API-003: "keep the connection and every other capability usable".
	if resp := c.call(3, "session.list", map[string]any{}); resp.Error != nil {
		t.Errorf("session.list after NOT_IMPLEMENTED: %+v", resp.Error)
	}
}

// TestEveryMethodDeclaresItsShapes_REQ_API_004 closes the hole the shapes could otherwise
// leave: a method registered without them publishes `params: {}` and `result: {}`, which is
// a schema that is quietly wrong rather than a build that fails.
//
// `emptyResult{}` is how a method says it really takes or returns nothing, so there is no
// way to satisfy this by accident.
func TestEveryMethodDeclaresItsShapes_REQ_API_004(t *testing.T) {
	t.Parallel()

	s := &Server{cfg: Config{}}
	for name, m := range s.registry() {
		if m.params == nil {
			t.Errorf("method %q declares no params shape; use emptyResult{} if it takes none", name)
		}
		if m.result == nil {
			t.Errorf("method %q declares no result shape; use emptyResult{} if it returns none", name)
		}
	}
}

// TestEveryNotificationDeclaresItsShape_REQ_API_004 is the same guarantee for §6.
//
// `toNotification` is a type switch over bus events, so nothing in the type system connects
// it to the shape table. This walks the switch with one event of every kind the daemon
// dispatches and asserts each method it can emit has a published payload.
func TestEveryNotificationDeclaresItsShape_REQ_API_004(t *testing.T) {
	t.Parallel()

	for _, e := range everyDispatchedEvent() {
		method, payload := toNotification(e.event)
		if method == "" {
			t.Errorf("%s produced no notification method", e.kind)
			continue
		}
		shape, ok := notificationShapes[method]
		if !ok {
			t.Errorf("the daemon emits %q and notificationShapes does not list it, so "+
				"api.schema omits it from §6", method)
			continue
		}
		if got, want := typeName(payload), typeName(shape); got != want {
			t.Errorf("%q is emitted as %s and published as %s", method, got, want)
		}
	}
}

// everyDispatchedEvent is one bus event of every kind the daemon turns into a notification.
//
// It is the one list: TestEveryWorkspaceEventIsDispatched checks it against
// `dispatchedKinds`, and the test above checks it against `notificationShapes`. Keeping a
// second copy beside either of them would be a third thing to update, and the whole point of
// both tests is that nobody remembers to.
//
// `session.output` and `session.unsubscribed` are absent on purpose: neither comes from the
// bus. Output is dispatched straight to the subscriptions and `session.unsubscribed` is
// emitted by a subscription's own goroutine, so `toNotification` has no case for either.
// They are covered by TestSchemaPublishesTheNonBusNotifications_REQ_API_004.
func everyDispatchedEvent() []struct {
	kind  bus.Kind
	event bus.Event
} {
	sample := sampleTree()
	block := sessdomain.Block{ID: "blk_01J9Z3K8T2QH6W4V5X7Y8Z9A0B", SessionID: fakeSessionID}
	return []struct {
		kind  bus.Kind
		event bus.Event
	}{
		{sessports.KindSessionExited, sessports.SessionExited{SessionID: fakeSessionID}},
		{sessports.KindSessionResized, sessports.SessionResized{SessionID: fakeSessionID}},
		{sessports.KindSessionInputOwner, sessports.SessionInputOwner{SessionID: fakeSessionID}},
		{sessports.KindSessionIntegration, sessports.SessionIntegration{SessionID: fakeSessionID}},
		{sessports.KindBlockStarted, sessports.BlockStarted{Block: block}},
		{sessports.KindBlockUpdated, sessports.BlockUpdated{BlockID: block.ID}},
		{sessports.KindBlockClosed, sessports.BlockClosed{Block: block}},
		{wsports.KindWorkspaceCreated, wsports.WorkspaceEvent{Kind: wsports.KindWorkspaceCreated, Workspace: sample.Workspace}},
		{wsports.KindWorkspaceUpdated, wsports.WorkspaceEvent{Kind: wsports.KindWorkspaceUpdated, Workspace: sample.Workspace}},
		{wsports.KindWorkspaceClosed, wsports.WorkspaceEvent{Kind: wsports.KindWorkspaceClosed, Workspace: sample.Workspace}},
		{wsports.KindWorkspaceFocused, wsports.WorkspaceEvent{Kind: wsports.KindWorkspaceFocused, Workspace: sample.Workspace}},
		{wsports.KindTabCreated, wsports.TabEvent{Kind: wsports.KindTabCreated, Tab: sample.Tab}},
		{wsports.KindTabClosed, wsports.TabEvent{Kind: wsports.KindTabClosed, Tab: sample.Tab}},
		{wsports.KindTabFocused, wsports.TabEvent{Kind: wsports.KindTabFocused, Tab: sample.Tab}},
		{wsports.KindPaneCreated, wsports.PaneEvent{Kind: wsports.KindPaneCreated, Pane: sample.RootPane}},
		{wsports.KindPaneUpdated, wsports.PaneEvent{Kind: wsports.KindPaneUpdated, Pane: sample.RootPane}},
		{wsports.KindPaneClosed, wsports.PaneEvent{Kind: wsports.KindPaneClosed, Pane: sample.RootPane}},
		{wsports.KindPaneFocused, wsports.PaneEvent{Kind: wsports.KindPaneFocused, Pane: sample.RootPane}},
		{wsports.KindPaneMoved, wsports.PaneMoved{Pane: sample.RootPane}},
		{wsports.KindLayoutUpdated, wsports.LayoutUpdated{}},
	}
}

func typeName(v any) string { return reflect.TypeOf(v).String() }

// TestSchemaPublishesTheNonBusNotifications_REQ_API_004 covers the two §6 methods that
// `toNotification` never produces, because they do not come from the bus: `session.output`
// is dispatched straight to the subscriptions, and `session.unsubscribed` is emitted by a
// subscription's own goroutine when its queue overflows.
//
// Nothing else would notice them missing: the walk above cannot reach them, and a client
// only finds out by receiving a notification the schema does not describe.
func TestSchemaPublishesTheNonBusNotifications_REQ_API_004(t *testing.T) {
	t.Parallel()

	published := map[string]bool{}
	for _, n := range Schema(Config{}).Notifications {
		published[n.Method] = true
	}
	for _, method := range []string{"session.output", "session.unsubscribed"} {
		if !published[method] {
			t.Errorf("the daemon emits %q and api.schema does not describe it", method)
		}
	}
}

// TestSchemaMatchesSpec_REQ_API_004 runs the comparison against the specification, so the
// drift REQ-API-004 forbids fails `task test` and not only the dedicated CI job.
//
// The checker is `tools/api_schema_check.py`: it parses §3, §5 and §6 and compares them with
// the document this package generates. Two independent derivations of one contract, which is
// the only arrangement in which "they agree" means anything.
func TestSchemaMatchesSpec_REQ_API_004(t *testing.T) {
	t.Parallel()

	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skipf("python3 is not installed: %v", err)
	}

	doc := Schema(Config{})
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal the schema: %v", err)
	}
	path := filepath.Join(t.TempDir(), "schema.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatalf("write the schema: %v", err)
	}

	cmd := exec.CommandContext(t.Context(), python, "tools/api_schema_check.py", path)
	cmd.Dir = "../.."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the binary and specs/api/umbral-daemon-api-v1.md disagree:\n%s", out)
	}
	t.Log(strings.TrimSpace(string(out)))
}

// TestSchemaCoversTheWholeSurface_REQ_API_004 asserts the document is about this protocol
// rather than an empty shell that would pass the comparison by having nothing in it.
func TestSchemaCoversTheWholeSurface_REQ_API_004(t *testing.T) {
	t.Parallel()

	doc := Schema(Config{})
	if doc.ProtocolVersion != ProtocolVersion {
		t.Errorf("protocol_version = %d, want %d", doc.ProtocolVersion, ProtocolVersion)
	}

	names := make([]string, 0, len(doc.Methods))
	for _, m := range doc.Methods {
		names = append(names, m.Name)
	}
	for _, want := range []string{"system.hello", "session.create", "block.search", "api.schema"} {
		if !slices.Contains(names, want) {
			t.Errorf("the schema omits %q", want)
		}
	}

	// Errors are the third blocking comparison, and the table is complete rather than the
	// subset F0 can currently raise: a client switching on `domain_code` needs every one.
	if len(doc.Errors) != len(errorCodes) {
		t.Errorf("the schema publishes %d error codes, the daemon knows %d",
			len(doc.Errors), len(errorCodes))
	}

	// A method's parameters are what the comparison is blocking on, so an empty params
	// object for a method that plainly takes one would make the check vacuous.
	for _, m := range doc.Methods {
		if m.Name != "session.input" {
			continue
		}
		if !slices.Contains(m.Params.Required, "session_id") ||
			!slices.Contains(m.Params.Required, "data_b64") {
			t.Errorf("session.input requires %v, want session_id and data_b64", m.Params.Required)
		}
	}
}

// TestSchemaReportsWhatThisBuildServes_REQ_API_003 pins the `served` member, which is how a
// client tells a daemon that lacks a module from one that never had the method.
func TestSchemaReportsWhatThisBuildServes_REQ_API_003(t *testing.T) {
	t.Parallel()

	bare := Schema(Config{})
	for _, m := range bare.Methods {
		if m.Name == "workspace.create" && m.Served {
			t.Error("workspace.create reports itself served on a daemon with no tree")
		}
		if m.Name == "api.schema" && !m.Served {
			t.Error("api.schema reports itself unserved; it needs no module")
		}
	}

	wired := Schema(Config{Workspaces: &fakeTree{tree: sampleTree()}})
	for _, m := range wired.Methods {
		if m.Name == "workspace.create" && !m.Served {
			t.Error("workspace.create reports itself unserved on a daemon with a tree")
		}
	}
}
