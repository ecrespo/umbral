package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/ecrespo/umbral/internal/tui/ports"
)

// The fake daemon's agent half: what the panel created, sent, answered and cancelled.

type sentMessage struct {
	threadID    string
	text        string
	attachments []ports.Attachment
}

type approvalAnswer struct{ id, decision, scope string }

func (f *fakeDaemon) CreateThread(_ context.Context, cwd string) (string, error) {
	f.mu.Lock()
	hold := f.holdCreate
	f.threads = append(f.threads, cwd)
	f.mu.Unlock()
	if hold != nil {
		<-hold
	}
	return "thr_fake", nil
}

func (f *fakeDaemon) Send(_ context.Context, threadID, text string, attachments []ports.Attachment) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentMessage{threadID: threadID, text: text, attachments: attachments})
	return "trn_fake", nil
}

func (f *fakeDaemon) Respond(_ context.Context, approvalID, decision, scope string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, approvalAnswer{approvalID, decision, scope})
	return f.respondErr
}

func (f *fakeDaemon) Cancel(_ context.Context, threadID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels = append(f.cancels, threadID)
	return !f.idle, nil
}

func (f *fakeDaemon) Approvals(context.Context) ([]ports.Approval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ports.Approval(nil), f.pending...), nil
}

func (f *fakeDaemon) agentCalls() (threads []string, sent []sentMessage, answers []approvalAnswer, cancels []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.threads...), append([]sentMessage(nil), f.sent...),
		append([]approvalAnswer(nil), f.answers...), append([]string(nil), f.cancels...)
}

// typeText types each rune as its own keypress.
func (h *harness) typeText(s string) {
	h.t.Helper()
	for _, r := range s {
		if r == ' ' {
			h.step(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
			continue
		}
		h.step(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func ctrlSpace() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeySpace, Mod: tea.ModCtrl} }

func altKey(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Mod: tea.ModAlt} }

func ctrlKey(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl} }

func (h *harness) event(ev ports.Event) { h.step(daemonEventMsg{event: ev}) }

// TestModeToggle_REQ_TUI_002: ctrl+space moves input from the shell to the agent and back.
// What is typed in agent mode never reaches the session, and Enter sends it to a thread the
// panel creates in the pane's directory; back in shell mode, keys go to the shell again.
func TestModeToggle_REQ_TUI_002(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.openSession()

	if h.m.AgentMode() {
		t.Fatal("the TUI starts in agent mode")
	}
	h.step(ctrlSpace())
	if !h.m.AgentMode() || !h.m.AgentPanelOpen() {
		t.Fatal("ctrl+space did not switch input to the agent")
	}
	h.typeText("explain this")
	if got := h.daemon.inputs(); len(got) != 0 {
		t.Fatalf("agent-mode keys reached the shell: %q", got)
	}
	if h.m.AgentInput() != "explain this" {
		t.Fatalf("panel input = %q", h.m.AgentInput())
	}
	h.step(keyPress(t, "enter"))
	threads, sent, _, _ := h.daemon.agentCalls()
	if len(threads) != 1 || threads[0] != "/work" || len(sent) != 1 || sent[0].text != "explain this" || sent[0].threadID != "thr_fake" {
		t.Fatalf("threads %v, sent %+v", threads, sent)
	}
	if h.m.AgentInput() != "" {
		t.Errorf("the input was not cleared after sending: %q", h.m.AgentInput())
	}

	// While the turn runs a second message is refused rather than sent into a CONFLICT.
	h.typeText("and now")
	h.step(keyPress(t, "enter"))
	if _, sent, _, _ := h.daemon.agentCalls(); len(sent) != 1 || !strings.Contains(h.m.Status(), "running") {
		t.Fatalf("a send during a turn: %d sends, status %q", len(sent), h.m.Status())
	}
	// Once it has ended, the next message goes to the same thread.
	h.event(ports.Event{Kind: ports.EventTurnFinished, ThreadID: "thr_fake", StopReason: "end_turn"})
	h.step(keyPress(t, "enter"))
	if threads, sent, _, _ := h.daemon.agentCalls(); len(threads) != 1 || len(sent) != 2 {
		t.Fatalf("a second message made %d threads and %d sends", len(threads), len(sent))
	}

	h.step(ctrlSpace())
	if h.m.AgentMode() {
		t.Fatal("ctrl+space did not switch back to the shell")
	}
	h.typeText("ls")
	if got := h.daemon.inputs(); len(got) != 2 || string(got[0]) != "l" || string(got[1]) != "s" {
		t.Fatalf("shell-mode keys = %q", got)
	}
}

