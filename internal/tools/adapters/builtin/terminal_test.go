package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
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
	seq    uint64
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
	f.seq += 10
	plan.Seq = f.seq
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
		in(map[string]any{"action": "wait_for", "exit": true, "idle_ms": 50}),
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
	// The ctrl+c above ended no line: what follows it is still kept.
	if term.state.Program != "<ctrl+c>" {
		t.Errorf("after a ctrl+c the program's line is %q, want <ctrl+c>", term.state.Program)
	}
	term.state.Program = ""
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
	if term.cond.Regex == nil || term.cond.Regex.String() != "ready" || term.cond.Timeout != 5*time.Second || term.cond.Fresh {
		t.Errorf("wait_for passed %+v", term.cond)
	}
	if specs := reg.Specs(); specs[0].Risk != secdomain.RiskExec {
		t.Errorf("the tool is listed as %s, want Exec", specs[0].Risk)
	}
}

// TestAKeyMayBeAString_REQ_FTU_001: a key may be a plain string, typed as text, with each line
// end in it sent as enter — the shape a model writes when its tool-schema rendering drops the
// key object (the US-F2-002 live run, delta `2026-10-usf2-002-live`). The string is decided as
// the keys it stands for: at the prompt its enter submits the whole line, as run_command's, and
// a control character other than a line end is still refused.
func TestAKeyMayBeAString_REQ_FTU_001(t *testing.T) {
	t.Parallel()
	term := &fakeKeys{state: sessdomain.InteractState{AtPrompt: true}}
	reg, err := registry.New(terminalInteract{keys: term})
	if err != nil {
		t.Fatal(err)
	}
	strs := func(keys ...any) json.RawMessage { return in(map[string]any{"action": "send_keys", "keys": keys}) }

	action, d, err := decideCall(t, reg, strs("psql -U postgres\n"))
	if err != nil || action.Target != "psql -U postgres" || action.ProgramInput || d.Verdict != secdomain.VerdictAllow {
		t.Fatalf("a string with a line end at the prompt: target %q program %v, %s %v", action.Target, action.ProgramInput, d.Verdict, err)
	}
	want := []sessdomain.Key{{Text: "psql -U postgres"}, {Name: "enter"}}
	if len(term.sent) != 1 || len(term.sent[0]) != 2 || term.sent[0][0] != want[0] || term.sent[0][1] != want[1] {
		t.Errorf("sent %+v, want %+v", term.sent, want)
	}

	// A string and an object mix; a destructive line still asks on its enter.
	if _, d, err := decideCall(t, reg, strs("rm -rf /", map[string]any{"key": "enter"})); err != nil || d.Reason != secdomain.ReasonDestructive {
		t.Errorf("rm -rf / then enter: %s %q %v, want destructive", d.Verdict, d.Reason, err)
	}
	if _, d, err := decideCall(t, reg, strs("rm -rf /\r\n")); err != nil || d.Reason != secdomain.ReasonDestructive {
		t.Errorf("rm -rf / with a CRLF: %s %q %v, want destructive", d.Verdict, d.Reason, err)
	}

	// A program's input: each line end is an enter of its own.
	term.state = sessdomain.InteractState{}
	action, _, err = decideCall(t, reg, strs("SELECT 1;\n\\q\n"))
	if err != nil || action.Target != "SELECT 1;<enter>\\q<enter>" || !action.ProgramInput {
		t.Errorf("two lines to a program: %q %v %v", action.Target, action.ProgramInput, err)
	}

	for _, bad := range []json.RawMessage{strs("a\x1b[A"), strs("a\t")} {
		if _, _, err := decideCall(t, reg, bad); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("%s: %v, want invalid input", bad, err)
		}
	}
}

