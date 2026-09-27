package api

// Result shapes for the methods of API Spec §5.
//
// These were `map[string]any` literals until T-F0-17. The wire bytes are the same; what
// changes is that the shape now has a Go type, which is what lets `api.schema` generate the
// response half of the protocol document by reflection instead of describing it by hand
// (REQ-API-004). A map can be marshalled but not read: nothing can ask it what members it
// has before one is built.
//
// The second reason is the one that shows up in review. A map's keys are strings checked by
// nobody; the `keyWorkspace`-style constants existed to make typos less likely, which is a
// weaker version of what a field name does for free.

// emptyResult is `{}`, the acknowledgement a method returns when there is nothing to say
// beyond "it happened". It is not `nil`: §5 types every result as an object, and a client
// decoding into a struct should not have to special-case a null.
type emptyResult struct{}

// closedResult is what the three `*.close` methods return.
type closedResult struct {
	Closed bool `json:"closed"`
}

type sessionListResult struct {
	Items []Session `json:"items"`
}

// cursorPosition is the cursor inside a screen snapshot (§5.10).
type cursorPosition struct {
	X uint16 `json:"x"`
	Y uint16 `json:"y"`
}

// screenSnapshot is the replayable screen `session.subscribe` hands a client before the
// first live chunk. `format` is `"vt"`: the bytes are a VT stream to be replayed into an
// empty emulator, not a rendered image.
type screenSnapshot struct {
	Format  string         `json:"format"`
	DataB64 string         `json:"data_b64"`
	Cursor  cursorPosition `json:"cursor"`
}

type sessionSubscribeResult struct {
	Snapshot screenSnapshot `json:"snapshot"`
	// Seq is the session's own counter: the daemon emits `session.output` starting at
	// Seq+1 (§5.11). It is not the envelope counter of §6.
	Seq uint64 `json:"seq"`
}

type workspaceCreateResult struct {
	Workspace Workspace `json:"workspace"`
	Tab       Tab       `json:"tab"`
	RootPane  Pane      `json:"root_pane"`
}

type workspaceListResult struct {
	Items []Workspace `json:"items"`
}

type workspaceResult struct {
	Workspace Workspace `json:"workspace"`
}

type tabCreateResult struct {
	Tab      Tab  `json:"tab"`
	RootPane Pane `json:"root_pane"`
}

type tabListResult struct {
	Items []Tab `json:"items"`
}

type tabResult struct {
	Tab Tab `json:"tab"`
}

type paneSplitResult struct {
	Pane   Pane   `json:"pane"`
	Layout Layout `json:"layout"`
}

type paneListResult struct {
	Items []Pane `json:"items"`
}

type paneResult struct {
	Pane Pane `json:"pane"`
}

// paneMoveResult carries both names the pane has answered to. `previous_pane_id` is the
// alias REQ-WS-007 keeps resolvable for the life of the terminal, so a client holding the
// old identifier learns its new one from the reply rather than from a lookup.
type paneMoveResult struct {
	Pane                Pane   `json:"pane"`
	PreviousPaneID      string `json:"previous_pane_id"`
	PreviousWorkspaceID string `json:"previous_workspace_id"`
	Layout              Layout `json:"layout"`
}

// layoutApplyResult reports what `layout.apply` built. `warnings` is where
// `ApplyWarning` lands: what a rebuilt tab cannot reproduce.
type layoutApplyResult struct {
	Tab      Tab      `json:"tab"`
	Panes    []Pane   `json:"panes"`
	Warnings []string `json:"warnings"`
}

// snapshotResult is §5.3. `threads` is present and empty until F1: a client iterating the
// result should not have to branch on which build of the daemon it reached.
type snapshotResult struct {
	Seq        uint64      `json:"seq"`
	Focused    Focus       `json:"focused"`
	Workspaces []Workspace `json:"workspaces"`
	Tabs       []Tab       `json:"tabs"`
	Panes      []Pane      `json:"panes"`
	Layouts    []Layout    `json:"layouts"`
	Threads    []any       `json:"threads"`
}

// Notification payloads of API Spec §6.
//
// The ones §6 types as an object it already names — `block.started` carries a `Block`,
// `workspace.created` a `Workspace` — reuse that type and are absent here. These are the
// rest: the payloads that exist only as a notification's parameters.

type sessionExitedPayload struct {
	SessionID string `json:"session_id"`
	ExitCode  int    `json:"exit_code"`
	ExitedAt  int64  `json:"exited_at"`
}

type sessionResizedPayload struct {
	SessionID string `json:"session_id"`
	Cols      uint16 `json:"cols"`
	Rows      uint16 `json:"rows"`
}

type sessionInputOwnerPayload struct {
	SessionID  string `json:"session_id"`
	InputOwner string `json:"input_owner"`
}

type sessionIntegrationPayload struct {
	SessionID   string `json:"session_id"`
	Integration string `json:"integration"`
}

// sessionOutputPayload is the hot one. `seq` here is the session's own counter, not the
// envelope's (§5.11 against §6).
type sessionOutputPayload struct {
	SessionID string `json:"session_id"`
	Seq       uint64 `json:"seq"`
	DataB64   string `json:"data_b64"`
}

type sessionUnsubscribedPayload struct {
	SessionID string `json:"session_id"`
	Reason    string `json:"reason"`
}

type blockUpdatedPayload struct {
	BlockID string `json:"block_id"`
	State   string `json:"state"`
}

// paneMovedPayload is REQ-WS-007's: it names both identifiers so a client can follow the
// terminal across the move instead of seeing an unrelated pane appear.
type paneMovedPayload struct {
	Pane                Pane   `json:"pane"`
	PreviousPaneID      string `json:"previous_pane_id"`
	PreviousWorkspaceID string `json:"previous_workspace_id"`
	Layout              Layout `json:"layout"`
}
