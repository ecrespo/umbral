package integration_test

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/sessions"
	"github.com/ecrespo/umbral/internal/sessions/domain"
)

func keyText(s string) domain.Key { return domain.Key{Text: s} }

var (
	keyEnter = domain.Key{Name: "enter"}
	keyCtrlC = domain.Key{Name: "c", Ctrl: true}
	keyCtrlD = domain.Key{Name: "d", Ctrl: true}
)

// send sends keys in the state the terminal is in now, as a decided call would.
func send(t *testing.T, h *harness, ctx context.Context, threadID, cwd string, keys ...domain.Key) domain.Plan {
	t.Helper()
	plan, err := h.SendKeys(ctx, threadID, cwd, keys, h.InteractState(threadID, cwd))
	if err != nil {
		t.Fatalf("send %+v: %v", keys, err)
	}
	return plan
}

func waitKeys(t *testing.T, h *harness, ctx context.Context, threadID, cwd string, cond domain.WaitCondition) domain.WaitOutcome {
	t.Helper()
	if cond.Timeout == 0 {
		cond.Timeout = 20 * time.Second
	}
	out, err := h.WaitFor(ctx, threadID, cwd, cond)
	if err != nil {
		t.Fatalf("wait_for %+v: %v", cond, err)
	}
	return out
}

