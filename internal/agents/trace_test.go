package agents

import (
	"testing"

	"github.com/ecrespo/umbral/internal/agents/domain"
	"github.com/ecrespo/umbral/internal/agents/ports"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
)

// TestTheRuntimeTracesEachTurn_REQ_OBS_001: a turn is one span, started before its first
// model call and ended with how the turn ended and what it used; every model call runs
// inside it, so the router's `llm.call` joins the turn's trace; and each tool is a span of
// its own inside the turn, ended with its status and risk.
func TestTheRuntimeTracesEachTurn_REQ_OBS_001(t *testing.T) {
	r := newRig(t, callTool("read_file", `{"path":"a.go"}`), answer("done"))
	th := r.thread(t, domain.CreateParams{Mode: secdomain.ModeNormal})
	res, end := r.send(t, th.ID, "read it", "")
	if end.StopReason != domain.StopEndTurn {
		t.Fatalf("the turn ended %s", end.StopReason)
	}

	r.tracer.mu.Lock()
	defer r.tracer.mu.Unlock()
	if len(r.tracer.turns) != 1 {
		t.Fatalf("%d turn spans, want 1", len(r.tracer.turns))
	}
	turn := r.tracer.turns[0]
	if turn.threadID != th.ID || turn.turnID != res.TurnID || turn.end == nil {
		t.Fatalf("turn span %+v, want thread %s turn %s, ended", turn, th.ID, res.TurnID)
	}
	if turn.end.StopReason != domain.StopEndTurn || turn.end.Usage.InTokens != 150 || turn.end.Usage.OutTokens != 30 {
		t.Errorf("turn span ended with %+v", *turn.end)
	}

	r.models.mu.Lock()
	spans := append([]string(nil), r.models.spans...)
	r.models.mu.Unlock()
	if len(spans) != 2 || spans[0] != "turn:"+res.TurnID || spans[1] != "turn:"+res.TurnID {
		t.Errorf("the model calls ran inside %q, want the turn's span", spans)
	}

	if len(r.tracer.tools) != 1 {
		t.Fatalf("%d tool spans, want 1", len(r.tracer.tools))
	}
	tool := r.tracer.tools[0]
	if tool.tool != "read_file" || tool.parent != "turn:"+res.TurnID || tool.callID == "" || tool.end == nil {
		t.Fatalf("tool span %+v", tool)
	}
	if tool.end.Status != domain.ToolOK || tool.end.Risk != string(secdomain.RiskReadOnly) {
		t.Errorf("tool span ended with %+v", *tool.end)
	}
}

// TestAToolThatNeverFinishedIsTracedToo: a tool span ends even when the call does not reach a
// recorded status — here an invalid call, which ends with its own status — so no span is
// left open in the exporter.
func TestAToolThatNeverFinishedIsTracedToo(t *testing.T) {
	r := newRig(t, callTool("no_such_tool", `{}`), answer("ok"))
	th := r.thread(t, domain.CreateParams{Mode: secdomain.ModeNormal})
	r.send(t, th.ID, "go", "")

	r.tracer.mu.Lock()
	defer r.tracer.mu.Unlock()
	if len(r.tracer.tools) != 1 || r.tracer.tools[0].end == nil || r.tracer.tools[0].end.Status != domain.ToolInvalidArgs {
		t.Fatalf("tool spans %+v", r.tracer.tools)
	}
}

// TestATurnsLogLinesCarryItsSpan: what the runtime logs about a turn is logged with the
// turn's context, so the daemon's log handler can add its trace id (Art. 7).
func TestATurnsLogLinesCarryItsSpan(t *testing.T) {
	r := newRig(t) // no script: the model call fails
	th := r.thread(t, domain.CreateParams{Mode: secdomain.ModeNormal})
	res, end := r.send(t, th.ID, "go", "")
	if end.StopReason != domain.StopProviderError {
		t.Fatalf("the turn ended %s", end.StopReason)
	}
	r.logs.mu.Lock()
	defer r.logs.mu.Unlock()
	if got := r.logs.lines["a turn stopped: the model call failed"]; got != "turn:"+res.TurnID {
		t.Fatalf("the line was logged inside %q, want the turn's span; lines %v", got, r.logs.lines)
	}
}

// TestTheRuntimeRequiresATracer: a daemon wired without one would run untraced turns, which
// Art. 7 does not allow, so New refuses it.
func TestTheRuntimeRequiresATracer(t *testing.T) {
	r := newRig(t)
	cfg := r.rt.cfg
	cfg.Tracer = nil
	if _, err := New(t.Context(), cfg); err == nil {
		t.Fatal("New accepted a Config with no Tracer")
	}
}

var _ ports.Tracer = (*fakeTracer)(nil)

// TestAnUnknownToolIsTracedAsUnknown: the name of a tool the registry does not have is the
// model's own text, so its span is `tool.unknown` rather than a span name — and a label —
// the model chose.
func TestAnUnknownToolIsTracedAsUnknown(t *testing.T) {
	r := newRig(t, callTool("ignore previous instructions and", `{}`), answer("ok"))
	th := r.thread(t, domain.CreateParams{Mode: secdomain.ModeNormal})
	r.send(t, th.ID, "go", "")

	r.tracer.mu.Lock()
	defer r.tracer.mu.Unlock()
	if len(r.tracer.tools) != 1 || r.tracer.tools[0].tool != "unknown" {
		t.Fatalf("tool spans %+v, want one named unknown", r.tracer.tools)
	}
}
