package agents

import (
	"errors"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/agents/domain"
	"github.com/ecrespo/umbral/internal/agents/ports"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
)

// requested waits for the turn to pause on an approval.
func (r *rig) requested(t *testing.T) domain.Approval {
	t.Helper()
	select {
	case a := <-r.bus.approvals:
		return a
	case <-time.After(10 * time.Second):
		t.Fatal("no approval was requested")
	}
	return domain.Approval{}
}

func (r *rig) finished(t *testing.T) ports.TurnFinished {
	t.Helper()
	select {
	case end := <-r.bus.ended:
		return end
	case <-time.After(10 * time.Second):
		t.Fatal("the turn did not finish")
	}
	return ports.TurnFinished{}
}

// TestAskPausesTurn_REQ_AGT_004: an `ask` persists the approval, pauses the turn — the thread
// awaiting_approval, the tool not run — and approval.respond resumes it and runs the tool.
func TestAskPausesTurn_REQ_AGT_004(t *testing.T) {
	r := newRig(t, callTool("run", `{"path":"make test"}`), answer("tests pass"))
	th := r.thread(t, domain.CreateParams{})
	if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "run the tests"}); err != nil {
		t.Fatal(err)
	}
	a := r.requested(t)
	if a.Tool != "run" || a.Risk != "Exec" || a.Reason != secdomain.ReasonPolicy || a.Summary != "make test" || a.State != domain.ApprovalPending {
		t.Fatalf("approval %+v", a)
	}
	if got, _ := r.rt.Get(t.Context(), th.ID); got.State != domain.StateAwaitingApproval {
		t.Fatalf("state while paused %s", got.State)
	}
	if r.tools.tools["run"].ran.Load() != 0 {
		t.Fatal("the tool ran before the approval")
	}
	pending, _ := r.rt.Approvals(t.Context(), "", false)
	if len(pending) != 1 || pending[0].ID != a.ID {
		t.Fatalf("approval.list %+v", pending)
	}

	decided, err := r.rt.Respond(t.Context(), domain.Response{ApprovalID: a.ID, Approve: true, Scope: domain.ScopeOnce})
	if err != nil || decided.State != domain.ApprovalApproved || decided.DecidedAt == nil {
		t.Fatalf("respond %+v %v", decided, err)
	}
	if end := r.finished(t); end.StopReason != domain.StopEndTurn {
		t.Fatalf("stop %s", end.StopReason)
	}
	if r.tools.tools["run"].ran.Load() != 1 {
		t.Fatal("the approved tool did not run")
	}
	if pending, _ := r.rt.Approvals(t.Context(), "", false); len(pending) != 0 {
		t.Fatalf("still pending %+v", pending)
	}
	if _, err := r.rt.Respond(t.Context(), domain.Response{ApprovalID: a.ID, Approve: false, Scope: domain.ScopeOnce}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("a second answer: %v", err)
	}
	if _, err := r.rt.Respond(t.Context(), domain.Response{ApprovalID: "apr_nope", Approve: true, Scope: domain.ScopeOnce}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an unknown approval: %v", err)
	}
	if _, err := r.rt.Respond(t.Context(), domain.Response{ApprovalID: a.ID, Approve: true, Scope: "forever"}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("a bad scope: %v", err)
	}
}

// TestDenyReturnsDeniedByUser_REQ_AGT_005: a denial runs nothing, gives the model a
// denied_by_user result and the turn continues.
func TestDenyReturnsDeniedByUser_REQ_AGT_005(t *testing.T) {
	r := newRig(t, callTool("run", `{"path":"make deploy"}`), answer("understood, not deploying"))
	th := r.thread(t, domain.CreateParams{})
	if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "deploy"}); err != nil {
		t.Fatal(err)
	}
	a := r.requested(t)
	if _, err := r.rt.Respond(t.Context(), domain.Response{ApprovalID: a.ID, Approve: false, Scope: domain.ScopeOnce}); err != nil {
		t.Fatal(err)
	}
	if end := r.finished(t); end.StopReason != domain.StopEndTurn {
		t.Fatalf("stop %s", end.StopReason)
	}
	if r.tools.tools["run"].ran.Load() != 0 {
		t.Fatal("a denied tool ran")
	}
	calls, _ := r.store.ToolCalls(t.Context(), th.ID)
	if calls[0].Status != domain.ToolDeniedByUser {
		t.Fatalf("status %s", calls[0].Status)
	}
	reqs := r.models.requests()
	last := reqs[len(reqs)-1].Request.Messages
	if tm := last[len(last)-1]; tm.ToolCallID != calls[0].ID || !tm.ToolError {
		t.Fatalf("the model did not read the refusal: %+v", tm)
	}
}