// TestTerminalInteractDrivesAProgram_REQ_FTU_001: terminal_interact drives the thread's PTY. A
// line submitted at the prompt starts a block with origin agent and the thread's id; keys then
// reach the program in the foreground; read_screen shows what it printed; wait_for ends
// matched, idle, exited or timeout. While the program runs, and while text typed at the prompt
// is not entered, run_command refuses (delta `2026-10-terminal-interact`, decisions 1 and 2).
func TestTerminalInteractDrivesAProgram_REQ_FTU_001(t *testing.T) {
	h := newHarness(t)
	cwd := t.TempDir()
	threadID := thread(t, h, cwd)
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()

	if state := h.InteractState(threadID, cwd); !state.AtPrompt || state.Line != "" || state.TakenOver {
		t.Fatalf("a thread with no PTY yet: %+v, want an empty prompt", state)
	}
	if plan := send(t, h, ctx, threadID, cwd, keyText("cat"), keyEnter); !plan.Submit || plan.Line != "cat" {
		t.Fatalf("submit = %+v", plan)
	}
	waitKeys(t, h, ctx, threadID, cwd, domain.WaitCondition{Idle: 300 * time.Millisecond})
	if state := h.InteractState(threadID, cwd); state.AtPrompt {
		t.Fatal("cat is in the foreground and the state says the prompt")
	}

	if _, err := h.RunForThread(ctx, threadID, cwd, "true"); !errors.Is(err, sessions.ErrProgramRunning) {
		t.Errorf("run_command while cat runs: %v, want ErrProgramRunning", err)
	}

	// The program's line is kept across calls until its enter (REQ-FTU-003).
	send(t, h, ctx, threadID, cwd, keyText("rm -"))
	if state := h.InteractState(threadID, cwd); state.Program != "rm -" {
		t.Errorf("the program's line is not kept: %+v", state)
	}
	send(t, h, ctx, threadID, cwd, keyText("x"), keyEnter)
	if state := h.InteractState(threadID, cwd); state.Program != "" {
		t.Errorf("the program's line outlived its enter: %+v", state)
	}
	waitKeys(t, h, ctx, threadID, cwd, domain.WaitCondition{Regex: regexp.MustCompile(`rm -x\s+rm -x`)})

	send(t, h, ctx, threadID, cwd, keyText("hello-umbral"), keyEnter)
	out := waitKeys(t, h, ctx, threadID, cwd, domain.WaitCondition{Regex: regexp.MustCompile(`hello-umbral\s+hello-umbral`)})
	if out.Reason != domain.WaitMatched || !strings.Contains(out.Screen.Text, "hello-umbral") {
		t.Errorf("wait_for regex = %q, screen %q", out.Reason, out.Screen.Text)
	}
	screen, err := h.ReadScreen(ctx, threadID, cwd)
	if err != nil || !strings.Contains(screen.Text, "hello-umbral") || screen.AtPrompt || screen.Size.Cols != 120 {
		t.Errorf("read_screen = %+v, %v", screen, err)
	}
	if out := waitKeys(t, h, ctx, threadID, cwd, domain.WaitCondition{Idle: 200 * time.Millisecond}); out.Reason != domain.WaitIdle {
		t.Errorf("wait_for idle = %q", out.Reason)
	}
	start := time.Now()
	if out := waitKeys(t, h, ctx, threadID, cwd, domain.WaitCondition{Exit: true, Timeout: 300 * time.Millisecond}); out.Reason != domain.WaitTimeout {
		t.Errorf("wait_for exit while cat runs = %q, want timeout", out.Reason)
	}
	if took := time.Since(start); took < 300*time.Millisecond || took > 3*time.Second {
		t.Errorf("the timeout took %v", took)
	}

	send(t, h, ctx, threadID, cwd, keyCtrlD)
	if out := waitKeys(t, h, ctx, threadID, cwd, domain.WaitCondition{Exit: true}); out.Reason != domain.WaitExited || !out.Screen.AtPrompt {
		t.Errorf("wait_for exit = %q, at prompt %v", out.Reason, out.Screen.AtPrompt)
	}

	var origin, rowThread string
	if err := h.store.DB().QueryRowContext(t.Context(),
		`SELECT origin, thread_id FROM blocks WHERE command = 'cat' AND thread_id = ?`, threadID).Scan(&origin, &rowThread); err != nil {
		t.Fatalf("the submitted line's block: %v", err)
	}
	if origin != "agent" || rowThread != threadID {
		t.Errorf("block origin %s thread %s", origin, rowThread)
	}

	// Text typed at the prompt and not entered keeps run_command off the line.
	if plan := send(t, h, ctx, threadID, cwd, keyText("echo pending")); plan.Submit || plan.Line != "echo pending" {
		t.Fatalf("typing = %+v", plan)
	}
	if state := h.InteractState(threadID, cwd); state.Line != "echo pending" {
		t.Errorf("the line is not kept: %+v", state)
	}
	// The call returns once the shell has read the text, so a ctrl+c sent next finds none of it
	// still queued.
	if screen, err := h.ReadScreen(ctx, threadID, cwd); err != nil || !strings.HasSuffix(screen.Text, "echo pending") {
		t.Errorf("the shell had not read the text when the call returned: %q, %v", screen.Text, err)
	}
	if _, err := h.RunForThread(ctx, threadID, cwd, "true"); !errors.Is(err, sessions.ErrPromptHasText) {
		t.Errorf("run_command over typed text: %v, want ErrPromptHasText", err)
	}
	send(t, h, ctx, threadID, cwd, keyCtrlC)
	// The call returns once the shell has answered the interrupt, so what is typed next cannot
	// reach it as typeahead of the line the interrupt dropped.
	if screen, err := h.ReadScreen(ctx, threadID, cwd); err != nil || strings.HasSuffix(screen.Text, "echo pending") {
		t.Errorf("the shell had not answered the ctrl+c when the call returned: %q, %v", screen.Text, err)
	}
	if run, err := h.RunForThread(ctx, threadID, cwd, "printf 'clean\\n'"); err != nil || run.Block.Command != "printf 'clean\\n'" {
		t.Errorf("run_command after ctrl+c: %+v, %v", run.Block, err)
	}
}

