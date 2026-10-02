package agents

import (
	"strings"
	"testing"

	"github.com/ecrespo/umbral/internal/agents/domain"
	llm "github.com/ecrespo/umbral/internal/llmgw/domain"
)

// callToolOn is callTool as the router reports it: the usage names the model that served it.
func callToolOn(model, name, input string) []llm.Event {
	evs := callTool(name, input)
	for i := range evs {
		if evs[i].Kind == llm.EventUsage {
			evs[i].Usage.Model = model
		}
	}
	return evs
}

// repairMessages are the tool messages the model read back as errors, in the order it read them.
func repairMessages(r *rig) []string {
	reqs := r.models.requests()
	last := reqs[len(reqs)-1].Request.Messages
	var out []string
	for _, m := range last {
		if m.Role == llm.RoleTool && m.ToolError {
			out = append(out, m.Text)
		}
	}
	return out
}

// TestInvalidArgsRepairOnce_REQ_AGT_006: arguments the schema refuses get one retry, with a
// message that tells the model what to repair; a second invalid call ends the turn with
// stop_reason = tool_error, and the model is not called a third time.
func TestInvalidArgsRepairOnce_REQ_AGT_006(t *testing.T) {
	r := newRig(t,
		callTool("read_file", `{}`),
		callTool("read_file", `{"file":"a"}`),
		answer("never reached"),
	)
	th := r.thread(t, domain.CreateParams{})
	_, end := r.send(t, th.ID, "read", "")
	if end.StopReason != domain.StopToolError {
		t.Fatalf("stop %s, want tool_error", end.StopReason)
	}
	if n := len(r.models.requests()); n != 2 {
		t.Fatalf("the model was called %d times, want 2: the call and its one repair", n)
	}
	repairs := repairMessages(r)
	if len(repairs) != 1 || !strings.Contains(repairs[0], "schema") || !strings.Contains(repairs[0], "read_file") {
		t.Fatalf("the retry read %q, want one repair message naming the tool and its schema", repairs)
	}
	calls, _ := r.store.ToolCalls(t.Context(), th.ID)
	if len(calls) != 2 || calls[0].Status != domain.ToolInvalidArgs || calls[1].Status != domain.ToolInvalidArgs {
		t.Fatalf("calls %+v, want two invalid_args rows", calls)
	}
	if r.tools.tools["read_file"].ran.Load() != 0 {
		t.Fatal("an invalid call ran")
	}
}

// TestARepairedCallRuns_REQ_AGT_006: when the retry is valid the call runs and the turn goes
// on to its end.
func TestARepairedCallRuns_REQ_AGT_006(t *testing.T) {
	r := newRig(t,
		callTool("read_file", `{}`),
		callTool("read_file", `{"path":"a"}`),
		answer("read it"),
	)
	th := r.thread(t, domain.CreateParams{})
	_, end := r.send(t, th.ID, "read", "")
	if end.StopReason != domain.StopEndTurn {
		t.Fatalf("stop %s, want end_turn", end.StopReason)
	}
	if r.tools.tools["read_file"].ran.Load() != 1 {
		t.Fatal("the repaired call did not run")
	}
}

// TestEachInvalidCallGetsItsOwnRepair_REQ_AGT_006: the one retry belongs to one invalid call.
// A valid call in between spends it, so a later invalid call in the same turn is repaired
// again rather than ending the turn.
func TestEachInvalidCallGetsItsOwnRepair_REQ_AGT_006(t *testing.T) {
	r := newRig(t,
		callTool("read_file", `{}`),
		callTool("read_file", `{"path":"a"}`),
		callTool("read_file", `{}`),
		callTool("read_file", `{"path":"b"}`),
		answer("both read"),
	)
	th := r.thread(t, domain.CreateParams{})
	_, end := r.send(t, th.ID, "read", "")
	if end.StopReason != domain.StopEndTurn || r.tools.tools["read_file"].ran.Load() != 2 {
		t.Fatalf("stop %s with %d runs, want end_turn with 2", end.StopReason, r.tools.tools["read_file"].ran.Load())
	}
}

// TestAnUnknownToolIsRepairedLikeBadArgs_REQ_AGT_006: a call to a tool that does not exist is
// as unusable as one with bad arguments: it gets the same one retry.
func TestAnUnknownToolIsRepairedLikeBadArgs_REQ_AGT_006(t *testing.T) {
	r := newRig(t,
		callTool("read_files", `{"path":"a"}`),
		callTool("readfile", `{"path":"a"}`),
		answer("never reached"),
	)
	th := r.thread(t, domain.CreateParams{})
	_, end := r.send(t, th.ID, "read", "")
	if end.StopReason != domain.StopToolError || len(r.models.requests()) != 2 {
		t.Fatalf("stop %s after %d calls, want tool_error after 2", end.StopReason, len(r.models.requests()))
	}
}

// TestInputThatIsNotJSONNeverRuns_REQ_AGT_006: arguments that are not JSON are invalid. They
// must not reach the tool as an empty object, which a tool with no required field accepts.
func TestInputThatIsNotJSONNeverRuns_REQ_AGT_006(t *testing.T) {
	r := newRig(t,
		callTool("list_dir", `{"path":`),
		answer("gave up"),
	)
	th := r.thread(t, domain.CreateParams{})
	r.send(t, th.ID, "list", "")
	if r.tools.tools["list_dir"].ran.Load() != 0 {
		t.Fatal("a call whose input is not JSON ran")
	}
	calls, _ := r.store.ToolCalls(t.Context(), th.ID)
	if len(calls) != 1 || calls[0].Status != domain.ToolInvalidArgs {
		t.Fatalf("calls %+v, want one invalid_args row", calls)
	}
}

