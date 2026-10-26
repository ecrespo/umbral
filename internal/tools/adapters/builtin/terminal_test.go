package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	secdomain "github.com/ecrespo/umbral/internal/security/domain"
	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/tools/adapters/registry"
	"github.com/ecrespo/umbral/internal/tools/domain"
)

// fakeKeys is a thread's PTY that keeps the prompt's line the way the sessions module does.
type fakeKeys struct {
	state  sessdomain.InteractState
	sent   [][]sessdomain.Key
	expect []sessdomain.InteractState
	screen sessdomain.Screen
	cond   sessdomain.WaitCondition
	err    error
}

func (f *fakeKeys) InteractState(string, string) sessdomain.InteractState { return f.state }

func (f *fakeKeys) SendKeys(_ context.Context, _, _ string, keys []sessdomain.Key, expect sessdomain.InteractState) (sessdomain.Plan, error) {
	if f.err != nil {
		return sessdomain.Plan{}, f.err
	}
	plan, err := sessdomain.PlanKeys(f.state, keys)
	if err != nil {
		return plan, err
	}
	f.sent, f.expect = append(f.sent, keys), append(f.expect, expect)
	if f.state.AtPrompt {
		f.state.Line = plan.Line
		if plan.Submit {
			f.state = sessdomain.InteractState{}
		}
	} else {
		f.state.Program = plan.Program
	}
	return plan, nil
}

func (f *fakeKeys) ReadScreen(context.Context, string, string) (sessdomain.Screen, error) {
	return f.screen, f.err
}

func (f *fakeKeys) WaitFor(_ context.Context, _, _ string, cond sessdomain.WaitCondition) (sessdomain.WaitOutcome, error) {
	f.cond = cond
	return sessdomain.WaitOutcome{Reason: sessdomain.WaitMatched, Screen: f.screen}, f.err
}

func keysInput(keys ...map[string]any) json.RawMessage {
	list := make([]any, len(keys))
	for i, k := range keys {
		list[i] = k
	}
	return in(map[string]any{"action": "send_keys", "keys": list})
}

// decideCall takes one call through the registry and the policy as a turn does, in normal mode
// with an allow rule for the tool, and runs it when the policy allows it.
func decideCall(t *testing.T, reg *registry.Registry, input json.RawMessage) (secdomain.Action, secdomain.Decision, error) {
	t.Helper()
	env := domain.Env{ThreadID: "thr_1", Cwd: "/work", WriteRoot: "/work"}
	call := domain.Call{Tool: "terminal_interact", Input: input}
	action, err := reg.Action(env, call)
	if err != nil {
		return action, secdomain.Decision{}, err
	}
	rules := []secdomain.Rule{{Tool: "terminal_interact", Pattern: "*", Decision: secdomain.VerdictAllow}}
	d := secdomain.Decide(action, secdomain.ModeNormal, rules, false)
	if d.Verdict == secdomain.VerdictAllow {
		_, err = reg.Invoke(context.Background(), env, call, domain.Grant{Action: action, Decision: d})
	}
	return action, d, err
}

