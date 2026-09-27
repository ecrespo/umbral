package agents

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/ecrespo/umbral/internal/agents/domain"
	"github.com/ecrespo/umbral/internal/agents/ports"
	llm "github.com/ecrespo/umbral/internal/llmgw/domain"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
)

const ulid1 = "01J9Z3K8T2QH6W4V5X7Y8Z9A0B"

// TestSendStreamsDeltas_REQ_AGT_001: thread.send runs the loop and the answer arrives as
// thread.delta notifications, then thread.turn_finished with the usage.
func TestSendStreamsDeltas_REQ_AGT_001(t *testing.T) {
	r := newRig(t, answer("hello there"))
	th := r.thread(t, domain.CreateParams{})
	res, end := r.send(t, th.ID, "hi", "")

	var text strings.Builder
	for _, ev := range r.bus.events() {
		if d, ok := ev.(ports.ThreadDelta); ok {
			if d.ThreadID != th.ID || d.TurnID != res.TurnID {
				t.Fatalf("delta %+v", d)
			}
			text.WriteString(d.Text)
		}
	}
	if text.String() != "hello there" {
		t.Fatalf("deltas said %q", text.String())
	}
	if end.StopReason != domain.StopEndTurn || end.Usage.InTokens != 100 || end.Usage.OutTokens != 20 || end.Usage.CostMicroUSD != 7 {
		t.Fatalf("turn finished %+v", end)
	}
	got, _ := r.rt.Get(t.Context(), th.ID)
	if got.State != domain.StateIdle || got.TokensUsed != 120 || got.CostMicroUSD != 7 {
		t.Fatalf("the thread after the turn: %+v", got)
	}
	msgs, _ := r.rt.Messages(t.Context(), th.ID)
	if len(msgs) != 2 || msgs[0].Content != "hi" || msgs[1].Content != "hello there" {
		t.Fatalf("history %+v", msgs)
	}
}

// TestPersistBeforeNotify_REQ_AGT_011: every delta, tool call status and turn end is in the
// store at the moment it is published (the checking bus fails the test otherwise).
func TestPersistBeforeNotify_REQ_AGT_011(t *testing.T) {
	r := newRig(t, callTool("read_file", `{"path":"a.go"}`), answer("done reading"))
	th := r.thread(t, domain.CreateParams{})
	_, end := r.send(t, th.ID, "read a.go", "")
	if end.StopReason != domain.StopEndTurn {
		t.Fatalf("stop %s", end.StopReason)
	}
	var statuses []domain.ToolStatus
	for _, ev := range r.bus.events() {
		if c, ok := ev.(ports.ThreadToolCall); ok {
			statuses = append(statuses, c.Call.Status)
		}
	}
	if len(statuses) != 2 || statuses[0] != domain.ToolPending || statuses[1] != domain.ToolOK {
		t.Fatalf("tool call statuses published %v", statuses)
	}
	if r.tools.tools["read_file"].ran.Load() != 1 {
		t.Fatal("the allowed tool did not run")
	}
	// The second model call reads the tool's result.
	reqs := r.models.requests()
	last := reqs[len(reqs)-1].Request.Messages
	if tm := last[len(last)-1]; tm.Role != llm.RoleTool || tm.Text != "file contents" {
		t.Fatalf("the model did not read the result: %+v", tm)
	}
}

// TestAWriteThatFailsStopsTheTurn_REQ_AGT_011: a write that fails stops the turn with
// storage_error, and nothing it would have recorded is published or run (Analyze C-01).
func TestAWriteThatFailsStopsTheTurn_REQ_AGT_011(t *testing.T) {
	r := newRig(t, callTool("read_file", `{"path":"a.go"}`), answer("never"))
	th := r.thread(t, domain.CreateParams{})
	r.store.mu.Lock()
	r.store.failAfter = 1 // the user's message is written; the tool call is not
	r.store.mu.Unlock()
	_, end := r.send(t, th.ID, "read", "")
	if end.StopReason != domain.StopStorageError {
		t.Fatalf("stop %s, want storage_error", end.StopReason)
	}
	if r.tools.tools["read_file"].ran.Load() != 0 {
		t.Fatal("a tool ran although its call could not be recorded")
	}
	for _, ev := range r.bus.events() {
		if _, ok := ev.(ports.ThreadToolCall); ok {
			t.Fatal("a tool call that was never persisted was published")
		}
	}
}