// TestAttachBlock_REQ_TUI_003: "attach to agent" on the selected block opens the panel in
// agent mode with `@block:<id>` preloaded, and sending it carries the block as an attachment.
func TestAttachBlock_REQ_TUI_003(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.openSession()
	h.daemon.setBlocks(sessionID(1), "test", "build")
	h.step(keyPress(t, "ctrl+b"))
	h.step(keyPress(t, "ctrl+p")) // the second-newest

	h.step(altKey('a'))
	if !h.m.AgentPanelOpen() || !h.m.AgentMode() {
		t.Fatal("attach did not open the agent panel in agent mode")
	}
	if got := h.m.AgentInput(); got != "@block:blk_build " {
		t.Fatalf("panel input = %q, want the selected block preloaded", got)
	}
	h.typeText("fix it")
	h.step(keyPress(t, "enter"))
	_, sent, _, _ := h.daemon.agentCalls()
	if len(sent) != 1 || len(sent[0].attachments) != 1 ||
		sent[0].attachments[0] != (ports.Attachment{Kind: "block", Ref: "blk_build"}) {
		t.Fatalf("sent %+v", sent)
	}

	// With nothing selected there is nothing to attach, and the TUI says so.
	h2 := newHarness(t)
	h2.openSession()
	h2.step(keyPress(t, "ctrl+b"))
	h2.step(altKey('a'))
	if h2.m.AgentPanelOpen() || !strings.Contains(h2.m.Status(), "block") {
		t.Fatalf("attach with no block: panel %v, status %q", h2.m.AgentPanelOpen(), h2.m.Status())
	}

	// With the block list closed alt+a is the shell's: ESC a, which zsh binds.
	h3 := newHarness(t)
	h3.openSession()
	h3.step(altKey('a'))
	if got := h3.daemon.inputs(); h3.m.AgentPanelOpen() || len(got) != 1 || string(got[0]) != "\x1ba" {
		t.Fatalf("alt+a without the block list: panel %v, shell got %q", h3.m.AgentPanelOpen(), got)
	}
}

// TestAttachmentsAreParsedFromTheInput: `@block:`, `@file:` and `@directory:` (or `@dir:`)
// tokens become the structured attachments thread.send takes (REQ-CTX-002); the text is
// sent as typed.
func TestAttachmentsAreParsedFromTheInput(t *testing.T) {
	t.Parallel()
	got := parseAttachments("look at @file:main.go and @dir:internal/ with @block:blk_1, please, and @directory:cmd")
	want := []ports.Attachment{
		{Kind: "file", Ref: "main.go"}, {Kind: "dir", Ref: "internal/"}, {Kind: "block", Ref: "blk_1"}, {Kind: "dir", Ref: "cmd"},
	}
	if len(got) != len(want) {
		t.Fatalf("parseAttachments = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseAttachments = %+v, want %+v", got, want)
		}
	}
	if got := parseAttachments("mail me @ home, or @unknown:x"); len(got) != 0 {
		t.Fatalf("parseAttachments = %+v, want none", got)
	}
}