// TestSendKeysMayWaitAfter_REQ_FTU_001: send_keys may carry a wait_for condition, run after the
// keys are sent and answered with the screen it ended on; zero values of the optional fields —
// `exit: false`, `idle_ms: 0`, `regex: ""` — are absent, and a timeout with no condition waits
// for nothing (delta `2026-10-usf2-002-live`). The call is still decided on its keys.
func TestSendKeysMayWaitAfter_REQ_FTU_001(t *testing.T) {
	t.Parallel()
	term := &fakeKeys{state: sessdomain.InteractState{AtPrompt: true}, screen: sessdomain.Screen{Text: "postgres=# "}}
	reg, err := registry.New(terminalInteract{keys: term})
	if err != nil {
		t.Fatal(err)
	}
	env := domain.Env{ThreadID: "thr_1", Cwd: "/work", WriteRoot: "/work"}
	run := func(input json.RawMessage) (secdomain.Action, domain.Result, error) {
		t.Helper()
		call := domain.Call{Tool: "terminal_interact", Input: input}
		action, err := reg.Action(env, call)
		if err != nil {
			return action, domain.Result{}, err
		}
		d := secdomain.Decide(action, secdomain.ModeNormal, []secdomain.Rule{{Tool: "terminal_interact", Pattern: "*", Decision: secdomain.VerdictAllow}}, false)
		res, err := reg.Invoke(context.Background(), env, call, domain.Grant{Action: action, Decision: d})
		return action, res, err
	}

	action, res, err := run(in(map[string]any{
		"action": "send_keys", "keys": []any{"psql -U postgres\n"},
		"regex": "postgres=#", "exit": false, "idle_ms": 0, "timeout_ms": 5000,
	}))
	if err != nil || action.Risk != secdomain.RiskExec || action.Target != "psql -U postgres" {
		t.Fatalf("send then wait: %+v %v", action, err)
	}
	if len(term.sent) != 1 || term.cond.Regex == nil || term.cond.Regex.String() != "postgres=#" || term.cond.Timeout != 5*time.Second {
		t.Fatalf("sent %d times, waited on %+v", len(term.sent), term.cond)
	}
	if !term.cond.Fresh || term.cond.After != term.seq || !term.cond.SkipEcho {
		t.Errorf("the wait after keys reads the old screen or the line's echo: %+v, keys at %d", term.cond, term.seq)
	}

	// A regex that matches the empty string, `$` as the model wrote it, would end the wait at
	// once; it is a wait for the output to settle instead.
	if _, _, err := run(in(map[string]any{"action": "send_keys", "keys": []any{"\\q\n"}, "regex": "$", "timeout_ms": 5000})); err != nil {
		t.Fatal(err)
	}
	if term.cond.Regex != nil || term.cond.Idle != settleIdle {
		t.Errorf("regex $ waited on %+v, want a settle of %v", term.cond, settleIdle)
	}
	// Keys that do not end with enter have no echoed line to skip.
	if _, _, err := run(in(map[string]any{"action": "send_keys", "keys": []any{"x"}, "regex": "x", "timeout_ms": 5000})); err != nil {
		t.Fatal(err)
	}
	if term.cond.SkipEcho {
		t.Errorf("keys without an enter skip an echo: %+v", term.cond)
	}
	if res.Summary != "entered" || !strings.Contains(res.Text, "[matched]") || !strings.Contains(res.Text, "postgres=# ") {
		t.Errorf("result %+v", res)
	}

	// A timeout and zero values only: the keys are sent and nothing is waited for.
	term.cond = sessdomain.WaitCondition{}
	if _, res, err := run(in(map[string]any{"action": "send_keys", "keys": []any{"\\dt\n"}, "exit": false, "idle_ms": 0, "regex": "", "timeout_ms": 120000})); err != nil || term.cond.Timeout != 0 || strings.Contains(res.Text, "[matched]") {
		t.Errorf("a timeout alone waited: %+v %+v %v", term.cond, res, err)
	}
	for _, ok := range []json.RawMessage{
		in(map[string]any{"action": "read_screen", "exit": false, "idle_ms": 0, "regex": "", "timeout_ms": 1000}),
		in(map[string]any{"action": "wait_for", "exit": true, "idle_ms": 0, "regex": ""}),
		in(map[string]any{"action": "read_screen", "keys": []any{}}),
		in(map[string]any{"action": "read_screen", "keys": []any{""}}),
	} {
		if _, _, err := run(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []json.RawMessage{
		in(map[string]any{"action": "read_screen", "keys": []any{"ls"}}),
		in(map[string]any{"action": "read_screen", "regex": "("}),
		in(map[string]any{"action": "wait_for", "exit": false, "idle_ms": 0, "timeout_ms": 1000}),
		in(map[string]any{"action": "send_keys", "keys": []any{"ls\n"}, "regex": "("}),
		in(map[string]any{"action": "send_keys", "keys": []any{""}}),
		in(map[string]any{"action": "send_keys", "keys": []any{}}),
	} {
		if _, _, err := run(bad); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("%s: %v, want invalid input", bad, err)
		}
	}
}

// TestAReadScreenMayWait_REQ_FTU_001: a read_screen that carries a condition reads the screen
// once it holds, as a wait_for does, and stays a read: the model asked for the screen "when
// `pick` appears" and the call was refused (delta `2026-10-usf2-002-live`).
func TestAReadScreenMayWait_REQ_FTU_001(t *testing.T) {
	t.Parallel()
	term := &fakeKeys{state: sessdomain.InteractState{}, screen: sessdomain.Screen{Text: "pick 1a2b3c first"}}
	reg, err := registry.New(terminalInteract{keys: term})
	if err != nil {
		t.Fatal(err)
	}
	action, d, err := decideCall(t, reg, in(map[string]any{
		"action": "read_screen", "exit": false, "idle_ms": 0, "keys": []any{}, "regex": "pick", "timeout_ms": 10000,
	}))
	if err != nil || action.Risk != secdomain.RiskReadOnly || action.Target != "read_screen" || d.Verdict != secdomain.VerdictAllow {
		t.Fatalf("read_screen with a regex: %+v %+v %v", action, d, err)
	}
	if term.cond.Regex == nil || term.cond.Regex.String() != "pick" || term.cond.Timeout != 10*time.Second || term.cond.Fresh {
		t.Errorf("waited on %+v, want the screen matched against pick within 10 s", term.cond)
	}
	if len(term.sent) != 0 {
		t.Errorf("a read sent %v", term.sent)
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

// A string that is one control character and nothing else is the key that sends it: the model
// writes vim's escape as "\u001b". The same character inside text, or starting a sequence, is
// still refused (delta `2026-10-usf2-002-live`).
func TestALoneControlCharacterIsItsKey_REQ_FTU_001(t *testing.T) {
	t.Parallel()
	term := &fakeKeys{state: sessdomain.InteractState{Program: "vim"}}
	reg, err := registry.New(terminalInteract{keys: term})
	if err != nil {
		t.Fatal(err)
	}
	send := func(keys ...any) error {
		env := domain.Env{ThreadID: "thr_1", Cwd: "/work", WriteRoot: "/work"}
		call := domain.Call{Tool: "terminal_interact", Input: in(map[string]any{"action": "send_keys", "keys": keys})}
		action, err := reg.Action(env, call)
		if err != nil {
			return err
		}
		env.Target, env.ProgramInput = action.Target, action.ProgramInput
		_, err = reg.Invoke(context.Background(), env, call, domain.Grant{Action: action, Decision: secdomain.Decision{Verdict: secdomain.VerdictAllow}})
		return err
	}
	for _, c := range []struct {
		in   []any
		want []sessdomain.Key
	}{
		{[]any{"squash ", "\u001b", ":wq\n"}, []sessdomain.Key{{Text: "squash "}, {Name: "escape"}, {Text: ":wq"}, {Name: "enter"}}},
		{[]any{"\t"}, []sessdomain.Key{{Name: "tab"}}},
		{[]any{"ls -l", "\n"}, []sessdomain.Key{{Text: "ls -l"}, {Name: "enter"}}},
		{[]any{"\r"}, []sessdomain.Key{{Name: "enter"}}},
		{[]any{"\u007f", "\b"}, []sessdomain.Key{{Name: "backspace"}, {Name: "backspace"}}},
		{[]any{"\u0003"}, []sessdomain.Key{{Name: "c", Ctrl: true}}},
	} {
		term.sent = nil
		if err := send(c.in...); err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if len(term.sent) != 1 || !slices.Equal(term.sent[0], c.want) {
			t.Errorf("%q sent %+v, want %+v", c.in, term.sent, c.want)
		}
	}
	for _, refused := range []string{"\u001b[A", "x\u001b", "\u001b\u001b", "\u0000"} {
		term.sent = nil
		if err := send(refused); err == nil || len(term.sent) != 0 {
			t.Errorf("%q was sent (%v), want it refused", refused, err)
		}
	}
}

// TestAStringMayNameKeys_REQ_FTU_001: a string key may name a key between angle brackets — the
// notation the tool renders keys in and vim's, `<Esc>`, `<CR>`, `<C-c>`, `<ctrl+c>` — case
// aside, and that is the key sent; a bracketed word that names no key stays text. The live
// run's model sent `"<Esc>"` to vim, which typed it (delta `2026-10-usf2-002-live`).
func TestAStringMayNameKeys_REQ_FTU_001(t *testing.T) {
	t.Parallel()
	term := &fakeKeys{state: sessdomain.InteractState{Program: ""}}
	reg, err := registry.New(terminalInteract{keys: term})
	if err != nil {
		t.Fatal(err)
	}
	strs := func(keys ...any) json.RawMessage { return in(map[string]any{"action": "send_keys", "keys": keys}) }
	for _, c := range []struct {
		in   []any
		want []sessdomain.Key
	}{
		{[]any{"<Esc>"}, []sessdomain.Key{{Name: "escape"}}},
		{[]any{"<esc>:wq<CR>"}, []sessdomain.Key{{Name: "escape"}, {Text: ":wq"}, {Name: "enter"}}},
		{[]any{"ciwsquash<Esc>j"}, []sessdomain.Key{{Text: "ciwsquash"}, {Name: "escape"}, {Text: "j"}}},
		{[]any{"<C-c>", "<ctrl+d>", "<Alt+x>"}, []sessdomain.Key{{Name: "c", Ctrl: true}, {Name: "d", Ctrl: true}, {Name: "x", Alt: true}}},
		{[]any{"<Up><PageDown><Tab><BS><Del><Space><F2>"}, []sessdomain.Key{{Name: "up"}, {Name: "page_down"}, {Name: "tab"}, {Name: "backspace"}, {Name: "delete"}, {Name: "space"}, {Name: "f2"}}},
		{[]any{"a<b and <html> stay\n"}, []sessdomain.Key{{Text: "a<b and <html> stay"}, {Name: "enter"}}},
		{[]any{"<C-ü>"}, []sessdomain.Key{{Text: "<C-ü>"}}},
	} {
		term.sent = nil
		term.state = sessdomain.InteractState{}
		// Run as a user's approval would: some of these keys ask, which is not what is tested.
		env := domain.Env{ThreadID: "thr_1", Cwd: "/work", WriteRoot: "/work"}
		call := domain.Call{Tool: "terminal_interact", Input: strs(c.in...)}
		action, err := reg.Action(env, call)
		if err == nil {
			env.Target, env.ProgramInput = action.Target, action.ProgramInput
			_, err = reg.Invoke(context.Background(), env, call, domain.Grant{Action: action, Decision: secdomain.Decision{Verdict: secdomain.VerdictAllow}})
		}
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if len(term.sent) != 1 || !slices.Equal(term.sent[0], c.want) {
			t.Errorf("%q sent %+v, want %+v", c.in, term.sent, c.want)
		}
	}
}

// TestTheScreenLeadsWithWhoHoldsTheKeyboard_REQ_FTU_001: a screen in a result opens with who
// has the keyboard — the shell at its prompt or a program — and the cursor, before the text: the
// live run's model read a screen's top, missed that psql had exited and typed its input to the
// shell (delta `2026-10-usf2-002-live`).
func TestTheScreenLeadsWithWhoHoldsTheKeyboard_REQ_FTU_001(t *testing.T) {
	t.Parallel()
	size := sessdomain.Size{Cols: 120, Rows: 40}
	for _, c := range []struct {
		screen sessdomain.Screen
		want   string
	}{
		{
			sessdomain.Screen{Text: "$ ", CursorX: 2, Size: size, AtPrompt: true},
			"[the shell is at its prompt; cursor at column 3, row 1 of 120x40]\n$ \n",
		},
		{
			sessdomain.Screen{Text: "postgres=# \n", CursorX: 11, Size: size},
			"[a program is in the foreground, not the shell: type its input, not commands; cursor at column 12, row 1 of 120x40]\npostgres=# \n",
		},
	} {
		if got := screenText(c.screen); got != c.want {
			t.Errorf("screenText = %q, want %q", got, c.want)
		}
	}
}