// TestAThreadScopeRemembersTheDecision_REQ_AGT_004: `thread` persists a rule for this thread,
// so the same call is not asked again — in this turn or the next.
func TestAThreadScopeRemembersTheDecision_REQ_AGT_004(t *testing.T) {
	r := newRig(t, callTool("run", `{"path":"make test"}`), callTool("run", `{"path":"make test"}`), answer("done"))
	th := r.thread(t, domain.CreateParams{})
	if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "test twice"}); err != nil {
		t.Fatal(err)
	}
	a := r.requested(t)
	if _, err := r.rt.Respond(t.Context(), domain.Response{ApprovalID: a.ID, Approve: true, Scope: domain.ScopeThread}); err != nil {
		t.Fatal(err)
	}
	if end := r.finished(t); end.StopReason != domain.StopEndTurn {
		t.Fatalf("stop %s", end.StopReason)
	}
	if n := r.tools.tools["run"].ran.Load(); n != 2 {
		t.Fatalf("ran %d times, want 2 with one approval", n)
	}
	rules, _ := r.store.Rules(t.Context(), th.ID)
	if len(rules) != 1 || rules[0].ThreadID != th.ID || rules[0].Pattern != "make test" || rules[0].Decision != secdomain.VerdictAllow {
		t.Fatalf("rules %+v", rules)
	}
	if all, _ := r.rt.Approvals(t.Context(), th.ID, true); len(all) != 1 {
		t.Fatalf("approvals %+v", all)
	}
}

// TestDestructiveIgnoresAlways_REQ_SEC_005: `always` on a destructive command is recorded as
// `once` and no rule is persisted, so the next one asks again.
func TestDestructiveIgnoresAlways_REQ_SEC_005(t *testing.T) {
	r := newRig(t, callTool("run", `{"path":"rm -rf build"}`), answer("removed"))
	th := r.thread(t, domain.CreateParams{Mode: secdomain.ModeAutoEdit})
	if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "clean"}); err != nil {
		t.Fatal(err)
	}
	a := r.requested(t)
	if a.Reason != secdomain.ReasonDestructive {
		t.Fatalf("reason %s", a.Reason)
	}
	decided, err := r.rt.Respond(t.Context(), domain.Response{ApprovalID: a.ID, Approve: true, Scope: domain.ScopeAlways})
	if err != nil || decided.Scope != domain.ScopeOnce {
		t.Fatalf("respond %+v %v", decided, err)
	}
	r.finished(t)
	if rules, _ := r.store.Rules(t.Context(), th.ID); len(rules) != 0 {
		t.Fatalf("a destructive command was remembered: %+v", rules)
	}
}

// TestAGlobInTheTargetIsNotRemembered_REQ_AGT_004: a target holding `*` would, as a rule,
// allow more than the call the user saw; the decision applies once.
func TestAGlobInTheTargetIsNotRemembered_REQ_AGT_004(t *testing.T) {
	r := newRig(t, callTool("run", `{"path":"cat *.log"}`), answer("ok"))
	th := r.thread(t, domain.CreateParams{})
	if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "logs"}); err != nil {
		t.Fatal(err)
	}
	a := r.requested(t)
	decided, err := r.rt.Respond(t.Context(), domain.Response{ApprovalID: a.ID, Approve: true, Scope: domain.ScopeAlways})
	if err != nil || decided.Scope != domain.ScopeOnce {
		t.Fatalf("respond %+v %v", decided, err)
	}
	r.finished(t)
	if rules, _ := r.store.Rules(t.Context(), th.ID); len(rules) != 0 {
		t.Fatalf("remembered %+v", rules)
	}
}