// TestTakeOverLocksTheAgentOut_REQ_FTU_002: a take-over moves the thread's PTY to the human at
// once; the agent's keys, reads and commands are then refused taken_over and write nothing,
// while the human types; a hand-back gives the input back and, at the prompt, empties the line
// the user left (decisions 6 and 7).
func TestTakeOverLocksTheAgentOut_REQ_FTU_002(t *testing.T) {
	h := newHarness(t)
	cwd := t.TempDir()
	threadID := thread(t, h, cwd)
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()

	if _, err := h.TakeOver(ctx, threadID, "conn_a"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("take over a thread with no PTY: %v, want ErrNotFound", err)
	}
	run, err := h.RunForThread(ctx, threadID, cwd, "true")
	if err != nil {
		t.Fatal(err)
	}
	// A key at the prompt waits for the shell's answer, at least 50 ms, without the input lock:
	// a take-over made meanwhile is not held behind it.
	sent := make(chan error, 1)
	go func() {
		_, err := h.SendKeys(ctx, threadID, cwd, []domain.Key{{Name: "backspace"}}, h.InteractState(threadID, cwd))
		sent <- err
	}()
	time.Sleep(5 * time.Millisecond)
	start := time.Now()
	sessionID, err := h.TakeOver(ctx, threadID, "conn_a")
	if took := time.Since(start); took > 30*time.Millisecond {
		t.Errorf("the take-over waited %v behind a key at the prompt", took)
	}
	// On a loaded machine the key may not have reached the PTY before the take-over; it is then
	// refused as the user's terminal, which is right too. Only another failure is a defect.
	if err := <-sent; err != nil && !errors.Is(err, domain.ErrTakenOver) {
		t.Errorf("backspace at the prompt: %v", err)
	}
	if err != nil || sessionID != run.Block.SessionID {
		t.Fatalf("take over = %q, %v; want %s", sessionID, err, run.Block.SessionID)
	}
	if got, _ := h.Get(ctx, sessionID); got.InputOwner != domain.InputOwnerHuman {
		t.Errorf("input owner after take-over = %s", got.InputOwner)
	}

	state := h.InteractState(threadID, cwd)
	if !state.TakenOver {
		t.Errorf("state = %+v, want taken over", state)
	}
	if _, err := h.SendKeys(ctx, threadID, cwd, []domain.Key{keyText("echo agent-typed"), keyEnter}, state); !errors.Is(err, domain.ErrTakenOver) {
		t.Errorf("send_keys while taken over: %v", err)
	}
	if _, err := h.ReadScreen(ctx, threadID, cwd); !errors.Is(err, domain.ErrTakenOver) {
		t.Errorf("read_screen while taken over: %v", err)
	}
	if _, err := h.WaitFor(ctx, threadID, cwd, domain.WaitCondition{Exit: true, Timeout: time.Second}); !errors.Is(err, domain.ErrTakenOver) {
		t.Errorf("wait_for while taken over: %v", err)
	}
	if _, err := h.RunForThread(ctx, threadID, cwd, "echo agent-ran"); !errors.Is(err, domain.ErrTakenOver) {
		t.Errorf("run_command while taken over: %v", err)
	}
	other := t.TempDir()
	if _, err := h.RunForThread(ctx, threadID, other, "true"); !errors.Is(err, domain.ErrTakenOver) {
		t.Errorf("a turn in another cwd replaced the user's terminal: %v", err)
	}
	if err := h.Input(ctx, sessionID, []byte("x"), domain.InputOwnerAgent); !isInputLocked(err) {
		t.Errorf("the agent's raw input: %v, want INPUT_LOCKED", err)
	}
	if err := h.Input(ctx, sessionID, []byte("echo human-typed\r"), domain.InputOwnerHuman); err != nil {
		t.Fatalf("the human's input: %v", err)
	}
	if !waitForScreen(t, h, sessionID, "human-typed") {
		t.Fatal("the human's command did not run")
	}
	if snap, _ := h.Snapshot(ctx, sessionID); containsBytes(snap.Data, "agent-typed") || containsBytes(snap.Data, "agent-ran") {
		t.Error("the agent's keys reached the PTY while the user held it")
	}

	// The user leaves text on the line and hands back.
	if err := h.Input(ctx, sessionID, []byte("echo left-by-user"), domain.InputOwnerHuman); err != nil {
		t.Fatal(err)
	}
	if !waitForScreen(t, h, sessionID, "left-by-user") {
		t.Fatal("the typed text did not echo")
	}
	if err := h.HandBack(ctx, threadID); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.Get(ctx, sessionID); got.InputOwner != domain.InputOwnerAgent {
		t.Errorf("input owner after hand-back = %s", got.InputOwner)
	}
	if state := h.InteractState(threadID, cwd); state.TakenOver || !state.AtPrompt || state.Line != "" {
		t.Errorf("state after hand-back = %+v", state)
	}
	run, err = h.RunForThread(ctx, threadID, cwd, "echo after")
	if err != nil {
		t.Fatal(err)
	}
	if run.Block.Command != "echo after" || strings.Contains(run.Output, "left-by-user") {
		t.Errorf("after hand-back the command was %q, output %q", run.Block.Command, run.Output)
	}

	// The same for keys: the agent's next line at the prompt is its own.
	if _, err := h.TakeOver(ctx, threadID, "conn_a"); err != nil {
		t.Fatal(err)
	}
	if err := h.Input(ctx, sessionID, []byte("echo left-again"), domain.InputOwnerHuman); err != nil {
		t.Fatal(err)
	}
	if !waitForScreen(t, h, sessionID, "left-again") {
		t.Fatal("the typed text did not echo")
	}
	if err := h.HandBack(ctx, threadID); err != nil {
		t.Fatal(err)
	}
	send(t, h, ctx, threadID, cwd, keyText("echo via-keys"), keyEnter)
	waitKeys(t, h, ctx, threadID, cwd, domain.WaitCondition{Exit: true})
	var keyed int
	if err := h.store.DB().QueryRowContext(t.Context(),
		`SELECT count(*) FROM blocks WHERE command = 'echo via-keys' AND thread_id = ?`, threadID).Scan(&keyed); err != nil || keyed != 1 {
		t.Errorf("the agent's keys after a hand-back did not run alone: %d blocks, %v", keyed, err)
	}

	// A hand-back of a terminal nobody holds changes nothing: the agent's line stays.
	send(t, h, ctx, threadID, cwd, keyText("echo kept"))
	if err := h.HandBack(ctx, threadID); err != nil {
		t.Fatal(err)
	}
	if state := h.InteractState(threadID, cwd); state.Line != "echo kept" {
		t.Errorf("a hand-back of a free terminal touched the line: %+v", state)
	}
	send(t, h, ctx, threadID, cwd, keyCtrlC)

	// A client that goes away hands back what it holds, and only that.
	if _, err := h.TakeOver(ctx, threadID, "conn_b"); err != nil {
		t.Fatal(err)
	}
	h.Release(ctx, "conn_a")
	if !h.InteractState(threadID, cwd).TakenOver {
		t.Error("another connection's close handed back conn_b's terminal")
	}
	h.Release(ctx, "conn_b")
	if h.InteractState(threadID, cwd).TakenOver {
		t.Error("the holder's close did not hand the terminal back")
	}

	// A hand-back while a program runs: what the user typed ahead is dropped once the prompt
	// is back, before the agent may type there.
	send(t, h, ctx, threadID, cwd, keyText("sleep 1"), keyEnter)
	if _, err := h.TakeOver(ctx, threadID, "conn_c"); err != nil {
		t.Fatal(err)
	}
	if err := h.Input(ctx, sessionID, []byte("echo typed-ahead"), domain.InputOwnerHuman); err != nil {
		t.Fatal(err)
	}
	if err := h.HandBack(ctx, threadID); err != nil {
		t.Fatal(err)
	}
	waitKeys(t, h, ctx, threadID, cwd, domain.WaitCondition{Exit: true})
	run, err = h.RunForThread(ctx, threadID, cwd, "echo after-sleep")
	if err != nil {
		t.Fatal(err)
	}
	if run.Block.Command != "echo after-sleep" || strings.Contains(run.Output, "typed-ahead") {
		t.Errorf("after a hand-back during a program the command was %q, output %q", run.Block.Command, run.Output)
	}
}

