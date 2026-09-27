package tui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/tui/ports"
)

// fakeDaemon stands in for `umbrald`. The model is defined against ports.Daemon precisely
// so these tests need no socket, no database and no shell.
type fakeDaemon struct {
	mu       sync.Mutex
	sessions int
	blocks   map[string][]sessdomain.Block
	input    [][]byte
	resizes  []sessdomain.Size
	events   chan ports.Event
}

func newFakeDaemon() *fakeDaemon {
	return &fakeDaemon{
		blocks: map[string][]sessdomain.Block{},
		events: make(chan ports.Event, 64),
	}
}

func (f *fakeDaemon) CreateSession(_ context.Context, size sessdomain.Size) (sessdomain.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions++
	return sessdomain.Session{
		ID:    sessionID(f.sessions),
		Size:  size,
		State: sessdomain.StateAlive,
	}, nil
}

func (f *fakeDaemon) Subscribe(_ context.Context, _ string) ([]byte, uint64, error) {
	return []byte("\x1b[H\x1b[2J"), 7, nil
}

func (f *fakeDaemon) Input(_ context.Context, _ string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.input = append(f.input, append([]byte(nil), data...))
	return nil
}

func (f *fakeDaemon) Resize(_ context.Context, _ string, size sessdomain.Size) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resizes = append(f.resizes, size)
	return nil
}

func (f *fakeDaemon) Blocks(_ context.Context, sessionID string, _ int) ([]sessdomain.Block, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.blocks[sessionID], nil
}

func (f *fakeDaemon) Events() <-chan ports.Event { return f.events }

func (f *fakeDaemon) setBlocks(sessionID string, commands ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	blocks := make([]sessdomain.Block, 0, len(commands))
	for i, c := range commands {
		exit := 0
		blocks = append(blocks, sessdomain.Block{
			ID:        "blk_" + c,
			SessionID: sessionID,
			Command:   c,
			State:     sessdomain.BlockFinished,
			ExitCode:  &exit,
			StartedAt: time.UnixMilli(int64(1000 - i)).UTC(),
		})
	}
	f.blocks[sessionID] = blocks
}

func (f *fakeDaemon) inputs() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.input...)
}

func sessionID(n int) string { return "ses_fake" + string(rune('0'+n)) }

// fakeScreen is a renderer that records what it was given. The real one is covered by its
// own tests against libghostty; here what matters is that the model feeds it the right
// bytes at the right time.
type fakeScreen struct {
	size    sessdomain.Size
	written []byte
	closed  bool
	resets  int
}

func (s *fakeScreen) Write(p []byte) (int, error) {
	s.written = append(s.written, p...)
	return len(p), nil
}
func (s *fakeScreen) Resize(size sessdomain.Size) error { s.size = size; return nil }
func (s *fakeScreen) Lines() ([]string, error) {
	return strings.Split(strings.TrimSuffix(string(s.written), "\n"), "\n"), nil
}
func (s *fakeScreen) Cursor() (uint16, uint16, error) { return 0, 0, nil }

// Reset empties it, as the real one does by building a fresh terminal. Recording the count
// is what lets a test tell "the snapshot was replayed into an empty screen" from "it was
// written on top of the old one".
func (s *fakeScreen) Reset() error {
	s.written = nil
	s.resets++
	return nil
}
func (s *fakeScreen) Close() error { s.closed = true; return nil }

// harness drives the model the way Bubble Tea would: every command a step returns is run
// and its message fed back, until nothing is left. Doing it here rather than through a
// real program keeps these tests deterministic — no goroutines, no timing.
type harness struct {
	t      *testing.T
	m      *Model
	daemon *fakeDaemon
	screen *fakeScreen
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	d := newFakeDaemon()
	h := &harness{t: t, daemon: d}
	h.m = New(d, func(size sessdomain.Size) (ports.Screen, error) {
		h.screen = &fakeScreen{size: size}
		return h.screen, nil
	})
	return h
}

// step feeds one message and drains whatever it produced, skipping the command that waits
// on the daemon's event channel: that one blocks by design.
func (h *harness) step(msg tea.Msg) {
	h.t.Helper()
	_, cmd := h.m.Update(msg)
	h.drain(cmd, 0)
}

