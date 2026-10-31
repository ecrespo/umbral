package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
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

// interactItem is one item of `keys`: a key object, or a string typed as text with each line
// end in it sent as enter (delta `2026-10-usf2-002-live`). A model whose tool schema is
// rendered without the nested object writes the keys as strings, and refusing that shape
// left it unable to type at all.
type interactItem struct {
	keys []interactKey
}

func (it *interactItem) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		it.keys = stringKeys(s)
		return nil
	}
	var k interactKey
	if err := json.Unmarshal(b, &k); err != nil {
		return err
	}
	it.keys = []interactKey{k}
	return nil
}

// stringKeys reads a string key: text, with each line end in it (`\n`, `\r` or `\r\n`) an
// enter, and each key named between angle brackets — `<Esc>`, `<CR>`, `<C-c>`, `<ctrl+c>` — that
// key. A bracketed word that names no key is text (delta `2026-10-usf2-002-live`). An empty
// string is no key, as an empty list is: a model fills `keys` on every action, and send_keys
// with nothing to send is refused by validation. A string that is one control character and
// nothing else is the key that sends it — the model writes vim's escape as "\u001b"; inside
// text, a control character is still refused.
func stringKeys(s string) []interactKey {
	if k, ok := controlKey(s); ok {
		return []interactKey{k}
	}
	var keys []interactKey
	text := func(t string) {
		for t != "" {
			i := strings.IndexAny(t, "\r\n")
			if i < 0 {
				keys = append(keys, interactKey{Text: t})
				return
			}
			if i > 0 {
				keys = append(keys, interactKey{Text: t[:i]})
			}
			keys = append(keys, interactKey{Key: keyEnter})
			if strings.HasPrefix(t[i:], "\r\n") {
				i++
			}
			t = t[i+1:]
		}
	}
	pending := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '<' {
			continue
		}
		j := strings.IndexByte(s[i:], '>')
		if j < 0 {
			break
		}
		k, ok := bracketKey(s[i+1 : i+j])
		if !ok {
			continue
		}
		text(s[pending:i])
		keys = append(keys, k)
		pending = i + j + 1
		i += j
	}
	text(s[pending:])
	return keys
}

// keyEnter, modCtrl and modAlt are the key name and the modifiers a string key and the schema
// spell out.
const (
	keyEnter = "enter"
	modCtrl  = "ctrl"
	modAlt   = "alt"
)

// controlKey reads a string of one control character as the key whose encoding it is: escape,
// enter (LF or CR, as in text), tab, backspace (DEL or BS) or a ctrl+letter. NUL and the other C0 characters name no key.
func controlKey(s string) (interactKey, bool) {
	if len(s) != 1 {
		return interactKey{}, false
	}
	switch c := s[0]; {
	case c == 0x1b:
		return interactKey{Key: "escape"}, true
	case c == '\n' || c == '\r':
		return interactKey{Key: keyEnter}, true
	case c == '\t':
		return interactKey{Key: "tab"}, true
	case c == 0x7f || c == '\b':
		return interactKey{Key: "backspace"}, true
	case c >= 0x01 && c <= 0x1a:
		return interactKey{Key: string(rune('a' + c - 1)), Ctrl: true}, true
	}
	return interactKey{}, false
}

// bracketAliases are the names a model writes for keys, vim's among them, beside the tool's own.
var bracketAliases = map[string]string{
	"esc": "escape", "cr": keyEnter, "return": keyEnter, "ret": keyEnter, "bs": "backspace",
	"del": "delete", "ins": "insert", "pageup": "page_up", "pgup": "page_up",
	"pagedown": "page_down", "pgdn": "page_down",
}

// bracketKey reads the inside of `<…>` as a key: a key name or an alias, or a letter or digit
// after a `ctrl`/`c` or `alt`/`a`/`m`/`meta` modifier joined by `+` or `-`, case aside.
func bracketKey(word string) (interactKey, bool) {
	w := strings.ToLower(word)
	var k interactKey
	for {
		mod, rest, found := strings.Cut(w, "+")
		if !found {
			mod, rest, found = strings.Cut(w, "-")
		}
		if !found || rest == "" {
			break
		}
		switch mod {
		case modCtrl, "c":
			k.Ctrl = true
		case modAlt, "a", "m", "meta":
			k.Alt = true
		default:
			return interactKey{}, false
		}
		w = rest
	}
	if alias, ok := bracketAliases[w]; ok {
		w = alias
	}
	switch {
	case (k.Ctrl || k.Alt) && sessdomain.IsCharKey(w):
	case slices.Contains(sessdomain.KeyNames, w):
	default:
		return interactKey{}, false
	}
	k.Key = w
	return k, true
}

