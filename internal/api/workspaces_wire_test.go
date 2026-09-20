package api

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/bus"
	wsdomain "github.com/ecrespo/umbral/internal/workspaces/domain"
	wsports "github.com/ecrespo/umbral/internal/workspaces/ports"
)

// These tests exist because `internal/api/workspaces.go` is five hundred lines of
// translation that the module's own integration tests walk straight past. Two contradictions
// of API Spec §5.4-§5.6 lived in it until they were written: every error the tree raised
// reached the client as INTERNAL_ERROR with a trace id attached, and the §6 notifications
// wrapped their object in an envelope §6 does not describe.

// fakeTree is a ports.Workspaces that records what the handler passed down and answers with
// whatever the test set. The tree's behaviour is tested against real SQLite elsewhere; what
// is tested here is the wire, which nothing else can reach.
type fakeTree struct {
	tree     wsdomain.Tree
	pane     wsdomain.Pane
	layout   wsdomain.Layout
	moved    wsports.Moved
	err      error
	lastCall struct {
		createParams wsdomain.CreateWorkspaceParams
		splitParams  wsdomain.SplitParams
		moveParams   wsdomain.MoveParams
		id           string
		label        string
		focus        bool
		closePanes   bool
	}
}

func (f *fakeTree) CreateWorkspace(_ context.Context, p wsdomain.CreateWorkspaceParams) (wsdomain.Tree, error) {
	f.lastCall.createParams = p
	return f.tree, f.err
}

func (f *fakeTree) ListWorkspaces(context.Context) ([]wsdomain.Workspace, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []wsdomain.Workspace{f.tree.Workspace}, nil
}

func (f *fakeTree) FocusWorkspace(_ context.Context, id string) (wsdomain.Workspace, error) {
	f.lastCall.id = id
	return f.tree.Workspace, f.err
}

func (f *fakeTree) RenameWorkspace(_ context.Context, id, label string) (wsdomain.Workspace, error) {
	f.lastCall.id, f.lastCall.label = id, label
	return f.tree.Workspace, f.err
}

func (f *fakeTree) CloseWorkspace(_ context.Context, id string, closePanes bool) error {
	f.lastCall.id, f.lastCall.closePanes = id, closePanes
	return f.err
}

func (f *fakeTree) CreateTab(_ context.Context, workspaceID, label string, focus bool) (wsdomain.Tab, wsdomain.Pane, error) {
	f.lastCall.id, f.lastCall.label, f.lastCall.focus = workspaceID, label, focus
	return f.tree.Tab, f.tree.RootPane, f.err
}

func (f *fakeTree) ListTabs(_ context.Context, workspaceID string) ([]wsdomain.Tab, error) {
	f.lastCall.id = workspaceID
	if f.err != nil {
		return nil, f.err
	}
	return []wsdomain.Tab{f.tree.Tab}, nil
}

func (f *fakeTree) FocusTab(_ context.Context, id string) (wsdomain.Tab, error) {
	f.lastCall.id = id
	return f.tree.Tab, f.err
}

func (f *fakeTree) RenameTab(_ context.Context, id, label string) (wsdomain.Tab, error) {
	f.lastCall.id, f.lastCall.label = id, label
	return f.tree.Tab, f.err
}

func (f *fakeTree) CloseTab(_ context.Context, id string) error {
	f.lastCall.id = id
	return f.err
}

func (f *fakeTree) SplitPane(_ context.Context, p wsdomain.SplitParams) (wsdomain.Pane, wsdomain.Layout, error) {
	f.lastCall.splitParams = p
	return f.pane, f.layout, f.err
}

func (f *fakeTree) ListPanes(_ context.Context, tabID string) ([]wsdomain.Pane, error) {
	f.lastCall.id = tabID
	if f.err != nil {
		return nil, f.err
	}
	return []wsdomain.Pane{f.pane}, nil
}

func (f *fakeTree) GetPane(_ context.Context, id string) (wsdomain.Pane, error) {
	f.lastCall.id = id
	return f.pane, f.err
}

