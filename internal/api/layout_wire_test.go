package api

import (
	"encoding/json"
	"testing"

	"github.com/ecrespo/umbral/internal/bus"

	wsdomain "github.com/ecrespo/umbral/internal/workspaces/domain"
	wsports "github.com/ecrespo/umbral/internal/workspaces/ports"
)

// TestLayoutExportWireShape_REQ_WS_004 pins §5.7: the result is a `Layout`, not a `Layout`
// wrapped in something, and `tab_id` is optional.
func TestLayoutExportWireShape_REQ_WS_004(t *testing.T) {
	t.Parallel()

	tree := &fakeTree{
		tree: sampleTree(),
		layout: wsdomain.Layout{
			WorkspaceID: "w1", TabID: "w1:t1", FocusedPaneID: "w1:p1",
			Root: &wsdomain.Node{
				Type: wsdomain.NodeSplit, Direction: wsdomain.SplitRight, Ratio: 0.6,
				First: &wsdomain.Node{
					Type: wsdomain.NodePane, PaneID: "w1:p1", Label: "editor", CWD: "/repo",
				},
				Second: &wsdomain.Node{
					Type: wsdomain.NodePane, PaneID: "w1:p2", Label: "tests", CWD: "/repo",
					Command: []string{"sh", "-c", "go test ./..."},
					Env:     map[string]string{"UMBRAL_ROLE": "tests"},
				},
			},
		},
	}
	c := dialTree(t, testServerWithTree(t, tree))

	got := resultOf(t, c.call(2, "layout.export", map[string]any{"tab_id": "w1:t1"}))
	for _, key := range []string{"workspace_id", "tab_id", "focused_pane_id", "root"} {
		if _, ok := got[key]; !ok {
			t.Errorf("result has no %q; §5.7 returns a Layout", key)
		}
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(got["root"], &root); err != nil {
		t.Fatalf("root is not an object: %v", err)
	}
	for _, key := range []string{"type", "direction", "ratio", "first", "second"} {
		if _, ok := root[key]; !ok {
			t.Errorf("the split node has no %q; §4 defines it", key)
		}
	}

	// REQ-WS-004 names three things a pane node must carry. The second pane has all three,
	// so their absence here is an absence and not a zero that might have been right.
	var second map[string]json.RawMessage
	if err := json.Unmarshal(root["second"], &second); err != nil {
		t.Fatalf("second is not an object: %v", err)
	}
	for _, key := range []string{"pane_id", "label", "cwd", "command", "env"} {
		if _, ok := second[key]; !ok {
			t.Errorf("the pane node has no %q; REQ-WS-004 names label, cwd and command", key)
		}
	}
	if string(second["command"]) != `["sh","-c","go test ./..."]` {
		t.Errorf("command = %s, want the argv it was exported with", second["command"])
	}

	// §5.7 makes tab_id optional, and an omitted one means the focused tab.
	tree.lastCall.id = "sentinel"
	resultOf(t, c.call(3, "layout.export", map[string]any{}))
	if tree.lastCall.id != "" {
		t.Errorf("an omitted tab_id reached the module as %q, want the empty string that means "+
			"the focused tab", tree.lastCall.id)
	}
}

// TestLayoutApplyWireShape_REQ_WS_005 pins §5.8's result, including the warnings the
// requirement makes part of the response rather than something a client has to know.
func TestLayoutApplyWireShape_REQ_WS_005(t *testing.T) {
	t.Parallel()

	sample := sampleTree()
	tree := &fakeTree{
		tree: sample,
		applied: wsdomain.Applied{
			Tab:      sample.Tab,
			Panes:    []wsdomain.Pane{sample.RootPane},
			Warnings: []string{wsdomain.ApplyWarning},
		},
	}
	c := dialTree(t, testServerWithTree(t, tree))

	got := resultOf(t, c.call(2, "layout.apply", map[string]any{
		"workspace_id": "w1", "tab_label": "restored",
		"root": map[string]any{
			"type": "split", "direction": "right", "ratio": 0.5,
			"first":  map[string]any{"type": "pane", "label": "a", "cwd": "/repo"},
			"second": map[string]any{"type": "pane", "label": "b", "cwd": "/repo"},
		},
	}))
	for _, key := range []string{"tab", "panes", "warnings"} {
		if _, ok := got[key]; !ok {
			t.Errorf("result has no %q; §5.8 returns all three", key)
		}
	}

	var warnings []string
	if err := json.Unmarshal(got["warnings"], &warnings); err != nil {
		t.Fatalf("warnings is not an array of strings: %v", err)
	}
	if len(warnings) == 0 || warnings[0] != wsdomain.ApplyWarning {
		t.Errorf("warnings = %v, want them to state %q", warnings, wsdomain.ApplyWarning)
	}

	// The tree survived the trip rather than the call merely succeeding: a decoder that
	// dropped `root` would leave the module applying nothing and still answering.
	root := tree.lastCall.applyParams.Root
	if root == nil {
		t.Fatal("the root never reached the module")
	}
	if root.Type != wsdomain.NodeSplit || root.Direction != wsdomain.SplitRight {
		t.Errorf("root reached the module as %+v", root)
	}
	if root.First == nil || root.First.Label != "a" || root.First.CWD != "/repo" {
		t.Errorf("the first pane reached the module as %+v", root.First)
	}
	if !tree.lastCall.applyParams.Focus {
		t.Error("layout.apply defaulted focus to false; §5.8 writes `focus?: true`")
	}
}

// TestLayoutUpdatedCarriesTheLayout pins §6's row for the notification a tree change emits.
func TestLayoutUpdatedCarriesTheLayout(t *testing.T) {
	t.Parallel()

	method, params := toNotification(wsports.LayoutUpdated{
		Layout: wsdomain.Layout{WorkspaceID: "w1", TabID: "w1:t1", FocusedPaneID: "w1:p1"},
	})
	if method != "layout.updated" {
		t.Fatalf("method = %q, want layout.updated", method)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("payload is not an object: %v", err)
	}
	for _, key := range []string{"workspace_id", "tab_id", "focused_pane_id", "root"} {
		if _, ok := payload[key]; !ok {
			t.Errorf("payload has no %q; §6 types it as a Layout", key)
		}
	}
	if _, wrapped := payload["layout"]; wrapped {
		t.Error("layout.updated wraps its Layout in an envelope; §6 types the payload as the Layout")
	}
}

// TestLayoutsAreAdvertisedAsTheirOwnCapability: API Spec §2 lists `layouts` beside
// `workspaces`, so a client can find one without the other.
func TestLayoutsAreAdvertisedAsTheirOwnCapability(t *testing.T) {
	t.Parallel()

	s := testServerWithTree(t, &fakeTree{tree: sampleTree()})
	got := s.capabilities()

	var layouts, workspaces bool
	for _, capability := range got {
		switch capability {
		case "layouts":
			layouts = true
		case capabilityWorkspaces:
			workspaces = true
		case "layout":
			t.Errorf("capabilities = %v, which advertises %q; §2 names it layouts", got, capability)
		}
	}
	if !layouts || !workspaces {
		t.Errorf("capabilities = %v, want both layouts and workspaces", got)
	}
}

// TestLayoutMethodsRequireAnInteractiveClient: §2 keeps the `cli` client out of anything
// that arranges windows, and a layout is a window arrangement.
func TestLayoutMethodsRequireAnInteractiveClient(t *testing.T) {
	t.Parallel()

	s := testServerWithTree(t, &fakeTree{tree: sampleTree()})
	c := dial(t, s)
	if resp := c.hello(s.Token(), ClientCLI); resp.Error != nil {
		t.Fatalf("handshake: %+v", resp.Error)
	}
	for _, method := range []string{"layout.export", "layout.apply"} {
		resp := c.call(2, method, nil)
		if resp.Error == nil {
			t.Errorf("a cli client was allowed to call %s", method)
			continue
		}
		if resp.Error.Code != codeMethodNotFound {
			t.Errorf("%s gave code %d, want METHOD_NOT_FOUND", method, resp.Error.Code)
		}
	}
}

// TestEveryWorkspaceEventIsDispatched closes a hole that no other test can see.
//
// A bus event reaches a client only if two things line up: `toNotification` knows its wire
// form, and `dispatchedKinds` lists it so the dispatcher is subscribed. Forgetting the
// second is silent — the translation is there, the event never arrives, and the symptom is
// a client whose tree drifts out of step with the daemon over minutes. So the two lists are
// checked against each other rather than against a reader's memory.
func TestEveryWorkspaceEventIsDispatched(t *testing.T) {
	t.Parallel()

	sample := sampleTree()
	events := []struct {
		kind  bus.Kind
		event bus.Event
	}{
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

	subscribed := make(map[bus.Kind]bool, len(dispatchedKinds))
	for _, kind := range dispatchedKinds {
		subscribed[kind] = true
	}

	for _, e := range events {
		if !subscribed[e.kind] {
			t.Errorf("%s has a wire form but is not in dispatchedKinds, so no client ever sees it", e.kind)
		}
		method, _ := toNotification(e.event)
		if method == "" {
			t.Errorf("%s is dispatched but toNotification has no case for it, so it is dropped", e.kind)
			continue
		}
		if method != string(e.kind) {
			t.Errorf("%s translates to the method %q; the kinds are named after the §6 methods "+
				"so the two cannot disagree", e.kind, method)
		}
	}
}