// TestStopsAtMaxSteps_REQ_AGT_008: a model that keeps calling tools is stopped at max_steps,
// and a thread past its token budget is stopped with budget.
func TestStopsAtMaxSteps_REQ_AGT_008(t *testing.T) {
	loop := make([][]llm.Event, 10)
	for i := range loop {
		loop[i] = callTool("read_file", `{"path":"a"}`)
	}
	r := newRig(t, loop...)
	th := r.thread(t, domain.CreateParams{MaxSteps: 3})
	_, end := r.send(t, th.ID, "loop", "")
	if end.StopReason != domain.StopMaxSteps || len(r.models.requests()) != 3 {
		t.Fatalf("stop %s after %d calls, want max_steps after 3", end.StopReason, len(r.models.requests()))
	}

	r2 := newRig(t, loop...)
	th2 := r2.thread(t, domain.CreateParams{BudgetTokens: 100})
	_, end = r2.send(t, th2.ID, "spend", "")
	if end.StopReason != domain.StopBudget || len(r2.models.requests()) != 2 {
		t.Fatalf("stop %s after %d calls, want budget after 2 (60 tokens each)", end.StopReason, len(r2.models.requests()))
	}
	if _, err := r2.rt.Send(t.Context(), ports.SendParams{ThreadID: th2.ID, Text: "more"}); !errors.Is(err, domain.ErrBudgetExceeded) {
		t.Fatalf("a send on a spent thread: %v", err)
	}
}

// TestAskModeReadOnlyTools_REQ_AGT_009: in `ask` mode the model is offered ReadOnly tools
// only, and a call to another one is refused by the policy without running.
func TestAskModeReadOnlyTools_REQ_AGT_009(t *testing.T) {
	r := newRig(t, callTool("write_file", `{"path":"x"}`), answer("ok"))
	th := r.thread(t, domain.CreateParams{Mode: secdomain.ModeAsk})
	r.send(t, th.ID, "write", "")
	offered := r.models.requests()[0].Request.Tools
	if len(offered) != 1 || offered[0].Name != "read_file" {
		t.Fatalf("ask mode offered %+v", offered)
	}
	if r.tools.tools["write_file"].ran.Load() != 0 {
		t.Fatal("a tool ask mode does not expose ran")
	}
	calls, _ := r.store.ToolCalls(t.Context(), th.ID)
	if len(calls) != 1 || calls[0].Status != domain.ToolDeniedByPolicy {
		t.Fatalf("calls %+v", calls)
	}
}

// TestInvalidToolInputIsRecorded_REQ_AGT_002: input the tool refuses is invalid_args, and the
// model reads why.
func TestInvalidToolInputIsRecorded_REQ_AGT_002(t *testing.T) {
	r := newRig(t, callTool("read_file", `{}`), answer("ok"))
	th := r.thread(t, domain.CreateParams{})
	r.send(t, th.ID, "read", "")
	calls, _ := r.store.ToolCalls(t.Context(), th.ID)
	if len(calls) != 1 || calls[0].Status != domain.ToolInvalidArgs {
		t.Fatalf("calls %+v", calls)
	}
}

// TestModelSwitchNextTurn_REQ_AGT_010: a model change applies from the next turn and the
// history is kept whole.
func TestModelSwitchNextTurn_REQ_AGT_010(t *testing.T) {
	r := newRig(t, answer("first"), answer("second"))
	th := r.thread(t, domain.CreateParams{Model: "ollama/a"})
	r.send(t, th.ID, "one", "")
	model := "ollama/b"
	if _, err := r.rt.Update(t.Context(), th.ID, domain.UpdateParams{Model: &model}); err != nil {
		t.Fatal(err)
	}
	r.send(t, th.ID, "two", "")
	reqs := r.models.requests()
	if reqs[0].Model != "ollama/a" || reqs[1].Model != "ollama/b" {
		t.Fatalf("models used %q then %q", reqs[0].Model, reqs[1].Model)
	}
	history := reqs[1].Request.Messages
	if len(history) != 3 || history[0].Text != "one" || history[1].Text != "first" || history[2].Text != "two" {
		t.Fatalf("the second turn's history: %+v", history)
	}
}