// TestReadsNeverStartATerminal_REQ_FTU_001: read_screen and wait_for are decided as reads, so
// they use the thread's PTY only when it has one; starting a shell is send_keys' (decision 3).
func TestReadsNeverStartATerminal_REQ_FTU_001(t *testing.T) {
	h := newHarness(t)
	cwd := t.TempDir()
	threadID := thread(t, h, cwd)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	if _, err := h.ReadScreen(ctx, threadID, cwd); !errors.Is(err, domain.ErrNoTerminal) {
		t.Errorf("read_screen with no PTY: %v, want ErrNoTerminal", err)
	}
	if _, err := h.WaitFor(ctx, threadID, cwd, domain.WaitCondition{Exit: true, Timeout: time.Second}); !errors.Is(err, domain.ErrNoTerminal) {
		t.Errorf("wait_for with no PTY: %v, want ErrNoTerminal", err)
	}
	if list, _ := h.List(ctx); len(list) != 0 {
		t.Errorf("a read started %d sessions", len(list))
	}
	if _, err := h.RunForThread(ctx, threadID, cwd, "true"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ReadScreen(ctx, threadID, t.TempDir()); !errors.Is(err, domain.ErrNoTerminal) {
		t.Errorf("read_screen in another cwd: %v, want ErrNoTerminal", err)
	}
	if list, _ := h.List(ctx); len(list) != 1 || list[0].State != domain.StateAlive {
		t.Errorf("a read in another cwd replaced the PTY: %+v", list)
	}
}

// TestAnInterruptKeepsTheAgentsNextBlock_REQ_FTU_001: after a ctrl+c at the prompt the next
// line the agent enters still opens a block with origin agent, the empty line typed ahead of
// it notwithstanding.
func TestAnInterruptKeepsTheAgentsNextBlock_REQ_FTU_001(t *testing.T) {
	h := newHarness(t)
	cwd := t.TempDir()
	threadID := thread(t, h, cwd)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	if _, err := h.RunForThread(ctx, threadID, cwd, "true"); err != nil {
		t.Fatal(err)
	}
	send(t, h, ctx, threadID, cwd, keyText("echo dropped"))
	send(t, h, ctx, threadID, cwd, keyCtrlC)
	waitKeys(t, h, ctx, threadID, cwd, domain.WaitCondition{Idle: 300 * time.Millisecond})
	send(t, h, ctx, threadID, cwd, keyText("echo origin-check"), keyEnter)
	waitKeys(t, h, ctx, threadID, cwd, domain.WaitCondition{Exit: true})
	var origin string
	if err := h.store.DB().QueryRowContext(t.Context(),
		`SELECT origin FROM blocks WHERE command = 'echo origin-check' AND thread_id = ?`, threadID).Scan(&origin); err != nil {
		t.Fatalf("the block after the interrupt: %v", err)
	}
	if origin != "agent" {
		t.Errorf("origin %s, want agent", origin)
	}
}

// TestTheDecisionHoldsForTheTerminalItSaw_REQ_FTU_003: keys decided on one state of the
// terminal are not written into another: the call fails and nothing reaches the PTY
// (decision 5).
func TestTheDecisionHoldsForTheTerminalItSaw_REQ_FTU_003(t *testing.T) {
	h := newHarness(t)
	cwd := t.TempDir()
	threadID := thread(t, h, cwd)
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	if _, err := h.RunForThread(ctx, threadID, cwd, "true"); err != nil {
		t.Fatal(err)
	}

	program := domain.InteractState{}
	if _, err := h.SendKeys(ctx, threadID, cwd, []domain.Key{keyText("echo never-ran"), keyEnter}, program); !errors.Is(err, domain.ErrTerminalChanged) {
		t.Errorf("keys decided as a program's input sent to the prompt: %v", err)
	}
	stale := domain.InteractState{AtPrompt: true, Line: "rm -"}
	if _, err := h.SendKeys(ctx, threadID, cwd, []domain.Key{keyText("rf x"), keyEnter}, stale); !errors.Is(err, domain.ErrTerminalChanged) {
		t.Errorf("keys decided on another line: %v", err)
	}
	screen, err := h.ReadScreen(ctx, threadID, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(screen.Text, "never-ran") || strings.Contains(screen.Text, "rf x") {
		t.Errorf("refused keys reached the screen: %q", screen.Text)
	}
	if _, err := h.SendKeys(ctx, threadID, cwd, []domain.Key{{Name: "tab"}}, h.InteractState(threadID, cwd)); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("an editing key at the prompt: %v", err)
	}
}

// TestAProgramsUnenteredInputNeverJoinsTheNextLine_REQ_FTU_003: text typed to a program that
// never reads it stays in the tty and lands on the shell's next line; the agent's next write at
// the prompt drops it first, so the line the shell runs is the one decided.
func TestAProgramsUnenteredInputNeverJoinsTheNextLine_REQ_FTU_003(t *testing.T) {
	h := newHarness(t)
	cwd := t.TempDir()
	threadID := thread(t, h, cwd)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	if _, err := h.RunForThread(ctx, threadID, cwd, "true"); err != nil {
		t.Fatal(err)
	}
	send(t, h, ctx, threadID, cwd, keyText("sleep 1"), keyEnter)
	waitKeys(t, h, ctx, threadID, cwd, domain.WaitCondition{Idle: 200 * time.Millisecond})
	send(t, h, ctx, threadID, cwd, keyText("echo lef"))
	waitKeys(t, h, ctx, threadID, cwd, domain.WaitCondition{Exit: true})
	if state := h.InteractState(threadID, cwd); !state.AtPrompt || state.Line != "" {
		t.Fatalf("after the program: %+v", state)
	}
	send(t, h, ctx, threadID, cwd, keyText("echo t-over"), keyEnter)
	waitKeys(t, h, ctx, threadID, cwd, domain.WaitCondition{Exit: true})
	var joined, alone int
	db := h.store.DB()
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM blocks WHERE command LIKE 'echo lef%' AND thread_id = ?`, threadID).Scan(&joined); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM blocks WHERE command = 'echo t-over' AND thread_id = ?`, threadID).Scan(&alone); err != nil {
		t.Fatal(err)
	}
	if joined != 0 || alone != 1 {
		t.Errorf("the shell ran the program's leftover with the agent's line: %d joined, %d alone", joined, alone)
	}
}

