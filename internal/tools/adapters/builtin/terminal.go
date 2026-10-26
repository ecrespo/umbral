package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	secdomain "github.com/ecrespo/umbral/internal/security/domain"
	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
	sessionsports "github.com/ecrespo/umbral/internal/sessions/ports"
	"github.com/ecrespo/umbral/internal/tools/domain"
)

// terminal_interact's actions (delta `2026-10-terminal-interact`, decision 2).
const (
	actSendKeys   = "send_keys"
	actReadScreen = "read_screen"
	actWaitFor    = "wait_for"
	takenOver     = "taken_over"
	fKey          = "key"
	fText         = "text"
)

// terminalInteract is terminal_interact: the thread's PTY driven key by key, its screen read
// and waited on (REQ-FTU-001). It is the PTY run_command uses.
type terminalInteract struct {
	keys sessionsports.AgentKeys
}

type interactKey struct {
	Text  string `json:"text"`
	Key   string `json:"key"`
	Ctrl  bool   `json:"ctrl"`
	Alt   bool   `json:"alt"`
	Shift bool   `json:"shift"`
}

type interactInput struct {
	Action    string        `json:"action"`
	Keys      []interactKey `json:"keys"`
	Regex     string        `json:"regex"`
	IdleMS    int           `json:"idle_ms"`
	Exit      bool          `json:"exit"`
	TimeoutMS int           `json:"timeout_ms"`
}

func (terminalInteract) Spec() domain.Spec {
	keyNames := make([]any, 0, len(sessdomain.KeyNames)+36)
	for _, n := range sessdomain.KeyNames {
		keyNames = append(keyNames, n)
	}
	for c := 'a'; c <= 'z'; c++ {
		keyNames = append(keyNames, string(c))
	}
	for c := '0'; c <= '9'; c++ {
		keyNames = append(keyNames, string(c))
	}
	key := map[string]any{
		kType: "object", "additionalProperties": false,
		"properties": map[string]any{
			fText:   map[string]any{kType: tString, "minLength": 1, kDescription: "Printable text, typed as it is."},
			fKey:    map[string]any{kType: tString, "enum": keyNames, kDescription: "A key; a letter or digit only with ctrl or alt."},
			"ctrl":  map[string]any{kType: tBoolean},
			"alt":   map[string]any{kType: tBoolean},
			"shift": map[string]any{kType: tBoolean},
		},
		"oneOf": []any{
			map[string]any{kRequired: []any{fText}, "not": map[string]any{"anyOf": []any{
				map[string]any{kRequired: []any{fKey}},
				map[string]any{kRequired: []any{"ctrl"}},
				map[string]any{kRequired: []any{"alt"}},
				map[string]any{kRequired: []any{"shift"}},
			}}},
			map[string]any{kRequired: []any{fKey}, "not": map[string]any{kRequired: []any{fText}}},
		},
	}
	return domain.Spec{
		Name: "terminal_interact",
		Description: "Drive this thread's terminal, the one run_command uses, as a person at the keyboard " +
			"would: answer an interactive program (psql, git rebase -i, an editor). " +
			"send_keys types keys; read_screen returns the screen's text; wait_for waits for a regex on the " +
			"screen, for no output during idle_ms, or for the program in the foreground to exit. At the shell's " +
			"prompt send a command as text with a final enter (or a final ctrl+c to clear the line); other keys " +
			"there are refused. Enter is decided on the whole line, as run_command is. If the result is " +
			"taken_over, the user holds the terminal: stop using it.",
		Risk:          secdomain.RiskExec,
		ReadOnlyCalls: true,
		InputSchema: object([]string{"action"}, map[string]any{
			"action": map[string]any{kType: tString, "enum": []any{actSendKeys, actReadScreen, actWaitFor}},
			"keys": map[string]any{
				kType: "array", "minItems": 1, "maxItems": sessdomain.MaxKeys, "items": key,
				kDescription: "send_keys: the keys, in order.",
			},
			"regex":   str("wait_for: an RE2 regular expression matched against the screen's text."),
			"idle_ms": map[string]any{kType: tInteger, kMinimum: sessdomain.MinWaitIdle.Milliseconds(), kMaximum: sessdomain.MaxWaitIdle.Milliseconds(), kDescription: "wait_for: end after this long with no output."},
			"exit":    map[string]any{kType: tBoolean, "const": true, kDescription: "wait_for: end when the program in the foreground exits."},
			"timeout_ms": map[string]any{
				kType: tInteger, kMinimum: 1, kMaximum: sessdomain.MaxWaitTime.Milliseconds(),
				kDescription: "wait_for: give up after this long; 10000 by default.",
			},
		}),
	}
}