// TestTUIAgentPanel_REQ_TUI_001: the agent panel shows the turn as it streams — the answer,
// the tool calls — and its pending approvals, which ctrl+y, ctrl+r, ctrl+l and ctrl+x answer
// one at a time, oldest first, with the diff on demand (ctrl+e).
func TestTUIAgentPanel_REQ_TUI_001(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.openSession()
	h.step(ctrlSpace())
	h.typeText("write the file")
	h.step(keyPress(t, "enter"))

	h.event(ports.Event{Kind: ports.EventThreadDelta, ThreadID: "thr_fake", Text: "Writing it"})
	h.event(ports.Event{Kind: ports.EventThreadDelta, ThreadID: "thr_fake", Text: " now."})
	h.event(ports.Event{Kind: ports.EventThreadDelta, ThreadID: "thr_other", Text: "NOT MINE"})
	h.event(ports.Event{Kind: ports.EventToolCall, ThreadID: "thr_fake", ToolCallID: "tc_1", Tool: "write_file", Status: "pending"})
	for i, a := range []ports.Approval{
		{ID: "apr_1", ThreadID: "thr_fake", Tool: "write_file", Risk: "WriteFS", Summary: "out.txt", Diff: "+hello\n"},
		{ID: "apr_2", ThreadID: "thr_fake", Tool: "run_command", Risk: "Exec", Summary: "go test ./..."},
		{ID: "apr_3", ThreadID: "thr_fake", Tool: "run_command", Risk: "Exec", Summary: "make lint"},
		{ID: "apr_4", ThreadID: "thr_fake", Tool: "run_command", Risk: "Exec", Summary: "rm -rf build"},
		{ID: "apr_5", ThreadID: "thr_fake", Tool: "run_command", Risk: "Exec", Summary: "go vet ./..."},
	} {
		h.event(ports.Event{Kind: ports.EventApprovalRequested, ThreadID: "thr_fake", Approval: a})
		if n := len(h.m.PendingApprovals()); n != i+1 {
			t.Fatalf("%d approvals pending after %d requests", n, i+1)
		}
	}
	// The same approval again — approval.list and the notification can both carry it — is
	// not a second one.
	h.event(ports.Event{Kind: ports.EventApprovalRequested, ThreadID: "thr_fake", Approval: ports.Approval{ID: "apr_1", ThreadID: "thr_fake"}})
	if n := len(h.m.PendingApprovals()); n != 5 {
		t.Fatalf("a repeated approval made %d pending", n)
	}

	view := h.m.View().Content
	for _, want := range []string{"Writing it now.", "write_file", "out.txt", "ctrl+y", "ctrl+x", "ctrl+l", "ctrl+r"} {
		if !strings.Contains(view, want) {
			t.Errorf("the panel lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "NOT MINE") {
		t.Error("another thread's delta reached the panel")
	}
	if strings.Contains(view, "+hello") {
		t.Error("the diff is shown before it was asked for")
	}
	h.step(ctrlKey('e'))
	if view := h.m.View().Content; !strings.Contains(view, "+hello") {
		t.Errorf("ctrl+e did not show the diff:\n%s", view)
	}

	h.step(ctrlKey('y'))
	h.step(ctrlKey('r'))
	h.step(ctrlKey('l'))
	h.step(ctrlKey('x'))
	_, _, answers, _ := h.daemon.agentCalls()
	want := []approvalAnswer{{"apr_1", "approve", "once"}, {"apr_2", "approve", "thread"}, {"apr_3", "approve", "always"}, {"apr_4", "deny", "once"}}
	if len(answers) != len(want) {
		t.Fatalf("answers %+v, want %+v", answers, want)
	}
	for i := range want {
		if answers[i] != want[i] {
			t.Fatalf("answers %+v, want %+v", answers, want)
		}
	}

	// ctrl+c in agent mode stops the turn, even while it waits on the user, and never
	// reaches the shell.
	h.step(keyPress(t, "ctrl+c"))
	if _, _, _, cancels := h.daemon.agentCalls(); len(cancels) != 1 || cancels[0] != "thr_fake" {
		t.Fatalf("cancels %v", cancels)
	}
	if got := h.daemon.inputs(); len(got) != 0 {
		t.Fatalf("ctrl+c reached the shell: %q", got)
	}

	// The cancel leaves the approval still waiting `expired` (API §5.25): the turn's end
	// takes it off the panel, and there is nothing left to answer.
	h.event(ports.Event{Kind: ports.EventTurnFinished, ThreadID: "thr_fake", StopReason: "cancelled"})
	if h.m.AgentRunning() || len(h.m.PendingApprovals()) != 0 {
		t.Fatalf("after the turn ended: running %v, %d pending", h.m.AgentRunning(), len(h.m.PendingApprovals()))
	}
	h.step(ctrlKey('x'))
	if _, _, answers, _ := h.daemon.agentCalls(); len(answers) != 4 {
		t.Fatalf("an answer after the turn ended was sent: %+v", answers)
	}
	view = h.m.View().Content
	if !strings.Contains(view, "cancelled") || strings.Contains(view, "waiting for you") {
		t.Errorf("the panel after the cancel:\n%s", view)
	}
}

// TestTypingAheadNeverAnswersAnApproval: an approval that arrives while the user is typing
// is not answered by the next letter. Only a control chord answers, so a word that happens
// to start with a, d or l is typed, never taken as a decision.
func TestTypingAheadNeverAnswersAnApproval(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.openSession()
	h.step(ctrlSpace())
	h.typeText("go")
	h.step(keyPress(t, "enter"))
	h.typeText("ag")
	h.event(ports.Event{
		Kind: ports.EventApprovalRequested, ThreadID: "thr_fake",
		Approval: ports.Approval{ID: "apr_1", ThreadID: "thr_fake", Tool: "write_file", Summary: "x"},
	})
	h.typeText("ain, delete it and log")
	if _, _, answers, _ := h.daemon.agentCalls(); len(answers) != 0 {
		t.Fatalf("typing answered the approval: %+v", answers)
	}
	if got := h.m.AgentInput(); got != "again, delete it and log" || len(h.m.PendingApprovals()) != 1 {
		t.Fatalf("input %q, %d pending", got, len(h.m.PendingApprovals()))
	}
}

// TestApprovalsOfEveryThreadAreShown: REQ-TUI-001's "pending approvals" are every thread's.
// The TUI is the client that answers them (API §2), so one left by `umb` or by an earlier
// run of the TUI is listed at start, a new one of any thread is shown, and a turn's end
// takes its thread's approvals away.
func TestApprovalsOfEveryThreadAreShown(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.daemon.pending = []ports.Approval{{ID: "apr_old", ThreadID: "thr_cli", Tool: "run_command", Risk: "Exec", Summary: "make"}}
	h.step(tea.WindowSizeMsg{Width: 100, Height: 30})
	h.drain(h.m.Init(), 0)
	if got := h.m.PendingApprovals(); len(got) != 1 || got[0].ID != "apr_old" {
		t.Fatalf("pending at start: %+v", got)
	}
	if !h.m.AgentPanelOpen() {
		t.Fatal("a pending approval at start did not open the panel")
	}
	h.event(ports.Event{
		Kind: ports.EventApprovalRequested, ThreadID: "thr_two",
		Approval: ports.Approval{ID: "apr_two", ThreadID: "thr_two", Tool: "write_file", Summary: "b.txt"},
	})
	if view := h.m.View().Content; !strings.Contains(view, "thr_cli") {
		t.Errorf("an approval of another thread does not name it:\n%s", view)
	}
	h.step(ctrlSpace())
	h.step(ctrlKey('y'))
	if _, _, answers, _ := h.daemon.agentCalls(); len(answers) != 1 || answers[0].id != "apr_old" {
		t.Fatalf("answers %+v", answers)
	}
	// A list that answers late, with the approval just answered, does not bring it back.
	h.step(approvalsLoadedMsg{approvals: []ports.Approval{{ID: "apr_old", ThreadID: "thr_cli"}}})
	if got := h.m.PendingApprovals(); len(got) != 1 || got[0].ID != "apr_two" {
		t.Fatalf("pending after a late list: %+v", got)
	}
	h.event(ports.Event{Kind: ports.EventTurnFinished, ThreadID: "thr_two", StopReason: "cancelled"})
	if n := len(h.m.PendingApprovals()); n != 0 {
		t.Fatalf("thr_two's approval outlived its turn: %d pending", n)
	}
}

// TestTheThreadStartsWhereThePanesLastCommandRan: the thread works in the directory of the
// focused pane's last command, which a `cd` moved; the session's own starting directory is
// only the fallback for a pane that has run nothing.
func TestTheThreadStartsWhereThePanesLastCommandRan(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.openSession()
	h.event(ports.Event{Kind: ports.EventBlockClosed, SessionID: sessionID(1), CWD: "/work/scratch"})
	h.event(ports.Event{Kind: ports.EventBlockClosed, SessionID: "ses_elsewhere", CWD: "/elsewhere"})
	h.step(ctrlSpace())
	h.typeText("hi")
	h.step(keyPress(t, "enter"))
	if threads, _, _, _ := h.daemon.agentCalls(); len(threads) != 1 || threads[0] != "/work/scratch" {
		t.Fatalf("thread created in %v", threads)
	}
}

// TestTheWaitingStatusHolds: in shell mode, with an approval pending, the status line says
// the agent is waiting, however many keys the shell has had since.
func TestTheWaitingStatusHolds(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.openSession()
	h.step(ctrlSpace())
	h.event(ports.Event{
		Kind: ports.EventApprovalRequested, ThreadID: "thr_x",
		Approval: ports.Approval{ID: "apr_1", ThreadID: "thr_x", Tool: "write_file", Summary: "x"},
	})
	h.step(ctrlSpace())
	h.typeText("ls")
	if got := h.m.View().Content; !strings.Contains(got, "waiting for an approval") {
		t.Fatalf("the status line does not say the agent waits:\n%s", got)
	}
}

// TestThePanelFitsTheWindow: with the block list and the panel open and a diff longer than
// the window, no row is wider than the window, the frame is no taller, and the keys that
// answer and the input line are still on screen.
func TestThePanelFitsTheWindow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.openSession()
	h.step(keyPress(t, "ctrl+b"))
	h.step(ctrlSpace())
	var diff strings.Builder
	for i := range 80 {
		diff.WriteString(strings.Repeat("+", 3) + strings.Repeat("x", 120) + string(rune('a'+i%26)) + "\n")
	}
	h.event(ports.Event{
		Kind: ports.EventApprovalRequested, ThreadID: "thr_x",
		Approval: ports.Approval{ID: "apr_1", ThreadID: "thr_x", Tool: "write_file", Summary: "x", Diff: diff.String()},
	})
	h.step(ctrlKey('e'))
	// The pane, the block list and the panel, each column after the first behind a
	// one-column separator, add up to the window.
	if got := int(h.screen.size.Cols) + 1 + blockListWidth + 1 + h.m.agentPanelWidth(); got != 100 {
		t.Errorf("the columns add up to %d in a 100-column window", got)
	}
	view := strings.TrimSuffix(h.m.View().Content, "\n")
	rows := strings.Split(view, "\n")
	if len(rows) > 30 {
		t.Errorf("the frame is %d rows in a 30-row window", len(rows))
	}
	for i, row := range rows {
		if n := len([]rune(row)); n > 100 {
			t.Errorf("row %d is %d columns in a 100-column window: %q", i, n, row)
		}
	}
	for _, want := range []string{"ctrl+y", "> ", "more lines"} {
		if !strings.Contains(view, want) {
			t.Errorf("the frame lacks %q:\n%s", want, view)
		}
	}
}

// TestACancelThatStoppedNothingClearsTheTurn: when the turn's end never reached the panel,
// ctrl+c gets `stopped_at: null` from the daemon — nothing was running — and the panel
// stops waiting for an end that will not come.
func TestACancelThatStoppedNothingClearsTheTurn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.openSession()
	h.step(ctrlSpace())
	h.typeText("go")
	h.step(keyPress(t, "enter"))
	h.daemon.mu.Lock()
	h.daemon.idle = true
	h.daemon.mu.Unlock()
	h.step(keyPress(t, "ctrl+c"))
	if h.m.AgentRunning() {
		t.Fatal("the panel still waits for a turn the daemon says is not running")
	}
}