// TestSendIdempotentByClientMsgID_REQ_AGT_015: two identical sends make one turn and run its
// tools once, while the first is running and after it.
func TestSendIdempotentByClientMsgID_REQ_AGT_015(t *testing.T) {
	r := newRig(t, callTool("read_file", `{"path":"a"}`), answer("done"))
	th := r.thread(t, domain.CreateParams{})
	// The model holds its answer until every repeat has returned: they all arrive while the
	// first turn runs.
	r.models.hold = make(chan struct{})

	var wg sync.WaitGroup
	results := make([]ports.SendResult, 4)
	errs := make([]error, 4)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "go", ClientMsgID: ulid1})
		}()
	}
	wg.Wait()
	if got, _ := r.rt.Get(t.Context(), th.ID); got.State != domain.StateRunning {
		t.Fatalf("the repeats must arrive while the turn runs; state %s", got.State)
	}
	close(r.models.hold)
	<-r.bus.ended
	for i := range results {
		if errs[i] != nil || results[i] != results[0] {
			t.Fatalf("send %d: %+v %v, want %+v", i, results[i], errs[i], results[0])
		}
	}
	// The repeat returns the original without reading its attachments again (this rig has no
	// context gatherer, so reading one would fail).
	again, err := r.rt.Send(t.Context(), ports.SendParams{
		ThreadID: th.ID, Text: "go", ClientMsgID: ulid1,
		Attachments: []ports.AttachmentRef{{Kind: "file", Ref: "gone.txt"}},
	})
	if err != nil || again != results[0] {
		t.Fatalf("after the turn: %+v %v", again, err)
	}
	if n := r.tools.tools["read_file"].ran.Load(); n != 1 {
		t.Fatalf("the tool ran %d times", n)
	}
	if n := len(r.models.requests()); n != 2 {
		t.Fatalf("%d model calls, want the one turn's 2", n)
	}
}

// TestASecondSendWhileRunningConflicts_REQ_AGT_001: a different message while a turn runs is
// CONFLICT (API §5.20).
func TestASecondSendWhileRunningConflicts_REQ_AGT_001(t *testing.T) {
	r := newRig(t, answer("x"))
	th := r.thread(t, domain.CreateParams{})
	r.store.mu.Lock()
	running := r.store.threads[th.ID]
	running.State = domain.StateRunning
	r.store.threads[th.ID] = running
	r.store.mu.Unlock()
	if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "x"}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err %v", err)
	}
}

// TestCompactedEventPersistedThenPublished_REQ_CTX_004: over the window the history is
// compacted before the call, the summary is a system_note, and context.compacted follows it.
func TestCompactedEventPersistedThenPublished_REQ_CTX_004(t *testing.T) {
	r := newRig(t, answer("a summary of the past"), answer("fixed"))
	r.models.window = 2000
	th := r.thread(t, domain.CreateParams{})
	// A long history of earlier turns.
	for i := range 20 {
		turn := newID("trn")
		r.store.messages = append(r.store.messages,
			domain.Message{ID: newID("msg"), ThreadID: th.ID, TurnID: turn, Role: domain.RoleUser, Content: strings.Repeat("q", 400)},
			domain.Message{ID: newID("msg"), ThreadID: th.ID, TurnID: turn, Role: domain.RoleAssistant, Content: strings.Repeat("a", 400)},
		)
		_ = i
	}
	_, end := r.send(t, th.ID, "fix it", "")
	if end.StopReason != domain.StopEndTurn {
		t.Fatalf("stop %s", end.StopReason)
	}
	reqs := r.models.requests()
	if len(reqs) != 2 || reqs[0].Class != "fast" {
		t.Fatalf("the summary is made by the fast class first: %+v", reqs)
	}
	main := reqs[1].Request
	last := main.Messages[len(main.Messages)-1]
	if !strings.Contains(main.System, "a summary of the past") || last.Text != "fix it" || main.Messages[0].Role != llm.RoleUser {
		t.Fatalf("the call carries the summary and ends with the request: %q / %+v", main.System, last)
	}
	if len(main.Messages) >= 41 {
		t.Fatalf("nothing was dropped: %d messages", len(main.Messages))
	}
	var compacted *ports.ContextCompacted
	for _, ev := range r.bus.events() {
		if c, ok := ev.(ports.ContextCompacted); ok {
			compacted = &c
		}
	}
	if compacted == nil || compacted.ThreadID != th.ID || compacted.BeforeTokens <= compacted.AfterTokens {
		t.Fatalf("context.compacted %+v", compacted)
	}
}