// TestInvalidMetricIncrements_REQ_OBS_002: every invalid tool call counts once in
// umbral_tool_calls_invalid_total, labelled by the model that made it.
func TestInvalidMetricIncrements_REQ_OBS_002(t *testing.T) {
	r := newRig(t,
		callToolOn("ol/small", "read_file", `{}`),
		callToolOn("ol/small", "read_file", `{"path":"a"}`),
		callToolOn("ol/big", "nope", `{}`),
		callToolOn("ol/big", "read_file", `{}`),
	)
	th := r.thread(t, domain.CreateParams{})
	r.send(t, th.ID, "read", "")
	if got := r.metrics.count("ol/small"); got != 1 {
		t.Errorf("ol/small counted %d invalid calls, want 1", got)
	}
	if got := r.metrics.count("ol/big"); got != 2 {
		t.Errorf("ol/big counted %d invalid calls, want 2", got)
	}
}

// TestADaemonFaultIsNotTheModels_REQ_AGT_006: an Action error that is not the model's — an
// environment the daemon built wrong — is an `error`, not invalid_args. It gets no repair,
// does not count against the model, and cannot end the turn with tool_error.
func TestADaemonFaultIsNotTheModels_REQ_AGT_006(t *testing.T) {
	r := newRig(t,
		callToolOn("ol/a", "broken_env", `{"path":"a"}`),
		callToolOn("ol/a", "broken_env", `{"path":"a"}`),
		answer("moved on"),
	)
	th := r.thread(t, domain.CreateParams{})
	_, end := r.send(t, th.ID, "go", "")
	if end.StopReason != domain.StopEndTurn {
		t.Fatalf("stop %s, want end_turn", end.StopReason)
	}
	calls, _ := r.store.ToolCalls(t.Context(), th.ID)
	for _, c := range calls {
		if c.Status != domain.ToolError {
			t.Fatalf("a daemon fault was recorded %s, want error", c.Status)
		}
	}
	if n := r.metrics.count("ol/a"); n != 0 {
		t.Fatalf("a daemon fault counted %d invalid calls against the model", n)
	}
}

// TestEmptyInputIsAnEmptyObject_REQ_AGT_006: a call with no arguments at all is `{}`, as the
// registry reads it, not input that is not JSON.
func TestEmptyInputIsAnEmptyObject_REQ_AGT_006(t *testing.T) {
	r := newRig(t, callTool("list_dir", " "), answer("listed"))
	th := r.thread(t, domain.CreateParams{})
	r.send(t, th.ID, "list", "")
	if r.tools.tools["list_dir"].ran.Load() != 1 {
		t.Fatal("a call with empty arguments did not run")
	}
}

// TestTheRetryStepStopsAtItsInvalidCall_REQ_AGT_006: in the retry step, the calls before the
// invalid one run, the invalid one is recorded, and the calls after it are neither run nor
// recorded: the turn has ended.
func TestTheRetryStepStopsAtItsInvalidCall_REQ_AGT_006(t *testing.T) {
	retry := []llm.Event{
		{Kind: llm.EventToolCall, ToolCall: llm.ToolCall{ID: "p1", Name: "list_dir", Input: `{}`}},
		{Kind: llm.EventToolCall, ToolCall: llm.ToolCall{ID: "p2", Name: "read_file", Input: `{}`}},
		{Kind: llm.EventToolCall, ToolCall: llm.ToolCall{ID: "p3", Name: "read_file", Input: `{"path":"b"}`}},
		{Kind: llm.EventDone, FinishReason: "tool_calls"},
	}
	r := newRig(t, callTool("read_file", `{}`), retry, answer("never reached"))
	th := r.thread(t, domain.CreateParams{})
	_, end := r.send(t, th.ID, "go", "")
	if end.StopReason != domain.StopToolError {
		t.Fatalf("stop %s, want tool_error", end.StopReason)
	}
	if r.tools.tools["list_dir"].ran.Load() != 1 || r.tools.tools["read_file"].ran.Load() != 0 {
		t.Fatalf("list_dir ran %d times and read_file %d, want 1 and 0",
			r.tools.tools["list_dir"].ran.Load(), r.tools.tools["read_file"].ran.Load())
	}
	calls, _ := r.store.ToolCalls(t.Context(), th.ID)
	if len(calls) != 3 {
		t.Fatalf("%d tool_calls rows, want 3: the first invalid call, list_dir and the second invalid call", len(calls))
	}
}

// TestTheRuntimeRequiresMetrics_REQ_OBS_002: the runtime refuses to start without its
// Metrics, so a daemon wired without the counter cannot silently count nothing.
func TestTheRuntimeRequiresMetrics_REQ_OBS_002(t *testing.T) {
	r := newRig(t)
	cfg := r.rt.cfg
	cfg.Metrics = nil
	if _, err := New(t.Context(), cfg); err == nil {
		t.Fatal("New accepted a Config with no Metrics")
	}
}