// parsed is a call's input read into the sessions module's terms.
type parsed struct {
	action string
	keys   []sessdomain.Key
	cond   sessdomain.WaitCondition
}

func parseInteract(input json.RawMessage) (parsed, error) {
	var in interactInput
	if err := decode(input, &in); err != nil {
		return parsed{}, err
	}
	p := parsed{action: in.Action}
	invalid := func(err error) (parsed, error) {
		return parsed{}, fmt.Errorf("%w: %w", domain.ErrInvalidInput, err)
	}
	switch in.Action {
	case actSendKeys:
		if in.Regex != "" || in.IdleMS != 0 || in.Exit || in.TimeoutMS != 0 {
			return invalid(errors.New("send_keys takes only keys"))
		}
		for _, k := range in.Keys {
			p.keys = append(p.keys, sessdomain.Key{Text: k.Text, Name: k.Key, Ctrl: k.Ctrl, Alt: k.Alt, Shift: k.Shift})
		}
		if err := sessdomain.ValidateKeys(p.keys); err != nil {
			return invalid(err)
		}
	case actReadScreen:
		if len(in.Keys) != 0 || in.Regex != "" || in.IdleMS != 0 || in.Exit || in.TimeoutMS != 0 {
			return invalid(errors.New("read_screen takes nothing"))
		}
	case actWaitFor:
		if len(in.Keys) != 0 {
			return invalid(errors.New("wait_for takes no keys"))
		}
		p.cond = sessdomain.WaitCondition{
			Idle: time.Duration(in.IdleMS) * time.Millisecond, Exit: in.Exit, Timeout: sessdomain.DefaultWaitTime,
		}
		if in.TimeoutMS > 0 {
			p.cond.Timeout = time.Duration(in.TimeoutMS) * time.Millisecond
		}
		if in.Regex != "" {
			re, err := regexp.Compile(in.Regex)
			if err != nil {
				return invalid(fmt.Errorf("regex: %w", err))
			}
			p.cond.Regex = re
		}
		if err := p.cond.Validate(); err != nil {
			return invalid(err)
		}
	}
	return p, nil
}

// sendTarget is what the policy decides a send on, in state (decision 4): the whole line an
// enter at the prompt submits, as a shell line; at the prompt without an enter, the keys typed;
// to a program, its input since its last line end with these keys, so a line split
// across calls is decided whole.
func sendTarget(state sessdomain.InteractState, keys []sessdomain.Key) (target string, program bool, err error) {
	plan, err := sessdomain.PlanKeys(state, keys)
	if err != nil {
		return "", false, fmt.Errorf("%w: %w", domain.ErrInvalidInput, err)
	}
	switch {
	case state.AtPrompt && plan.Submit:
		return plan.Line, false, nil
	case state.AtPrompt:
		return sessdomain.RenderKeys(keys), true, nil
	default:
		return plan.Target, true, nil
	}
}

func (t terminalInteract) Action(env domain.Env, input json.RawMessage) (secdomain.Action, error) {
	p, err := parseInteract(input)
	if err != nil {
		return secdomain.Action{}, err
	}
	a := secdomain.Action{
		ThreadID: env.ThreadID, Tool: "terminal_interact", Risk: secdomain.RiskReadOnly, Target: p.action,
		Cwd: realPath(env.Cwd), WriteRoot: realPath(env.WriteRoot),
	}
	if p.action != actSendKeys || t.keys == nil {
		return a, nil
	}
	state := t.keys.InteractState(env.ThreadID, env.Cwd)
	if state.TakenOver {
		// Nothing is sent while the user holds the terminal: the call reads taken_over. A
		// hand-back before it runs changes the action, which the registry then refuses.
		a.Target = takenOver
		return a, nil
	}
	target, program, err := sendTarget(state, p.keys)
	if err != nil {
		return secdomain.Action{}, err
	}
	a.Risk, a.Target, a.ProgramInput = secdomain.RiskExec, target, program
	return a, nil
}