func (h *harness) drain(cmd tea.Cmd, depth int) {
	h.t.Helper()
	if cmd == nil || depth > 8 {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()

	select {
	case msg := <-done:
		if msg == nil {
			return
		}
		// tea.Batch hands back the commands rather than a message; running each one is
		// what the real program does, and skipping them here would quietly hide half of
		// what a keypress does.
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				h.drain(c, depth+1)
			}
			return
		}
		_, next := h.m.Update(msg)
		h.drain(next, depth+1)
	case <-time.After(50 * time.Millisecond):
		// waitForEvent blocks on the daemon's channel by design, and in these tests
		// nothing pushes to it. Timing out is how the harness steps past it.
	}
}

// openSession runs the model's startup: create a session, attach, and take the snapshot.
func (h *harness) openSession() {
	h.t.Helper()
	h.step(tea.WindowSizeMsg{Width: 100, Height: 30})
	h.drain(h.m.openTabCmd(), 0)
}

// TestTUIBlockNavigation_REQ_TUI_001 is the criterion the task names: jumping between
// blocks. REQ-TUI-001 asks the TUI for "tabs, splits, jumping between blocks and an agent
// panel"; the agent panel is T-F1-20's half, and the traceability matrix says so.
//
// The jump is asserted on the model's selection rather than on rendered text, because what
// the requirement is about is which block the user is on. Where that lands on screen is
// layout, and a test tied to it would fail on every cosmetic change.
func TestTUIBlockNavigation_REQ_TUI_001(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.openSession()
	h.daemon.setBlocks(sessionID(1), "go build ./...", "go test ./...", "git status")

	// ctrl+b opens the list and loads it.
	h.step(keyPress(t, "ctrl+b"))

	blk, ok := h.m.SelectedBlock()
	if !ok {
		t.Fatalf("no block is selected after opening the list; status = %q", h.m.Status())
	}
	if blk.Command != "go build ./..." {
		t.Errorf("selection starts on %q, want the newest block", blk.Command)
	}

	// ctrl+p walks towards older blocks; the list is newest first.
	h.step(keyPress(t, "ctrl+p"))
	if blk, _ := h.m.SelectedBlock(); blk.Command != "go test ./..." {
		t.Errorf("after one jump back the selection is %q, want the second-newest", blk.Command)
	}
	h.step(keyPress(t, "ctrl+p"))
	if blk, _ := h.m.SelectedBlock(); blk.Command != "git status" {
		t.Errorf("after two jumps back the selection is %q, want the oldest", blk.Command)
	}

	// Past the end it stays put and says so, rather than wrapping round to the newest,
	// which would look like the list had jumped on its own.
	h.step(keyPress(t, "ctrl+p"))
	if blk, _ := h.m.SelectedBlock(); blk.Command != "git status" {
		t.Errorf("jumping past the oldest moved the selection to %q", blk.Command)
	}
	if !strings.Contains(h.m.Status(), "oldest") {
		t.Errorf("status = %q, want it to say the end of the list was reached", h.m.Status())
	}

	// ctrl+g walks back towards the newest.
	h.step(keyPress(t, "ctrl+g"))
	if blk, _ := h.m.SelectedBlock(); blk.Command != "go test ./..." {
		t.Errorf("after jumping forward the selection is %q, want the second-newest", blk.Command)
	}
}

// TestBlockJumpOpensTheListWhenItIsClosed: jumping is what the list is for, so asking to
// jump while it is hidden should show it rather than moving a selection nobody can see.
func TestBlockJumpOpensTheListWhenItIsClosed(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.openSession()
	h.daemon.setBlocks(sessionID(1), "one", "two")

	// Load the list without opening it, then jump.
	h.drain(h.m.loadBlocksForFocus(), 0)
	if h.m.showBlocks {
		t.Fatal("the list is open before anything asked for it")
	}
	h.step(keyPress(t, "ctrl+p"))

	if !h.m.showBlocks {
		t.Error("jumping did not open the block list; the selection moved out of sight")
	}
}

// TestNewTabAndSwitching covers the tabs half of REQ-TUI-001.
func TestTabsAndSwitching_REQ_TUI_001(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.openSession()

	if got := len(h.m.sessionIDs()); got != 1 {
		t.Fatalf("%d sessions after startup, want 1", got)
	}
	h.step(keyPress(t, "ctrl+t"))
	if got := len(h.m.sessionIDs()); got != 2 {
		t.Fatalf("%d sessions after ctrl+t, want 2", got)
	}
	if h.m.CurrentTab() != 1 {
		t.Errorf("focus is on tab %d after opening one, want the new tab", h.m.CurrentTab())
	}
	h.step(keyPress(t, "ctrl+n"))
	if h.m.CurrentTab() != 0 {
		t.Errorf("ctrl+n went to tab %d, want it to wrap round to 0", h.m.CurrentTab())
	}
}