// TestACancelledTurnExpiresItsApproval_REQ_AGT_004: a turn stopped while it waits leaves the
// approval expired, and a late answer is CONFLICT.
func TestACancelledTurnExpiresItsApproval_REQ_AGT_004(t *testing.T) {
	r := newRig(t, callTool("run", `{"path":"make"}`))
	th := r.thread(t, domain.CreateParams{})
	if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "build"}); err != nil {
		t.Fatal(err)
	}
	a := r.requested(t)
	r.rt.Close()
	got, _ := r.store.Approval(t.Context(), a.ID)
	if got.State != domain.ApprovalExpired {
		t.Fatalf("state %s", got.State)
	}
	if _, err := r.rt.Respond(t.Context(), domain.Response{ApprovalID: a.ID, Approve: true, Scope: domain.ScopeOnce}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("a late answer: %v", err)
	}
}

// TestARememberedDenyAppliesToTheRestOfTheTurn_REQ_AGT_005: a `thread` deny is a rule at once:
// the same call later in the turn is refused by it, without asking again.
func TestARememberedDenyAppliesToTheRestOfTheTurn_REQ_AGT_005(t *testing.T) {
	r := newRig(t, callTool("run", `{"path":"make deploy"}`), callTool("run", `{"path":"make deploy"}`), answer("ok"))
	th := r.thread(t, domain.CreateParams{})
	if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "deploy"}); err != nil {
		t.Fatal(err)
	}
	a := r.requested(t)
	if _, err := r.rt.Respond(t.Context(), domain.Response{ApprovalID: a.ID, Approve: false, Scope: domain.ScopeThread}); err != nil {
		t.Fatal(err)
	}
	r.finished(t)
	calls, _ := r.store.ToolCalls(t.Context(), th.ID)
	if len(calls) != 2 || calls[0].Status != domain.ToolDeniedByUser || calls[1].Status != domain.ToolDeniedByPolicy {
		t.Fatalf("calls %+v", calls)
	}
	if all, _ := r.rt.Approvals(t.Context(), th.ID, true); len(all) != 1 {
		t.Fatalf("asked again: %+v", all)
	}
}

// TestACompoundLineIsNotRemembered_REQ_AGT_004: the rules read a line one command at a time,
// so a rule on a whole compound line would never match again; the decision applies once.
func TestACompoundLineIsNotRemembered_REQ_AGT_004(t *testing.T) {
	r := newRig(t, callTool("run", `{"path":"go build && go test"}`), answer("ok"))
	th := r.thread(t, domain.CreateParams{})
	if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "check"}); err != nil {
		t.Fatal(err)
	}
	a := r.requested(t)
	decided, err := r.rt.Respond(t.Context(), domain.Response{ApprovalID: a.ID, Approve: true, Scope: domain.ScopeThread})
	if err != nil || decided.Scope != domain.ScopeOnce {
		t.Fatalf("respond %+v %v", decided, err)
	}
	r.finished(t)
	if rules, _ := r.store.Rules(t.Context(), th.ID); len(rules) != 0 {
		t.Fatalf("remembered %+v", rules)
	}
}

// TestAnAnswerRacingACancelIsACancel_REQ_AGT_004: an approval answered as the turn is cancelled
// does not run the tool. The answer and the cancel both land before the turn waits, so its
// select has both ready; it is run several times because a select picks among them at random.
func TestAnAnswerRacingACancelIsACancel_REQ_AGT_004(t *testing.T) {
	for range 16 {
		r := newRig(t, callTool("run", `{"path":"make"}`))
		th := r.thread(t, domain.CreateParams{})
		r.bus.onApproval = func(a domain.Approval) {
			if _, err := r.rt.Respond(t.Context(), domain.Response{ApprovalID: a.ID, Approve: true, Scope: domain.ScopeOnce}); err != nil {
				t.Error(err)
			}
			r.rt.stop()
		}
		if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "build"}); err != nil {
			t.Fatal(err)
		}
		if end := r.finished(t); end.StopReason != domain.StopCancelled {
			t.Fatalf("stop reason %s", end.StopReason)
		}
		if n := r.tools.tools["run"].ran.Load(); n != 0 {
			t.Fatalf("the tool ran %d times after the cancel", n)
		}
	}
}