func (f *fakeTree) FocusPane(_ context.Context, id string) (wsdomain.Pane, error) {
	f.lastCall.id = id
	return f.pane, f.err
}

func (f *fakeTree) RenamePane(_ context.Context, id, label string) (wsdomain.Pane, error) {
	f.lastCall.id, f.lastCall.label = id, label
	return f.pane, f.err
}

func (f *fakeTree) MovePane(_ context.Context, p wsdomain.MoveParams) (wsports.Moved, error) {
	f.lastCall.moveParams = p
	return f.moved, f.err
}

func (f *fakeTree) ClosePane(_ context.Context, id string) error {
	f.lastCall.id = id
	return f.err
}

func (f *fakeTree) Layout(_ context.Context, tabID string) (wsdomain.Layout, error) {
	f.lastCall.id = tabID
	return f.layout, f.err
}

// sampleTree is a workspace with every field populated, so a missing one on the wire shows
// up as an absence rather than as a zero that might have been correct.
func sampleTree() wsdomain.Tree {
	created := time.UnixMilli(1757592000000).UTC()
	return wsdomain.Tree{
		Workspace: wsdomain.Workspace{
			ID: "w1", Label: "api", CWD: "/home/u/repo", OrderIndex: 0,
			RollupState: wsdomain.AttentionUnknown, TabIDs: []string{"w1:t1"},
			CreatedAt: created,
		},
		Tab: wsdomain.Tab{
			ID: "w1:t1", WorkspaceID: "w1", Label: "agents", OrderIndex: 0,
			FocusedPaneID: "w1:p1", CreatedAt: created,
		},
		RootPane: wsdomain.Pane{
			ID: "w1:p1", TabID: "w1:t1", WorkspaceID: "w1", SessionID: "ses_x",
			Label: "editor", CWD: "/home/u/repo", Aliases: []string{"w1:p1"},
			AttentionState: wsdomain.AttentionUnknown, CreatedAt: created,
		},
	}
}

func testServerWithTree(t *testing.T, tree wsports.Workspaces) *Server {
	t.Helper()

	dir := t.TempDir()
	eventBus := bus.New()
	t.Cleanup(eventBus.Close)

	s, err := Listen(t.Context(), Config{
		SocketPath:    filepath.Join(dir, SocketFileName),
		TokenPath:     filepath.Join(dir, TokenFileName),
		DaemonVersion: "0.1.0-test",
		Workspaces:    tree,
		Bus:           eventBus,
	})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- s.Serve(ctx) }()

	t.Cleanup(func() {
		cancel()
		_ = s.Close()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after Close")
		}
	})
	return s
}

// dialTree opens an authenticated TUI connection to a server carrying the tree.
func dialTree(t *testing.T, s *Server) *client {
	t.Helper()
	c := dial(t, s)
	if resp := c.hello(s.Token(), ClientTUI); resp.Error != nil {
		t.Fatalf("handshake: %+v", resp.Error)
	}
	return c
}

// resultOf re-marshals a result so a test can read the keys a client would see, rather than
// the Go types the handler happened to build.
func resultOf(t *testing.T, resp response) map[string]json.RawMessage {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("call failed: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("result is not an object: %v", err)
	}
	return out
}

func field(t *testing.T, obj map[string]json.RawMessage, key string) map[string]json.RawMessage {
	t.Helper()
	raw, ok := obj[key]
	if !ok {
		t.Fatalf("result has no %q; keys are %v", key, keysOf(obj))
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%q is not an object: %v", key, err)
	}
	return out
}

func keysOf(obj map[string]json.RawMessage) []string {
	out := make([]string, 0, len(obj))
	for k := range obj {
		out = append(out, k)
	}
	return out
}