// TestACancelBeforeTheThreadExistsDropsTheMessage: ctrl+c while thread.create is still out
// takes back the message waiting on it, so it is not sent once the thread arrives.
func TestACancelBeforeTheThreadExistsDropsTheMessage(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	h.daemon.holdCreate = hold
	h.openSession()
	h.step(ctrlSpace())
	h.typeText("go")
	h.step(keyPress(t, "enter"))
	h.step(keyPress(t, "ctrl+c"))
	if h.m.AgentRunning() {
		t.Fatal("still running after the cancel")
	}
	h.step(threadCreatedMsg{threadID: "thr_fake"})
	if _, sent, _, _ := h.daemon.agentCalls(); len(sent) != 0 {
		t.Fatalf("the cancelled message was sent: %+v", sent)
	}
}

// TestToolCallsAreRowsOfTheirOwn: two calls of the same tool are two rows, each updated in
// place by its own id.
func TestToolCallsAreRowsOfTheirOwn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.openSession()
	h.step(ctrlSpace())
	h.typeText("go")
	h.step(keyPress(t, "enter"))
	for _, ev := range []ports.Event{
		{ToolCallID: "tc_1", Status: "running"},
		{ToolCallID: "tc_1", Status: "ok"},
		{ToolCallID: "tc_2", Status: "running"},
		{ToolCallID: "tc_2", Status: "failed"},
	} {
		ev.Kind, ev.ThreadID, ev.Tool = ports.EventToolCall, "thr_fake", "read_file"
		h.event(ev)
	}
	view := h.m.View().Content
	if strings.Count(view, "read_file") != 2 || !strings.Contains(view, "read_file: ok") || !strings.Contains(view, "read_file: failed") {
		t.Fatalf("tool rows:\n%s", view)
	}
}