// TestAnUnreachableModelEndsTheTurn_REQ_LLM_003: no candidate left is provider_error, and the
// thread takes the next message.
func TestAnUnreachableModelEndsTheTurn_REQ_LLM_003(t *testing.T) {
	r := newRig(t)
	th := r.thread(t, domain.CreateParams{})
	_, end := r.send(t, th.ID, "hi", "")
	if end.StopReason != domain.StopProviderError {
		t.Fatalf("stop %s", end.StopReason)
	}
	if got, _ := r.rt.Get(t.Context(), th.ID); got.State != domain.StateIdle {
		t.Fatalf("state %s", got.State)
	}
}

// TestCreateValidates_REQ_AGT_001: thread.create fills its defaults and refuses bad input.
func TestCreateValidates_REQ_AGT_001(t *testing.T) {
	r := newRig(t)
	th := r.thread(t, domain.CreateParams{})
	if th.Mode != secdomain.ModeNormal || th.ModelClass != "code" || th.MaxSteps != 50 || th.BudgetTokens != 400000 || !strings.HasPrefix(th.ID, "thr_") {
		t.Fatalf("defaults %+v", th)
	}
	for _, bad := range []domain.CreateParams{
		{Cwd: "relative"}, {Cwd: "/x", Mode: "yolo"}, {Cwd: "/x", ModelClass: "huge"}, {Cwd: "/x", MaxSteps: 201},
	} {
		if _, err := r.rt.Create(t.Context(), bad); !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
	for _, text := range []string{"", strings.Repeat("x", domain.MaxMessageChars+1)} {
		if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: text}); !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("text of %d: %v", len(text), err)
		}
	}
	if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "x", ClientMsgID: "not-a-ulid"}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("client_msg_id: %v", err)
	}
}

// TestEveryWriteThatFailsStopsTheTurn_REQ_AGT_011: whichever write fails — a delta, a tool
// call's first record — the turn stops with storage_error and publishes nothing unrecorded.
func TestEveryWriteThatFailsStopsTheTurn_REQ_AGT_011(t *testing.T) {
	for _, c := range []struct {
		name      string
		script    []llm.Event
		failAfter int
		ran       bool
	}{
		// user message, first delta's insert; the second delta's append fails.
		{"a delta", answer("hello there"), 2, false},
		// user message, the empty assistant message; the pending tool call fails.
		{"a pending tool call", callTool("read_file", `{"path":"a"}`), 2, false},
		// ... and its outcome after it ran.
		{"a tool call's outcome", callTool("read_file", `{"path":"a"}`), 3, true},
		// user message, assistant message, pending call; the approval it asks for fails.
		{"an approval", callTool("run", `{"path":"make"}`), 3, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, c.script, answer("never"))
			th := r.thread(t, domain.CreateParams{})
			r.store.mu.Lock()
			r.store.failAfter = c.failAfter
			r.store.mu.Unlock()
			_, end := r.send(t, th.ID, "go", "")
			if end.StopReason != domain.StopStorageError {
				t.Fatalf("stop %s, want storage_error", end.StopReason)
			}
			ran := r.tools.tools["read_file"].ran.Load()+r.tools.tools["run"].ran.Load() == 1
			if ran != c.ran {
				t.Fatalf("the tool ran: %v, want %v", ran, c.ran)
			}
		})
	}
}