// TestWorkspaceCreateWireShape_REQ_WS_001 checks the result keys §5.4 names and every field
// of the three objects §4 defines.
func TestWorkspaceCreateWireShape_REQ_WS_001(t *testing.T) {
	t.Parallel()

	tree := &fakeTree{tree: sampleTree()}
	c := dialTree(t, testServerWithTree(t, tree))

	got := resultOf(t, c.call(2, "workspace.create", map[string]any{
		"cwd": "/home/u/repo", "label": "api", "tab_label": "agents",
	}))
	for _, key := range []string{"workspace", "tab", "root_pane"} {
		if _, ok := got[key]; !ok {
			t.Errorf("result has no %q; §5.4 returns all three in one response", key)
		}
	}

	// §4 `Workspace`, field by field. A missing key is what a client notices, so the
	// presence of each is asserted and not merely the ones with interesting values.
	ws := field(t, got, "workspace")
	for _, key := range []string{
		"id", "label", "cwd", "order_index", "rollup_state", "tab_ids", "created_at", "closed_at",
	} {
		if _, ok := ws[key]; !ok {
			t.Errorf("Workspace has no %q; §4 defines it", key)
		}
	}
	if string(ws["rollup_state"]) != `"unknown"` {
		t.Errorf("rollup_state = %s, want \"unknown\"", ws["rollup_state"])
	}
	if string(ws["created_at"]) != "1757592000000" {
		t.Errorf("created_at = %s, want epoch milliseconds (Art. 6)", ws["created_at"])
	}
	if string(ws["closed_at"]) != "null" {
		t.Errorf("closed_at = %s, want null on an open workspace", ws["closed_at"])
	}

	tab := field(t, got, "tab")
	for _, key := range []string{
		"id", "workspace_id", "label", "order_index", "focused_pane_id", "created_at", "closed_at",
	} {
		if _, ok := tab[key]; !ok {
			t.Errorf("Tab has no %q; §4 defines it", key)
		}
	}

	pane := field(t, got, "root_pane")
	for _, key := range []string{
		"id", "tab_id", "workspace_id", "session_id", "thread_id", "label", "cwd",
		"aliases", "attention_state", "state_source", "metadata", "created_at", "closed_at",
	} {
		if _, ok := pane[key]; !ok {
			t.Errorf("Pane has no %q; §4 defines it", key)
		}
	}
	// §4 types these as `string | null`, and "" would make a client that checks for a
	// session find one that is not there.
	if string(pane["thread_id"]) != "null" {
		t.Errorf("thread_id = %s, want null", pane["thread_id"])
	}
	if string(pane["state_source"]) != "null" {
		t.Errorf("state_source = %s, want null while nothing has reported", pane["state_source"])
	}
	if string(pane["attention_state"]) != `"unknown"` {
		t.Errorf("attention_state = %s, want \"unknown\"", pane["attention_state"])
	}
}

// TestWorkspaceDefaultsFollowTheApiSpec covers the three optional parameters whose default
// is `true` or a number, which a Go zero value cannot express and a handler can silently
// invert.
func TestWorkspaceDefaultsFollowTheApiSpec(t *testing.T) {
	t.Parallel()

	tree := &fakeTree{tree: sampleTree()}
	c := dialTree(t, testServerWithTree(t, tree))

	// §5.4 `focus?: true`.
	c.call(2, "workspace.create", map[string]any{"cwd": "/tmp"})
	if !tree.lastCall.createParams.Focus {
		t.Error("workspace.create defaulted focus to false; §5.4 writes `focus?: true`")
	}
	c.call(3, "workspace.create", map[string]any{"cwd": "/tmp", "focus": false})
	if tree.lastCall.createParams.Focus {
		t.Error("workspace.create ignored an explicit focus:false")
	}

	// §5.4 `close_panes?: true`.
	c.call(4, "workspace.close", map[string]any{"workspace_id": "w1"})
	if !tree.lastCall.closePanes {
		t.Error("workspace.close defaulted close_panes to false; §5.4 writes `close_panes?: true`")
	}
	c.call(5, "workspace.close", map[string]any{"workspace_id": "w1", "close_panes": false})
	if tree.lastCall.closePanes {
		t.Error("workspace.close ignored an explicit close_panes:false")
	}

	// §5.6 `ratio?: 0.1-0.9 (default 0.5)`. The handler passes it through untouched and the
	// domain applies the default, so what is checked here is that it survives the trip.
	c.call(6, "pane.split", map[string]any{"pane_id": "w1:p1", "direction": "down", "ratio": 0.7})
	if tree.lastCall.splitParams.Ratio != 0.7 {
		t.Errorf("pane.split passed ratio %v, want 0.7", tree.lastCall.splitParams.Ratio)
	}
	if tree.lastCall.splitParams.Direction != wsdomain.SplitDown {
		t.Errorf("pane.split passed direction %q, want down", tree.lastCall.splitParams.Direction)
	}
	if !tree.lastCall.splitParams.Focus {
		t.Error("pane.split defaulted focus to false; §5.6 writes `focus?: true`")
	}
}