// TestAWaitAfterKeysSeesOnlyTheirOutput_REQ_FTU_001: a wait that follows keys (send_keys with a
// condition) matches its regex against the output after them, read as text, and not against
// the screen, where an earlier prompt matching the same regex is still drawn. The keys' plan
// carries the output sequence number they were sent after (delta `2026-10-usf2-002-live`).
func TestAWaitAfterKeysSeesOnlyTheirOutput_REQ_FTU_001(t *testing.T) {
	h := newHarness(t)
	cwd := t.TempDir()
	threadID := thread(t, h, cwd)
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()

	send(t, h, ctx, threadID, cwd, keyText("cat"), keyEnter)
	waitKeys(t, h, ctx, threadID, cwd, domain.WaitCondition{Idle: 300 * time.Millisecond})
	plan := send(t, h, ctx, threadID, cwd, keyText("ready-1"), keyEnter)
	waitKeys(t, h, ctx, threadID, cwd, domain.WaitCondition{Regex: regexp.MustCompile(`ready-1\s+ready-1`)})

	// ready-1 is on the screen; keys that print nothing new do not match it again.
	quiet := send(t, h, ctx, threadID, cwd, keyText("x"))
	old := regexp.MustCompile(`ready-1`)
	if out := waitKeys(t, h, ctx, threadID, cwd, domain.WaitCondition{Regex: old, Fresh: true, After: quiet.Seq, Timeout: 700 * time.Millisecond}); out.Reason != domain.WaitTimeout {
		t.Errorf("a wait after keys matched the screen's old output: %q", out.Reason)
	}
	if out := waitKeys(t, h, ctx, threadID, cwd, domain.WaitCondition{Regex: old, Timeout: 700 * time.Millisecond}); out.Reason != domain.WaitMatched {
		t.Errorf("a wait on the screen did not see ready-1: %q", out.Reason)
	}
	if quiet.Seq <= plan.Seq {
		t.Errorf("the plan's seq %d is not after the earlier keys' %d", quiet.Seq, plan.Seq)
	}
	next := send(t, h, ctx, threadID, cwd, keyEnter, keyText("ready-2"), keyEnter)
	out := waitKeys(t, h, ctx, threadID, cwd, domain.WaitCondition{Regex: regexp.MustCompile(`ready-2`), Fresh: true, After: next.Seq})
	if out.Reason != domain.WaitMatched || !strings.Contains(out.Screen.Text, "ready-2") {
		t.Errorf("the wait after ready-2: %q, screen %q", out.Reason, out.Screen.Text)
	}
	// Conditions combine and the first that holds ends the wait: the output, else the quiet.
	third := send(t, h, ctx, threadID, cwd, keyText("ready-3"), keyEnter)
	both := domain.WaitCondition{Regex: regexp.MustCompile(`ready-3`), Idle: domain.MinWaitIdle, Fresh: true, After: third.Seq}
	if out := waitKeys(t, h, ctx, threadID, cwd, both); out.Reason != domain.WaitMatched {
		t.Errorf("regex or quiet after ready-3: %q", out.Reason)
	}
	both.Regex = regexp.MustCompile(`never-printed`)
	if out := waitKeys(t, h, ctx, threadID, cwd, both); out.Reason != domain.WaitIdle {
		t.Errorf("regex or quiet with nothing matching: %q", out.Reason)
	}
	send(t, h, ctx, threadID, cwd, keyCtrlD)
	waitKeys(t, h, ctx, threadID, cwd, domain.WaitCondition{Exit: true})

	// SkipEcho reads the output after the line the keys entered: the terminal's echo of that
	// line does not answer a wait, as `regex: "postgres"` after typing `psql -U postgres` did.
	send(t, h, ctx, threadID, cwd, keyText("sh -c 'read x; echo got-$x'"), keyEnter)
	waitKeys(t, h, ctx, threadID, cwd, domain.WaitCondition{Idle: 300 * time.Millisecond})
	line := send(t, h, ctx, threadID, cwd, keyText("mark"), keyEnter)
	if out := waitKeys(t, h, ctx, threadID, cwd, domain.WaitCondition{Regex: regexp.MustCompile(`got-mark`), Fresh: true, After: line.Seq, SkipEcho: true}); out.Reason != domain.WaitMatched {
		t.Errorf("the program's answer after its line: %q", out.Reason)
	}
	echo := domain.WaitCondition{Regex: regexp.MustCompile(`(?m)^mark\s*$`), Fresh: true, After: line.Seq, Timeout: 700 * time.Millisecond}
	if out := waitKeys(t, h, ctx, threadID, cwd, echo); out.Reason != domain.WaitMatched {
		t.Errorf("without SkipEcho the echo did not match: %q", out.Reason)
	}
	echo.SkipEcho = true
	if out := waitKeys(t, h, ctx, threadID, cwd, echo); out.Reason != domain.WaitTimeout {
		t.Errorf("a wait after the entered line matched its echo: %q", out.Reason)
	}
}