type interactInput struct {
	Action    string         `json:"action"`
	Keys      []interactItem `json:"keys"`
	Regex     string         `json:"regex"`
	IdleMS    int            `json:"idle_ms"`
	Exit      bool           `json:"exit"`
	TimeoutMS int            `json:"timeout_ms"`
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
			modCtrl: map[string]any{kType: tBoolean},
			modAlt:  map[string]any{kType: tBoolean},
			"shift": map[string]any{kType: tBoolean},
		},
		"oneOf": []any{
			map[string]any{kRequired: []any{fText}, "not": map[string]any{"anyOf": []any{
				map[string]any{kRequired: []any{fKey}},
				map[string]any{kRequired: []any{modCtrl}},
				map[string]any{kRequired: []any{modAlt}},
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
			"screen, for no output during idle_ms, or for the program in the foreground to exit, ending at the first " +
			"that holds; read_screen given any of those returns the screen once it holds; send_keys given any of " +
			"those waits for it after typing, on the output the keys caused. At the shell's " +
			"prompt send a command as text with a final enter (or a final ctrl+c to clear the line); other keys " +
			"there are refused. Enter is decided on the whole line, as run_command is. If the result is " +
			"taken_over, the user holds the terminal: stop using it. Each screen opens with who has the keyboard: " +
			"type a command only when the shell is at its prompt, and a program's input only while it runs. " +
			"An editor such as vim starts in normal mode: <Esc> leaves insert mode, :wq<CR> saves and quits.",
		Risk:          secdomain.RiskExec,
		ReadOnlyCalls: true,
		InputSchema: object([]string{"action"}, map[string]any{
			"action": map[string]any{kType: tString, "enum": []any{actSendKeys, actReadScreen, actWaitFor}},
			"keys": map[string]any{
				kType: "array", "maxItems": sessdomain.MaxKeys,
				"items": map[string]any{"anyOf": []any{
					map[string]any{kType: tString, kDescription: "Text typed as it is; each line end in it is an enter."},
					key,
				}},
				kDescription: "send_keys: the keys, in order. A string is text, and its line ends are enter: [\"ls -l\\n\"].",
			},
			"regex": str("wait_for, or send_keys then wait: an RE2 regular expression matched against the screen's text."),
			"idle_ms": map[string]any{
				kType: tInteger, kMinimum: 0, kMaximum: sessdomain.MaxWaitIdle.Milliseconds(),
				kDescription: fmt.Sprintf("wait_for, or send_keys then wait: end after this long with no output, at least %d; 0 is none.", sessdomain.MinWaitIdle.Milliseconds()),
			},
			"exit": map[string]any{kType: tBoolean, kDescription: "wait_for, or send_keys then wait: end when the program in the foreground exits."},
			"timeout_ms": map[string]any{
				kType: tInteger, kMinimum: 0, kMaximum: sessdomain.MaxWaitTime.Milliseconds(),
				kDescription: "how long a wait may take; 10000 by default. Alone it waits for nothing.",
			},
		}),
	}
}

// parsed is a call's input read into the sessions module's terms. wait is a send_keys that
// waits on cond after its keys are sent.
type parsed struct {
	action string
	keys   []sessdomain.Key
	cond   sessdomain.WaitCondition
	wait   bool
}

// waitCondition reads a wait_for condition into p.cond and validates it.
func waitCondition(p *parsed, in interactInput) error {
	p.cond = sessdomain.WaitCondition{
		Idle: time.Duration(in.IdleMS) * time.Millisecond, Exit: in.Exit, Timeout: sessdomain.DefaultWaitTime,
	}
	if in.TimeoutMS > 0 {
		p.cond.Timeout = time.Duration(in.TimeoutMS) * time.Millisecond
	}
	if in.Regex != "" {
		re, err := regexp.Compile(in.Regex)
		if err != nil {
			return fmt.Errorf("regex: %w", err)
		}
		p.cond.Regex = re
		// A regex that matches the empty string — `$`, `.*` — holds before any output; it is
		// read as a wait for the output to settle (delta `2026-10-usf2-002-live`).
		if re.MatchString("") {
			p.cond.Regex = nil
			if p.cond.Idle == 0 {
				p.cond.Idle = settleIdle
			}
		}
	}
	return p.cond.Validate()
}

// settleIdle is the quiet a wait whose regex matches the empty string waits for instead.
const settleIdle = 500 * time.Millisecond

func parseInteract(input json.RawMessage) (parsed, error) {
	var in interactInput
	if err := decode(input, &in); err != nil {
		return parsed{}, err
	}
	p := parsed{action: in.Action}
	invalid := func(err error) (parsed, error) {
		return parsed{}, fmt.Errorf("%w: %w", domain.ErrInvalidInput, err)
	}
	// The wait fields' zero values are absent: a model fills every field of the schema, and
	// `exit: false` or `idle_ms: 0` asks for nothing (delta `2026-10-usf2-002-live`).
	condition := in.Regex != "" || in.IdleMS != 0 || in.Exit
	for _, it := range in.Keys {
		for _, k := range it.keys {
			p.keys = append(p.keys, sessdomain.Key{Text: k.Text, Name: k.Key, Ctrl: k.Ctrl, Alt: k.Alt, Shift: k.Shift})
		}
	}
	switch in.Action {
	case actSendKeys:
		if err := sessdomain.ValidateKeys(p.keys); err != nil {
			return invalid(err)
		}
		// A condition on send_keys is a wait_for run after the keys; a timeout alone waits
		// for nothing.
		if condition {
			if err := waitCondition(&p, in); err != nil {
				return invalid(err)
			}
			p.wait = true
		}
	case actReadScreen:
		if len(p.keys) != 0 {
			return invalid(errors.New("read_screen takes no keys"))
		}
		// A condition on read_screen reads the screen once it holds, as wait_for does.
		if condition {
			if err := waitCondition(&p, in); err != nil {
				return invalid(err)
			}
			p.wait = true
		}
	case actWaitFor:
		if len(p.keys) != 0 {
			return invalid(errors.New("wait_for takes no keys"))
		}
		if err := waitCondition(&p, in); err != nil {
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
		res, seq, err := t.send(ctx, env, p.keys)
		if err != nil || !p.wait || res.Summary == takenOver {
			return res, err
		}
		// The wait reads only what the terminal wrote after these keys, so a prompt or a
		// line still on the screen from before cannot satisfy it.
		cond := p.cond
		cond.Fresh, cond.After = true, seq
		cond.SkipEcho = len(p.keys) > 0 && p.keys[len(p.keys)-1].Name == keyEnter && !p.keys[len(p.keys)-1].Ctrl
		out, err := t.keys.WaitFor(ctx, env.ThreadID, env.Cwd, cond)
		if err != nil {
			waited, err := heldOr(err)
			if err != nil {
				return domain.Result{}, err
			}
			res.Text += waited.Text
			return res, nil
		}
		res.Text += "[" + out.Reason + "]\n" + screenText(out.Screen)
		return res, nil
	case actReadScreen:
		if p.wait {
			return t.waitFor(ctx, env, p.cond)
		}
		screen, err := t.keys.ReadScreen(ctx, env.ThreadID, env.Cwd)
		if err != nil {
			return heldOr(err)
		}
		return domain.Result{Summary: "screen", Text: screenText(screen)}, nil
	default:
		return t.waitFor(ctx, env, p.cond)
	}
}

// waitFor waits on the screen and answers with the reason and the screen it ended on.
func (t terminalInteract) waitFor(ctx context.Context, env domain.Env, cond sessdomain.WaitCondition) (domain.Result, error) {
	out, err := t.keys.WaitFor(ctx, env.ThreadID, env.Cwd, cond)
	if err != nil {
		return heldOr(err)
	}
	return domain.Result{Summary: out.Reason, Text: "[" + out.Reason + "]\n" + screenText(out.Screen)}, nil
}

// send sends the keys in the state the grant was decided on: the target is computed again
// from the terminal now and must be the one the registry checked (env.Target), and the
// sessions module checks that state once more under its input lock (decision 5). It also
// returns the output seq the keys were written after.
func (t terminalInteract) send(ctx context.Context, env domain.Env, keys []sessdomain.Key) (domain.Result, uint64, error) {
	state := t.keys.InteractState(env.ThreadID, env.Cwd)
	if state.TakenOver || (env.Target == takenOver && !env.ProgramInput) {
		// A call decided while the user held the terminal sends nothing, even once it is
		// handed back: it was decided as a read.
		res, err := heldOr(sessdomain.ErrTakenOver)
		return res, 0, err
	}
	target, program, err := sendTarget(state, keys)
	if err != nil {
		return domain.Result{}, 0, err
	}
	if target != env.Target || program != env.ProgramInput {
		return domain.Result{}, 0, sessdomain.ErrTerminalChanged
	}
	plan, err := t.keys.SendKeys(ctx, env.ThreadID, env.Cwd, keys, state)
	if err != nil {
		res, err := heldOr(err)
		return res, 0, err
	}
	switch {
	case state.AtPrompt && plan.Submit:
		return domain.Result{Summary: "entered", Text: "Entered at the prompt: " + plan.Line + "\nRead the screen or wait_for to see what it does.\n"}, plan.Seq, nil
	case state.AtPrompt:
		return domain.Result{Summary: "typed", Text: "Typed at the prompt; the line is now: " + plan.Line + "\n"}, plan.Seq, nil
	default:
		return domain.Result{Summary: "sent", Text: "Sent to the program in the foreground.\n"}, plan.Seq, nil
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

// screenText is a screen as the model reads it: what holds the terminal and where the cursor
// is, then the text.
func screenText(s sessdomain.Screen) string {
	var b strings.Builder
	// Who has the keyboard comes first: a model reads a screen from its top, and the last
	// line was missed (delta `2026-10-usf2-002-live`).
	holder := "a program is in the foreground, not the shell: type its input, not commands"
	if s.AtPrompt {
		holder = "the shell is at its prompt"
	}
	fmt.Fprintf(&b, "[%s; cursor at column %d, row %d of %dx%d]\n", holder, s.CursorX+1, s.CursorY+1, s.Size.Cols, s.Size.Rows)
	b.WriteString(s.Text)
	if s.Text != "" && !strings.HasSuffix(s.Text, "\n") {
		b.WriteString("\n")
	}
	return b.String()
}