// TestSplitCreatesASecondPaneAndResizesBoth covers the splits half, and the consequence
// that makes it more than a layout change: both panes get narrower, and the daemon has to
// be told, or the shells keep line-wrapping for a width they no longer have.
func TestSplitResizesBothPanes_REQ_TUI_001(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.openSession()

	before := h.m.paneSize(1)
	h.step(keyPress(t, "ctrl+s"))

	if got := len(h.m.sessionIDs()); got != 2 {
		t.Fatalf("%d sessions after splitting, want 2", got)
	}
	after := h.m.paneSize(2)
	if after.Cols >= before.Cols {
		t.Errorf("a pane is %d columns after splitting and %d before; it should be narrower",
			after.Cols, before.Cols)
	}

	// A third split is refused in F0 rather than silently ignored.
	h.step(keyPress(t, "ctrl+s"))
	if got := len(h.m.sessionIDs()); got != 2 {
		t.Errorf("%d sessions after a third split, want 2", got)
	}
	if !strings.Contains(h.m.Status(), "T-F0-14") {
		t.Errorf("status = %q, want it to say where the pane tree arrives", h.m.Status())
	}
}

// TestSnapshotThenLiveOutput_REQ_TERM_004 is the reconnection contract seen from the
// client: the snapshot lands first, and a chunk the snapshot already contained is dropped
// rather than drawn twice.
func TestSnapshotThenLiveOutput_REQ_TERM_004(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.openSession()

	// openSession already applied the fake's snapshot, which is current as of seq 7.
	h.step(daemonEventMsg{event: ports.Event{
		Kind: ports.EventOutput, SessionID: sessionID(1), Seq: 5, Data: []byte("stale"),
	}})
	h.step(daemonEventMsg{event: ports.Event{
		Kind: ports.EventOutput, SessionID: sessionID(1), Seq: 8, Data: []byte("live"),
	}})

	written := string(h.screen.written)
	if strings.Contains(written, "stale") {
		t.Error("a chunk at or below the snapshot's seq was applied; the screen would show it twice")
	}
	if !strings.Contains(written, "live") {
		t.Errorf("the live chunk never reached the screen: %q", written)
	}
}

// TestDisconnectionIsShownRatherThanFrozen: when the daemon goes away the user has to be
// told, or a stale screen looks like a working one.
func TestDisconnectionIsShownRatherThanFrozen(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.openSession()
	h.step(daemonEventMsg{event: ports.Event{Kind: ports.EventDisconnected}})

	if !h.m.Disconnected() {
		t.Error("the model did not record the disconnection")
	}
	if !strings.Contains(h.m.View().Content, "disconnected") {
		t.Errorf("the view does not say the daemon is gone:\n%s", h.m.View().Content)
	}
}

// TestTypingReachesTheSession is the difference between a terminal and a viewer: a key the
// TUI did not claim belongs to the shell.
func TestTypingReachesTheSession(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.openSession()

	h.step(keyPress(t, "a"))
	h.step(keyPress(t, "enter"))
	// ctrl+c is not one of the TUI's shortcuts, so it has to reach the program: without
	// it a running command could never be interrupted.
	h.step(keyPress(t, "ctrl+c"))

	inputs := h.daemon.inputs()
	got := make([]string, 0, len(inputs))
	for _, in := range inputs {
		got = append(got, string(in))
	}
	want := []string{"a", "\r", "\x03"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("the session received %q, want %q", got, want)
	}
}

// keyPress builds the message Bubble Tea would deliver for a key.
func keyPress(t *testing.T, s string) tea.KeyPressMsg {
	t.Helper()
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "ctrl+b", "ctrl+p", "ctrl+g", "ctrl+t", "ctrl+n", "ctrl+s", "ctrl+o", "ctrl+c", "ctrl+q":
		return tea.KeyPressMsg{Code: rune(s[len(s)-1]), Mod: tea.ModCtrl}
	default:
		return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
	}
}