// TestModelTextCannotDriveTheTerminal: what the model and a tool's target say is drawn, not
// obeyed: escape and other control characters are dropped, and so are the invisible format
// characters — bidi overrides, zero-width spaces — that would show a command in an order
// other than the one it runs in; tabs become spaces.
func TestModelTextCannotDriveTheTerminal(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.openSession()
	h.step(ctrlSpace())
	h.typeText("go")
	h.step(keyPress(t, "enter"))
	h.event(ports.Event{Kind: ports.EventThreadDelta, ThreadID: "thr_fake", Text: "\x1b]52;c;ZXZpbA==\x07\x1b[2Jred\tcell\rX\u009b31m"})
	h.event(ports.Event{
		Kind: ports.EventApprovalRequested, ThreadID: "thr_fake",
		Approval: ports.Approval{ID: "apr_1", ThreadID: "thr_fake", Tool: "write_file", Summary: "a\x1b[8mb\u202egnp.\u200bexe\u2066", Diff: "+\tx\x1b[0m\n"},
	})
	h.step(ctrlKey('e'))
	view := h.m.View().Content
	// The fake pane draws its own `\x1b[H\x1b[2J`; what is checked is what the model sent.
	for _, bad := range []string{"\u202e", "\u200b", "\u2066", "\x1b]", "ZXZpbA", "\x07", "\x1b[2Jred", "\r", "\t", "\u009b", "\x1b[8m", "\x1b[0m"} {
		if strings.Contains(view, bad) {
			t.Errorf("the panel drew %q:\n%q", bad, view)
		}
	}
	if !strings.Contains(view, "red     cell") {
		t.Errorf("the tab was not expanded:\n%s", view)
	}
}