// TestWorkspaceErrorsReachTheWire is the test the two CRITICAL findings needed.
//
// The module returns its own sentinels and never a numeric code; `toWire` is what turns
// them into API Spec §3's table. Until this test existed, it did not: every sentinel fell
// through to the default and reached the client as INTERNAL_ERROR — the wrong code, and one
// that carries a `trace_id` Art. 7 reserves for real faults, minted by an ordinary typo.
func TestWorkspaceErrorsReachTheWire(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		err        error
		wantCode   int
		wantDomain string
		wantTrace  bool
	}{
		{"not found", wsdomain.ErrNotFound, codeNotFound, domainNotFound, false},
		{"validation", wsdomain.ErrValidation, codeValidationError, domainValidationError, false},
		{"conflict", wsdomain.ErrConflict, codeConflict, domainConflict, false},
		{"anything else", errors.New("the disk caught fire"), codeInternalError, "INTERNAL_ERROR", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tree := &fakeTree{tree: sampleTree(), err: tc.err}
			c := dialTree(t, testServerWithTree(t, tree))

			resp := c.call(2, "pane.get", map[string]any{"pane_id": "w1:p9"})
			if resp.Error == nil {
				t.Fatal("the call succeeded")
			}
			if resp.Error.Code != tc.wantCode {
				t.Errorf("code = %d, want %d (%s)", resp.Error.Code, tc.wantCode, tc.wantDomain)
			}
			raw, err := json.Marshal(resp.Error.Data)
			if err != nil {
				t.Fatalf("marshal error data: %v", err)
			}
			var data struct {
				DomainCode string `json:"domain_code"`
				TraceID    string `json:"trace_id"`
			}
			if err := json.Unmarshal(raw, &data); err != nil {
				t.Fatalf("error data is not an object: %v", err)
			}
			if data.DomainCode != tc.wantDomain {
				t.Errorf("domain_code = %q, want %q", data.DomainCode, tc.wantDomain)
			}
			// §3: "`trace_id` is present on INTERNAL_ERROR only (Art. 7)". A client typo
			// must not mint one.
			if hasTrace := data.TraceID != ""; hasTrace != tc.wantTrace {
				t.Errorf("trace_id present = %v, want %v", hasTrace, tc.wantTrace)
			}
		})
	}
}