// TestAssembledLineAsksOnEnter_REQ_FTU_003: the line typed at the prompt is kept across calls,
// and the enter that submits it is decided on the whole line exactly as run_command's — `rm -`
// in one call and `rf /` with enter in the next ask as destructive, an allow rule for the tool
// notwithstanding. Editing keys at the prompt are refused as invalid input; a program's input
// is matched as text; read_screen and wait_for are ReadOnly (delta `2026-10-terminal-interact`,
// decisions 3 and 4).
func TestAssembledLineAsksOnEnter_REQ_FTU_003(t *testing.T) {
	t.Parallel()
	term := &fakeKeys{state: sessdomain.InteractState{AtPrompt: true}}
	reg, err := registry.New(terminalInteract{keys: term})
	if err != nil {
		t.Fatal(err)
	}

	action, d, err := decideCall(t, reg, keysInput(map[string]any{"text": "rm -"}))
	if err != nil || d.Verdict != secdomain.VerdictAllow || action.Risk != secdomain.RiskExec || !action.ProgramInput {
		t.Fatalf("typing at the prompt: %+v %s %v", action, d.Verdict, err)
	}
	action, d, err = decideCall(t, reg, keysInput(map[string]any{"text": "rf /"}, map[string]any{"key": "enter"}))
	if err != nil || action.Target != "rm -rf /" || action.ProgramInput || d.Verdict != secdomain.VerdictAsk || d.Reason != secdomain.ReasonDestructive {
		t.Errorf("the enter: target %q program %v, %s %q, %v", action.Target, action.ProgramInput, d.Verdict, d.Reason, err)
	}
	if len(term.sent) != 1 {
		t.Errorf("the destructive line was sent without an approval: %d sends", len(term.sent))
	}

	if _, _, err := decideCall(t, reg, keysInput(map[string]any{"key": "tab"})); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("tab at the prompt: %v, want invalid input", err)
	}
	for _, bad := range []json.RawMessage{
		keysInput(map[string]any{"text": "a\x1b[A"}),
		keysInput(map[string]any{"text": "a", "key": "enter"}),
		keysInput(map[string]any{"key": "q"}),
		keysInput(),
		in(map[string]any{"action": "wait_for"}),
		in(map[string]any{"action": "wait_for", "regex": "(", "timeout_ms": 10}),
		in(map[string]any{"action": "wait_for", "exit": true, "idle_ms": 200}),
	} {
		if _, _, err := decideCall(t, reg, bad); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("%s: %v, want invalid input", bad, err)
		}
	}

	// A program in the foreground: its input is matched as text, the shell parser left out.
	term.state = sessdomain.InteractState{}
	action, d, err = decideCall(t, reg, keysInput(map[string]any{"text": "SELECT * FROM <t>;"}, map[string]any{"key": "enter"}, map[string]any{"key": "c", "ctrl": true}))
	if err != nil || action.Target != "SELECT * FROM <<t>;<enter><ctrl+c>" || !action.ProgramInput || d.Verdict != secdomain.VerdictAllow {
		t.Errorf("program input: %q %v %s %v", action.Target, action.ProgramInput, d.Verdict, err)
	}
	if _, d, _ := decideCall(t, reg, keysInput(map[string]any{"text": "DROP TABLE users;"}, map[string]any{"key": "enter"})); d.Reason != secdomain.ReasonDestructive {
		t.Errorf("a DROP to psql: %s %q, want destructive", d.Verdict, d.Reason)
	}
	// A program's line is kept across calls too: keys a program leaves unread reach the shell
	// when it ends, so `rm -` then `rf ~` and enter ask.
	if _, d, err := decideCall(t, reg, keysInput(map[string]any{"text": "rm -"})); err != nil || d.Verdict != secdomain.VerdictAllow || term.state.Program != "rm -" {
		t.Fatalf("half a line to a program: %s %v, program %q", d.Verdict, err, term.state.Program)
	}
	action, d, _ = decideCall(t, reg, keysInput(map[string]any{"text": "rf ~"}, map[string]any{"key": "enter"}))
	if action.Target != "rm -rf ~<enter>" || d.Reason != secdomain.ReasonDestructive {
		t.Errorf("the rest of it: target %q, %s %q, want destructive", action.Target, d.Verdict, d.Reason)
	}
	term.state.Program = ""

	for _, read := range []json.RawMessage{
		in(map[string]any{"action": "read_screen"}),
		in(map[string]any{"action": "wait_for", "regex": "ready", "timeout_ms": 5000}),
	} {
		action, d, err := decideCall(t, reg, read)
		if err != nil || action.Risk != secdomain.RiskReadOnly || d.Verdict != secdomain.VerdictAllow {
			t.Errorf("%s: %s %s %v", read, action.Risk, d.Verdict, err)
		}
	}
	if term.cond.Regex == nil || term.cond.Regex.String() != "ready" || term.cond.Timeout != 5*time.Second {
		t.Errorf("wait_for passed %+v", term.cond)
	}
	if specs := reg.Specs(); specs[0].Risk != secdomain.RiskExec {
		t.Errorf("the tool is listed as %s, want Exec", specs[0].Risk)
	}
}