// TestPasteGoesToTheAgentInput: text pasted in agent mode lands in the input, newlines
// flattened to spaces and control sequences dropped, rather than being lost.
func TestPasteGoesToTheAgentInput(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.openSession()
	h.step(ctrlSpace())
	h.step(tea.PasteMsg{Content: "why does\nthis fail?\x1b[A"})
	if got := h.m.AgentInput(); got != "why does this fail?" {
		t.Fatalf("input after paste = %q", got)
	}
}

// TestThePanelNarrowsThePanes: opening the panel takes width from the panes, and the
// daemon is told, so the shell's idea of its terminal matches what is drawn.
func TestThePanelNarrowsThePanes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.openSession()
	before := h.screen.size.Cols
	h.step(ctrlSpace())
	if after := h.screen.size.Cols; after >= before {
		t.Fatalf("the pane is %d columns with the panel open, %d without", after, before)
	}
	h.daemon.mu.Lock()
	last := h.daemon.resizes[len(h.daemon.resizes)-1]
	h.daemon.mu.Unlock()
	if last.Cols != h.screen.size.Cols {
		t.Fatalf("the daemon was told %d columns, the pane has %d", last.Cols, h.screen.size.Cols)
	}
}

// TestALongApprovalStaysReadable: an approval whose summary is longer than the panel keeps
// its head — the tool, its risk and the start of what it does — and the keys; what does
// not fit is counted, never scrolled off the top.
func TestALongApprovalStaysReadable(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.openSession()
	h.step(ctrlSpace())
	h.event(ports.Event{Kind: ports.EventApprovalRequested, ThreadID: "thr_x", Approval: ports.Approval{
		ID: "apr_1", ThreadID: "thr_x", Tool: "run_command", Risk: "Exec", Summary: "echo START " + strings.Repeat("word ", 600),
	}})
	view := strings.TrimSuffix(h.m.View().Content, "\n")
	if rows := strings.Count(view, "\n") + 1; rows > 30 {
		t.Errorf("the frame is %d rows in a 30-row window", rows)
	}
	for _, want := range []string{"approve run_command (Exec): echo START", "more lines", "ctrl+y", "> "} {
		if !strings.Contains(view, want) {
			t.Errorf("the frame lacks %q:\n%s", want, view)
		}
	}
}