// TestTheNextTurnReadsEarlierToolResults_REQ_AGT_011: a later turn rebuilds the history from
// the store, tool calls and results included, in order.
func TestTheNextTurnReadsEarlierToolResults_REQ_AGT_011(t *testing.T) {
	r := newRig(t, callTool("read_file", `{"path":"a"}`), answer("read it"), answer("again"))
	th := r.thread(t, domain.CreateParams{})
	r.send(t, th.ID, "read", "")
	r.send(t, th.ID, "and now?", "")
	reqs := r.models.requests()
	h := reqs[len(reqs)-1].Request.Messages
	if len(h) != 5 || len(h[1].ToolCalls) != 1 || h[2].Role != llm.RoleTool || h[2].Text != "file contents" ||
		h[2].ToolCallID != h[1].ToolCalls[0].ID || h[3].Text != "read it" || h[4].Text != "and now?" {
		t.Fatalf("the second turn's history: %+v", h)
	}
}

// TestAFailedSummaryRecordPublishesNothing_REQ_CTX_004: when the summary cannot be written, the
// turn stops with storage_error and context.compacted is not published.
func TestAFailedSummaryRecordPublishesNothing_REQ_CTX_004(t *testing.T) {
	r := newRig(t, answer("a summary"), answer("never"))
	r.models.window = 2000
	th := r.thread(t, domain.CreateParams{})
	for range 20 {
		r.store.messages = append(r.store.messages,
			domain.Message{ID: newID("msg"), ThreadID: th.ID, TurnID: "trn_old", Role: domain.RoleUser, Content: strings.Repeat("q", 400)},
			domain.Message{ID: newID("msg"), ThreadID: th.ID, TurnID: "trn_old", Role: domain.RoleAssistant, Content: strings.Repeat("a", 400)},
		)
	}
	r.store.mu.Lock()
	r.store.failAfter = 1
	r.store.mu.Unlock()
	_, end := r.send(t, th.ID, "fix it", "")
	if end.StopReason != domain.StopStorageError {
		t.Fatalf("stop %s", end.StopReason)
	}
	for _, ev := range r.bus.events() {
		if _, ok := ev.(ports.ContextCompacted); ok {
			t.Fatal("context.compacted published for a summary that was never recorded")
		}
	}
}

// TestASendWithNoModelIsProviderUnavailable_REQ_LLM_003: a thread none of whose candidates can
// serve it is PROVIDER_UNAVAILABLE at send, and nothing is persisted (API §5.20).
func TestASendWithNoModelIsProviderUnavailable_REQ_LLM_003(t *testing.T) {
	r := newRig(t, answer("x"))
	th := r.thread(t, domain.CreateParams{})
	r.models.unavailable = true
	if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "hi"}); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("err %v", err)
	}
	if msgs, _ := r.rt.Messages(t.Context(), th.ID); len(msgs) != 0 {
		t.Fatalf("a refused send persisted %+v", msgs)
	}
}

// TestTheTurnRunsWithTheModeItStartedIn_REQ_AGT_009: a mode change that lands between the
// send's read and the turn's start is the mode the turn runs with, not the stale one.
func TestTheTurnRunsWithTheModeItStartedIn_REQ_AGT_009(t *testing.T) {
	r := newRig(t, answer("ok"))
	th := r.thread(t, domain.CreateParams{})
	ask := secdomain.ModeAsk
	r.store.beforeBegin = func() {
		if _, err := r.store.UpdateThread(t.Context(), th.ID, domain.UpdateParams{Mode: &ask}, 5); err != nil {
			t.Error(err)
		}
	}
	r.send(t, th.ID, "hi", "")
	offered := r.models.requests()[0].Request.Tools
	if len(offered) != 1 || offered[0].Name != "read_file" {
		t.Fatalf("the turn ran in the stale mode: offered %+v", offered)
	}
}

// TestAClosedRuntimeStartsNoTurn_REQ_AGT_001: a send racing Close starts nothing.
func TestAClosedRuntimeStartsNoTurn_REQ_AGT_001(t *testing.T) {
	r := newRig(t, answer("x"))
	th := r.thread(t, domain.CreateParams{})
	r.rt.Close()
	if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "hi"}); err == nil {
		t.Fatal("a closed runtime accepted a turn")
	}
	if msgs, _ := r.rt.Messages(t.Context(), th.ID); len(msgs) != 0 {
		t.Fatal("a closed runtime persisted a message it will never answer")
	}
}