// TestTakenOverIsAResult_REQ_FTU_002: while the user holds the terminal every call reads
// `taken_over` and sends nothing; the call is not something to approve.
func TestTakenOverIsAResult_REQ_FTU_002(t *testing.T) {
	t.Parallel()
	term := &fakeKeys{state: sessdomain.InteractState{TakenOver: true}, err: sessdomain.ErrTakenOver}
	reg, err := registry.New(terminalInteract{keys: term})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []json.RawMessage{
		keysInput(map[string]any{"text": "ls"}, map[string]any{"key": "enter"}),
		in(map[string]any{"action": "read_screen"}),
		in(map[string]any{"action": "wait_for", "exit": true}),
	} {
		env := domain.Env{ThreadID: "thr_1", Cwd: "/work", WriteRoot: "/work"}
		call := domain.Call{Tool: "terminal_interact", Input: input}
		action, err := reg.Action(env, call)
		if err != nil {
			t.Fatal(err)
		}
		d := secdomain.Decide(action, secdomain.ModeNormal, nil, false)
		if d.Verdict != secdomain.VerdictAllow {
			t.Errorf("%s while taken over asks: %s", input, d.Verdict)
			continue
		}
		res, err := reg.Invoke(context.Background(), env, call, domain.Grant{Action: action, Decision: d})
		if err != nil || res.Summary != "taken_over" || !strings.HasPrefix(res.Text, "taken_over") {
			t.Errorf("%s: %+v, %v", input, res, err)
		}
	}
	if len(term.sent) != 0 {
		t.Error("keys were sent while the user held the terminal")
	}
}

// TestADecisionWhileTakenOverSendsNothing_REQ_FTU_002: a call decided while the user held the
// terminal was decided as a read; handed back before it runs, it still sends nothing. A read
// before the thread has a terminal is the result no_terminal.
func TestADecisionWhileTakenOverSendsNothing_REQ_FTU_002(t *testing.T) {
	t.Parallel()
	term := &fakeKeys{state: sessdomain.InteractState{TakenOver: true}}
	reg, err := registry.New(terminalInteract{keys: term})
	if err != nil {
		t.Fatal(err)
	}
	env := domain.Env{ThreadID: "thr_1", Cwd: "/work", WriteRoot: "/work"}
	call := domain.Call{Tool: "terminal_interact", Input: keysInput(map[string]any{"text": "taken_over"})}
	action, err := reg.Action(env, call)
	if err != nil {
		t.Fatal(err)
	}
	d := secdomain.Decide(action, secdomain.ModeNormal, nil, false)
	term.state = sessdomain.InteractState{AtPrompt: true}
	env.Target, env.ProgramInput = action.Target, action.ProgramInput
	res, err := terminalInteract{keys: term}.Run(context.Background(), env, call.Input)
	if err != nil || res.Summary != "taken_over" || len(term.sent) != 0 {
		t.Errorf("a call decided while taken over, run after the hand-back: %+v %v, %d sends (%s)", res, err, len(term.sent), d.Verdict)
	}

	term.err = sessdomain.ErrNoTerminal
	res, err = reg.Invoke(context.Background(), env, domain.Call{Tool: "terminal_interact", Input: in(map[string]any{"action": "read_screen"})},
		domain.Grant{Action: secdomain.Action{ThreadID: "thr_1", Tool: "terminal_interact", Risk: secdomain.RiskReadOnly, Target: "read_screen", Cwd: "/work", WriteRoot: "/work"}, Decision: secdomain.Decision{Verdict: secdomain.VerdictAllow}})
	if err != nil || res.Summary != "no_terminal" {
		t.Errorf("a read with no terminal: %+v, %v", res, err)
	}
}