// TestAFailedAnswerKeepsTheApproval: an answer the daemon never took — a timeout, a lost
// connection — leaves the approval pending to be answered again; one the daemon says was
// already decided or has expired is gone.
func TestAFailedAnswerKeepsTheApproval(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.openSession()
	h.step(ctrlSpace())
	h.event(ports.Event{
		Kind: ports.EventApprovalRequested, ThreadID: "thr_x",
		Approval: ports.Approval{ID: "apr_1", ThreadID: "thr_x", Tool: "write_file", Summary: "x"},
	})
	h.daemon.mu.Lock()
	h.daemon.respondErr = errors.New("timed out")
	h.daemon.mu.Unlock()
	h.step(ctrlKey('y'))
	if got := h.m.PendingApprovals(); len(got) != 1 || got[0].ID != "apr_1" {
		t.Fatalf("after a failed answer: %+v", got)
	}
	h.daemon.mu.Lock()
	h.daemon.respondErr = fmt.Errorf("respond: %w", ports.ErrApprovalGone)
	h.daemon.mu.Unlock()
	h.step(ctrlKey('y'))
	if got := h.m.PendingApprovals(); len(got) != 0 {
		t.Fatalf("an expired approval stayed: %+v", got)
	}
}

// TestACancelThatStoppedNothingDropsItsApprovals: a cancel of a thread with no turn running
// means its approvals are no longer waiting on anything, so they leave the panel too.
func TestACancelThatStoppedNothingDropsItsApprovals(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.openSession()
	h.step(ctrlSpace())
	h.event(ports.Event{
		Kind: ports.EventApprovalRequested, ThreadID: "thr_other",
		Approval: ports.Approval{ID: "apr_1", ThreadID: "thr_other", Tool: "write_file", Summary: "x"},
	})
	h.daemon.mu.Lock()
	h.daemon.idle = true
	h.daemon.mu.Unlock()
	h.step(keyPress(t, "ctrl+c"))
	if _, _, _, cancels := h.daemon.agentCalls(); len(cancels) != 1 || cancels[0] != "thr_other" {
		t.Fatalf("cancels %v", cancels)
	}
	if got := h.m.PendingApprovals(); len(got) != 0 {
		t.Fatalf("the stale approval stayed: %+v", got)
	}
}