// TestWorkspaceNotificationsCarryTheObject is the other CRITICAL finding's test.
//
// API Spec §6 types `workspace.created`, `tab.created` and `pane.created` as `Workspace`,
// `Tab` and `Pane` — the object itself, exactly as `block.started` carries a `Block`. An
// envelope would leave a client written against §6 looking for `id` one level down.
// `pane.moved` is the one exception, and §6 spells its payload out.
func TestWorkspaceNotificationsCarryTheObject(t *testing.T) {
	t.Parallel()

	sample := sampleTree()
	cases := []struct {
		name     string
		event    bus.Event
		method   string
		wantKeys []string
	}{
		{
			"workspace",
			wsports.WorkspaceEvent{
				Kind: wsports.KindWorkspaceCreated, Workspace: sample.Workspace,
			},
			"workspace.created",
			[]string{"id", "label", "rollup_state", "tab_ids"},
		},
		{
			"tab",
			wsports.TabEvent{Kind: wsports.KindTabCreated, Tab: sample.Tab},
			"tab.created",
			[]string{"id", "workspace_id", "focused_pane_id"},
		},
		{
			"pane",
			wsports.PaneEvent{Kind: wsports.KindPaneCreated, Pane: sample.RootPane},
			"pane.created",
			[]string{"id", "tab_id", "attention_state", "aliases"},
		},
		{
			"moved",
			wsports.PaneMoved{
				Pane: sample.RootPane, PreviousPaneID: "w2:p3", PreviousWorkspaceID: "w2",
			},
			"pane.moved",
			[]string{"pane", "previous_pane_id", "previous_workspace_id", "layout"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			method, params := toNotification(tc.event)
			if method != tc.method {
				t.Fatalf("method = %q, want %q", method, tc.method)
			}
			raw, err := json.Marshal(params)
			if err != nil {
				t.Fatalf("marshal params: %v", err)
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatalf("payload is not an object: %v", err)
			}
			for _, key := range tc.wantKeys {
				if _, ok := payload[key]; !ok {
					t.Errorf("%s payload has no %q; keys are %v", tc.method, key, keysOf(payload))
				}
			}
			// The three object notifications must not wrap: a payload with exactly one key
			// named after the object is the envelope §6 does not describe.
			if tc.method != "pane.moved" {
				for _, envelope := range []string{"workspace", "tab", "pane"} {
					if _, wrapped := payload[envelope]; wrapped && len(payload) == 1 {
						t.Errorf("%s wraps its object in a %q envelope; §6 types the payload as the object",
							tc.method, envelope)
					}
				}
			}
		})
	}
}

// TestWorkspaceMethodsRequireAnInteractiveClient pins API Spec §2: `cli` gets `system.*`,
// `block.*`, three `thread.*` and `model.list`, and nothing that arranges windows.
func TestWorkspaceMethodsRequireAnInteractiveClient(t *testing.T) {
	t.Parallel()

	s := testServerWithTree(t, &fakeTree{tree: sampleTree()})
	c := dial(t, s)
	if resp := c.hello(s.Token(), ClientCLI); resp.Error != nil {
		t.Fatalf("handshake: %+v", resp.Error)
	}

	for _, method := range []string{
		"workspace.create", "workspace.list", "tab.create", "pane.split", "pane.get",
	} {
		resp := c.call(2, method, nil)
		if resp.Error == nil {
			t.Errorf("a cli client was allowed to call %s", method)
			continue
		}
		if resp.Error.Code != codeMethodNotFound {
			t.Errorf("%s gave code %d, want METHOD_NOT_FOUND (%d)",
				method, resp.Error.Code, codeMethodNotFound)
		}
	}
}

// TestTheTreeIsAdvertisedAsWorkspaces pins the namespace name §2 uses. Three method
// prefixes, one capability: a client looking for `workspaces` must find it, and must not be
// offered three names §2 never defines.
func TestTheTreeIsAdvertisedAsWorkspaces(t *testing.T) {
	t.Parallel()

	s := testServerWithTree(t, &fakeTree{tree: sampleTree()})
	got := s.capabilities()

	found := false
	for _, capability := range got {
		switch capability {
		case capabilityWorkspaces:
			found = true
		case "workspace", "tab", "pane":
			t.Errorf("capabilities = %v, which advertises %q; API Spec §2 has no such namespace",
				got, capability)
		}
	}
	if !found {
		t.Errorf("capabilities = %v, want it to advertise %q", got, capabilityWorkspaces)
	}
}