func (terminalInteract) Summary(input json.RawMessage) string {
	p, err := parseInteract(input)
	if err != nil || p.action != actSendKeys {
		var in interactInput
		_ = json.Unmarshal(input, &in)
		return in.Action
	}
	return sessdomain.RenderKeys(p.keys)
}

func (t terminalInteract) Run(ctx context.Context, env domain.Env, input json.RawMessage) (domain.Result, error) {
	p, err := parseInteract(input)
	if err != nil {
		return domain.Result{}, err
	}
	if t.keys == nil {
		return domain.Result{}, errors.New("terminal_interact has no terminal in this daemon")
	}
	if env.ThreadID == "" {
		return domain.Result{}, fmt.Errorf("%w: terminal_interact drives a thread's terminal and this call has no thread", domain.ErrInvalidEnv)
	}
	switch p.action {
	case actSendKeys:
		return t.send(ctx, env, p.keys)
	case actReadScreen:
		screen, err := t.keys.ReadScreen(ctx, env.ThreadID, env.Cwd)
		if err != nil {
			return heldOr(err)
		}
		return domain.Result{Summary: "screen", Text: screenText(screen)}, nil
	default:
		out, err := t.keys.WaitFor(ctx, env.ThreadID, env.Cwd, p.cond)
		if err != nil {
			return heldOr(err)
		}
		return domain.Result{Summary: out.Reason, Text: "[" + out.Reason + "]\n" + screenText(out.Screen)}, nil
	}
}

// send sends the keys in the state the grant was decided on: the target is computed again
// from the terminal now and must be the one the registry checked (env.Target), and the
// sessions module checks that state once more under its input lock (decision 5).
func (t terminalInteract) send(ctx context.Context, env domain.Env, keys []sessdomain.Key) (domain.Result, error) {
	state := t.keys.InteractState(env.ThreadID, env.Cwd)
	if state.TakenOver || (env.Target == takenOver && !env.ProgramInput) {
		// A call decided while the user held the terminal sends nothing, even once it is
		// handed back: it was decided as a read.
		return heldOr(sessdomain.ErrTakenOver)
	}
	target, program, err := sendTarget(state, keys)
	if err != nil {
		return domain.Result{}, err
	}
	if target != env.Target || program != env.ProgramInput {
		return domain.Result{}, sessdomain.ErrTerminalChanged
	}
	plan, err := t.keys.SendKeys(ctx, env.ThreadID, env.Cwd, keys, state)
	if err != nil {
		return heldOr(err)
	}
	switch {
	case state.AtPrompt && plan.Submit:
		return domain.Result{Summary: "entered", Text: "Entered at the prompt: " + plan.Line + "\nRead the screen or wait_for to see what it does.\n"}, nil
	case state.AtPrompt:
		return domain.Result{Summary: "typed", Text: "Typed at the prompt; the line is now: " + plan.Line + "\n"}, nil
	default:
		return domain.Result{Summary: "sent", Text: "Sent to the program in the foreground.\n"}, nil
	}
}

// heldOr turns the user's holding the terminal into the result taken_over (REQ-FTU-002), and a
// read before the thread has a terminal into no_terminal; any other error stays one.
func heldOr(err error) (domain.Result, error) {
	if errors.Is(err, sessdomain.ErrNoTerminal) {
		return domain.Result{Summary: "no_terminal", Text: err.Error() + "\n"}, nil
	}
	if errors.Is(err, sessdomain.ErrTakenOver) {
		return domain.Result{
			Summary: takenOver,
			Text:    "taken_over: the user has taken over this terminal. Stop using it; they will hand it back.\n",
		}, nil
	}
	return domain.Result{}, err
}

// screenText is a screen as the model reads it: the text, then where the cursor is and what
// holds the terminal.
func screenText(s sessdomain.Screen) string {
	var b strings.Builder
	b.WriteString(s.Text)
	if s.Text != "" && !strings.HasSuffix(s.Text, "\n") {
		b.WriteString("\n")
	}
	holder := "a program is in the foreground"
	if s.AtPrompt {
		holder = "the shell is at its prompt"
	}
	fmt.Fprintf(&b, "[cursor at column %d, row %d of %dx%d; %s]\n", s.CursorX+1, s.CursorY+1, s.Size.Cols, s.Size.Rows, holder)
	return b.String()
}