// TestDroppedSubscriptionReattaches covers what API Spec §6 asks of a client whose
// subscription the daemon dropped for falling behind: subscribe again and take the fresh
// snapshot. Treating it as a disconnection would strand a working connection on a frozen
// screen, and it is one word apart in the notification table from one that is fatal.
func TestDroppedSubscriptionReattaches(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.openSession()

	// Something is on the screen from before the drop.
	h.step(daemonEventMsg{event: ports.Event{
		Kind: ports.EventOutput, SessionID: sessionID(1), Seq: 9, Data: []byte("stale rows"),
	}})
	if !strings.Contains(string(h.screen.written), "stale rows") {
		t.Fatal("the setup did not put anything on the screen")
	}

	h.step(daemonEventMsg{event: ports.Event{
		Kind: ports.EventUnsubscribed, SessionID: sessionID(1),
	}})

	if h.m.Disconnected() {
		t.Error("a dropped subscription was reported as losing the daemon")
	}
	if h.screen.resets == 0 {
		t.Error("the screen was not emptied before the fresh snapshot; a snapshot only " +
			"reproduces the daemon's screen when it is replayed into an empty emulator")
	}
	// What is on the screen now is the snapshot and nothing else.
	if strings.Contains(string(h.screen.written), "stale rows") {
		t.Error("rows from before the drop survived the re-attach")
	}
	if len(h.screen.written) == 0 {
		t.Error("no fresh snapshot was applied; the pane would be blank")
	}
	if !strings.Contains(h.m.Status(), "re-attaching") {
		t.Errorf("status = %q, want it to say the client is re-attaching", h.m.Status())
	}
}

// TestOutputBeforeTheSnapshotIsHeld_REQ_TERM_004 covers the ordering the client cannot
// assume. The daemon writes the subscribe reply before any notification, but the two reach
// the model as independent messages from different goroutines, so the wire order is not
// the arrival order. Drawing a chunk first and then the snapshot would overwrite live
// output with an older screen — the client-side mirror of the gap T-F0-06 had in the
// daemon.
func TestOutputBeforeTheSnapshotIsHeld_REQ_TERM_004(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	// A window size and a session, but no subscription yet.
	h.step(tea.WindowSizeMsg{Width: 100, Height: 30})
	// The tab is installed without running the subscribe command it returns, which is
	// exactly the window this test is about: the pane exists and its snapshot is still
	// in flight.
	h.m.Update(tabOpenedMsg{session: sessdomain.Session{ID: sessionID(1)}})
	h.screen.written = nil

	// The chunk arrives first.
	h.step(daemonEventMsg{event: ports.Event{
		Kind: ports.EventOutput, SessionID: sessionID(1), Seq: 9, Data: []byte("live"),
	}})
	if strings.Contains(string(h.screen.written), "live") {
		t.Fatal("a chunk was drawn before the snapshot; the snapshot would then overwrite it")
	}

	// Then the snapshot, which is current as of seq 7.
	h.step(subscribedMsg{sessionID: sessionID(1), snapshot: []byte("SNAP"), seq: 7})

	written := string(h.screen.written)
	if !strings.HasPrefix(written, "SNAP") {
		t.Errorf("the screen starts with %q, want the snapshot first", written)
	}
	if !strings.Contains(written, "live") {
		t.Errorf("the held chunk was dropped rather than applied after the snapshot: %q", written)
	}
}

// TestHeldOutputBelowTheSnapshotSeqIsDiscarded: a chunk the snapshot already contains must
// not be replayed just because it was held.
func TestHeldOutputBelowTheSnapshotSeqIsDiscarded(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.step(tea.WindowSizeMsg{Width: 100, Height: 30})
	// The tab is installed without running the subscribe command it returns, which is
	// exactly the window this test is about: the pane exists and its snapshot is still
	// in flight.
	h.m.Update(tabOpenedMsg{session: sessdomain.Session{ID: sessionID(1)}})
	h.screen.written = nil

	h.step(daemonEventMsg{event: ports.Event{
		Kind: ports.EventOutput, SessionID: sessionID(1), Seq: 3, Data: []byte("already-on-screen"),
	}})
	h.step(subscribedMsg{sessionID: sessionID(1), snapshot: []byte("SNAP"), seq: 7})

	if strings.Contains(string(h.screen.written), "already-on-screen") {
		t.Error("a held chunk below the snapshot's seq was applied; it is on the screen twice")
	}
}