// TestPaneSplitReturnsTheLayout_REQ_WS_003 checks the second half of §5.6's result, and that
// §4's `Layout` keeps its keys when a tab has emptied.
func TestPaneSplitReturnsTheLayout_REQ_WS_003(t *testing.T) {
	t.Parallel()

	sample := sampleTree()
	tree := &fakeTree{
		tree: sample, pane: sample.RootPane,
		layout: wsdomain.Layout{
			WorkspaceID: "w1", TabID: "w1:t1", FocusedPaneID: "w1:p2",
			Root: &wsdomain.Node{
				Type: wsdomain.NodeSplit, Direction: wsdomain.SplitRight, Ratio: 0.6,
				First:  &wsdomain.Node{Type: wsdomain.NodePane, PaneID: "w1:p1"},
				Second: &wsdomain.Node{Type: wsdomain.NodePane, PaneID: "w1:p2"},
			},
		},
	}
	c := dialTree(t, testServerWithTree(t, tree))

	got := resultOf(t, c.call(2, "pane.split", map[string]any{
		"pane_id": "w1:p1", "direction": "right", "ratio": 0.6,
	}))
	for _, key := range []string{"pane", "layout"} {
		if _, ok := got[key]; !ok {
			t.Errorf("result has no %q; §5.6 returns both", key)
		}
	}
	layout := field(t, got, "layout")
	for _, key := range []string{"workspace_id", "tab_id", "focused_pane_id", "root"} {
		if _, ok := layout[key]; !ok {
			t.Errorf("Layout has no %q; §4 defines it", key)
		}
	}

	// An emptied tab still has both keys, as null. The stored form omits them to keep the
	// column small; the wire form is read by a client that expects §4's shape.
	tree.layout = wsdomain.Layout{WorkspaceID: "w1", TabID: "w1:t1"}
	empty := field(t, resultOf(t, c.call(3, "pane.split", map[string]any{
		"pane_id": "w1:p1", "direction": "right",
	})), "layout")
	for _, key := range []string{"focused_pane_id", "root"} {
		if _, ok := empty[key]; !ok {
			t.Errorf("an emptied Layout omits %q; §4 lists it and a client reads it as null", key)
		}
	}
}

// TestMoveResultNamesWhereThePaneCameFrom pins §5.6's move result, which is what lets a
// client follow a terminal instead of watching a pane vanish (REQ-WS-007).
func TestMoveResultNamesWhereThePaneCameFrom(t *testing.T) {
	t.Parallel()

	sample := sampleTree()
	tree := &fakeTree{
		tree: sample,
		moved: wsports.Moved{
			Pane: sample.RootPane, PreviousPaneID: "w2:p7", PreviousWorkspaceID: "w2",
			Layout: wsdomain.Layout{WorkspaceID: "w1", TabID: "w1:t1"},
		},
	}
	c := dialTree(t, testServerWithTree(t, tree))

	got := resultOf(t, c.call(2, "pane.move", map[string]any{
		"pane_id":     "w2:p7",
		"destination": map[string]any{"type": "tab", "tab_id": "w1:t1"},
	}))
	for _, key := range []string{"pane", "previous_pane_id", "previous_workspace_id", "layout"} {
		if _, ok := got[key]; !ok {
			t.Errorf("result has no %q; §5.6 names it", key)
		}
	}
	if string(got["previous_pane_id"]) != `"w2:p7"` {
		t.Errorf("previous_pane_id = %s, want \"w2:p7\"", got["previous_pane_id"])
	}

	// The destination survived the trip rather than the call merely succeeding.
	if tree.lastCall.moveParams.Destination.Type != wsdomain.MoveToTab {
		t.Errorf("destination type reached the module as %q", tree.lastCall.moveParams.Destination.Type)
	}
	if tree.lastCall.moveParams.Destination.TabID != "w1:t1" {
		t.Errorf("destination tab reached the module as %q", tree.lastCall.moveParams.Destination.TabID)
	}
}

// TestListResultsUseItems pins the paging shape §5 uses everywhere, including that an empty
// list is `[]` and not `null` — a client iterating the value should not have to check.
func TestListResultsUseItems(t *testing.T) {
	t.Parallel()

	tree := &fakeTree{tree: sampleTree()}
	c := dialTree(t, testServerWithTree(t, tree))

	for _, tc := range []struct {
		method string
		params any
	}{
		{"workspace.list", nil},
		{"tab.list", map[string]any{"workspace_id": "w1"}},
		{"pane.list", map[string]any{"tab_id": "w1:t1"}},
	} {
		got := resultOf(t, c.call(2, tc.method, tc.params))
		raw, ok := got["items"]
		if !ok {
			t.Errorf("%s has no items; keys are %v", tc.method, keysOf(got))
			continue
		}
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			t.Errorf("%s items is not an array: %v", tc.method, err)
		}
		if len(items) != 1 {
			t.Errorf("%s returned %d items, want 1", tc.method, len(items))
		}
	}
}
