package tui

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/ecrespo/umbral/internal/tui/ports"
)

// The agent panel (REQ-TUI-001's agent half, REQ-TUI-002, REQ-TUI-003): a column beside the
// panes that shows one thread's turn as it streams, the approvals pending on every thread,
// and the line the user is typing to it. ctrl+space moves input between the shell and the
// panel.

// keyEsc is the escape key's name, which the panel and the shell both route.
const keyEsc = "esc"

// Panel width bounds. A panel narrower than the minimum cannot show a diff line; one wider
// than the maximum takes columns the shell needs more.
const (
	agentPanelMin = 30
	agentPanelMax = 72
)

type entryKind int

const (
	entryUser entryKind = iota
	entryAgent
	entryTool
	entryNote
)

type entry struct {
	kind entryKind
	// id is a tool call's, which its status changes are matched by.
	id   string
	text string
}

// agentPanel is the panel's state. The model owns one: a thread is created by the first
// message sent and kept for the rest of the run.
type agentPanel struct {
	open bool
	// mode routes the keyboard to the panel instead of the focused shell (REQ-TUI-002).
	mode bool

	threadID string
	creating bool
	// queued is a message typed before the thread existed, sent once it does.
	queued *outgoing

	input    []rune
	running  bool
	thinking bool
	entries  []entry

	// approvals are every thread's, answered oldest first, one chord each. answered keeps
	// what was answered here, so a late approval.list cannot bring one back.
	approvals []ports.Approval
	answered  map[string]bool
	showDiff  bool
}

type outgoing struct {
	text        string
	attachments []ports.Attachment
}

// The messages the panel's commands send back.
type (
	threadCreatedMsg struct{ threadID string }
	threadFailedMsg  struct{ err error }
	sentMsg          struct{ err error }
	// cancelledMsg is thread.cancel's answer; stopped is false when no turn was running.
	cancelledMsg struct {
		threadID string
		stopped  bool
	}
	approvalsLoadedMsg struct {
		approvals []ports.Approval
		err       error
	}
	respondFailedMsg struct {
		approval ports.Approval
		err      error
	}
)

// AgentMode reports whether keys go to the agent panel.
func (m *Model) AgentMode() bool { return m.agent.mode }

// AgentPanelOpen reports whether the panel is shown.
func (m *Model) AgentPanelOpen() bool { return m.agent.open }

// AgentInput is the line being typed to the agent.
func (m *Model) AgentInput() string { return string(m.agent.input) }

// AgentRunning reports whether the panel's thread has a turn running.
func (m *Model) AgentRunning() bool { return m.agent.running }

// PendingApprovals are the approvals waiting for an answer, oldest first.
func (m *Model) PendingApprovals() []ports.Approval {
	return append([]ports.Approval(nil), m.agent.approvals...)
}

// agentPanelWidth is the panel's column: two fifths of the window, within its bounds.
func (m *Model) agentPanelWidth() int {
	if !m.agent.open {
		return 0
	}
	return clamp(m.width*2/5, agentPanelMin, agentPanelMax)
}

// toggleMode is ctrl+space. Entering agent mode opens the panel; leaving it keeps the panel
// on screen, so a running turn stays visible while the user types in the shell.
func (m *Model) toggleMode() tea.Cmd {
	m.agent.mode = !m.agent.mode
	if m.agent.mode && !m.agent.open {
		m.agent.open = true
		return m.resize(m.width, m.height)
	}
	return nil
}

// closePanel hides the panel and gives the keyboard back to the shell.
func (m *Model) closePanel() tea.Cmd {
	m.agent.open, m.agent.mode = false, false
	return m.resize(m.width, m.height)
}

// attachBlock is "attach to agent" on the selected block (REQ-TUI-003): the panel opens in
// agent mode with `@block:<id>` in the input, ready for the user to say what to do with it.
func (m *Model) attachBlock() tea.Cmd {
	blk, ok := m.SelectedBlock()
	if !ok {
		m.status = "no block selected: ctrl+b opens the block list"
		return nil
	}
	m.agent.input = []rune("@block:" + blk.ID + " ")
	m.agent.mode = true
	if !m.agent.open {
		m.agent.open = true
		return m.resize(m.width, m.height)
	}
	return nil
}

// handleAgentKey is a keypress in agent mode that the TUI's own shortcuts did not claim.
//
// Approvals are answered with control chords, never letters: an approval can arrive while
// the user is typing, and the next letter of a word must not approve a write or persist an
// `always` rule. Everything printable is typed.
func (m *Model) handleAgentKey(msg tea.KeyPressMsg) tea.Cmd {
	a := &m.agent
	key := msg.String()

	if len(a.approvals) > 0 {
		switch key {
		case "ctrl+y":
			return m.answer("approve", "once")
		case "ctrl+r":
			return m.answer("approve", "thread")
		case "ctrl+l":
			return m.answer("approve", "always")
		case "ctrl+x":
			return m.answer("deny", "once")
		case "ctrl+e":
			a.showDiff = !a.showDiff
			return nil
		}
	}

	switch key {
	case "enter":
		return m.sendInput()
	case "backspace":
		if n := len(a.input); n > 0 {
			a.input = a.input[:n-1]
		}
		return nil
	case "ctrl+c":
		if a.running || len(a.approvals) > 0 {
			return m.cancelTurn()
		}
		a.input = nil
		return nil
	case keyEsc:
		if len(a.input) > 0 {
			a.input = nil
			return nil
		}
		return m.closePanel()
	case "space":
		a.input = append(a.input, ' ')
		return nil
	}
	if text := tea.Key(msg).Text; text != "" {
		a.input = append(a.input, []rune(text)...)
	}
	return nil
}

// paste is text pasted in agent mode: one line of the input, without the control
// characters a clipboard can carry.
func (m *Model) paste(text string) {
	text = strings.ReplaceAll(sanitize(text), "\n", " ")
	m.agent.input = append(m.agent.input, []rune(text)...)
}

// sendInput sends the typed line, creating the thread on the first message.
func (m *Model) sendInput() tea.Cmd {
	a := &m.agent
	text := strings.TrimSpace(string(a.input))
	if text == "" {
		return nil
	}
	if a.running || a.queued != nil {
		m.status = "a turn is running: wait for it, or ctrl+c to stop it"
		return nil
	}
	out := outgoing{text: text, attachments: parseAttachments(text)}
	a.input = nil
	a.entries = append(a.entries, entry{kind: entryUser, text: text})
	a.running = true

	if a.threadID != "" {
		return m.sendCmd(a.threadID, out)
	}
	a.queued = &out
	if a.creating {
		return nil
	}
	a.creating = true
	cwd := ""
	if p := m.focusedPane(); p != nil {
		cwd = p.cwd
	}
	return m.createThreadCmd(cwd)
}

// threadCreated installs the thread and sends what was waiting for it.
func (m *Model) threadCreated(msg threadCreatedMsg) tea.Cmd {
	a := &m.agent
	a.threadID, a.creating = msg.threadID, false
	if a.queued == nil {
		return nil
	}
	out := *a.queued
	a.queued = nil
	return m.sendCmd(a.threadID, out)
}

func (m *Model) agentFailed(err error) {
	a := &m.agent
	a.creating, a.running, a.queued = false, false, nil
	a.entries = append(a.entries, entry{kind: entryNote, text: "not sent: " + err.Error()})
	m.status = err.Error()
}

// answer replies to the oldest pending approval.
func (m *Model) answer(decision, scope string) tea.Cmd {
	a := &m.agent
	apr := a.approvals[0]
	a.approvals = a.approvals[1:]
	a.showDiff = false
	if a.answered == nil {
		a.answered = map[string]bool{}
	}
	a.answered[apr.ID] = true
	verb := map[string]string{"approve": "approved", "deny": "denied"}[decision]
	switch scope {
	case "always":
		verb += " always"
	case "thread":
		verb += " for this thread"
	}
	a.entries = append(a.entries, entry{kind: entryNote, text: fmt.Sprintf("%s %s %s", verb, apr.Tool, apr.Summary)})
	return m.respondCmd(apr, decision, scope)
}

// cancelTurn is ctrl+c: it stops the turn whose approval is shown, or else the panel's own.
// A message still waiting for its thread is taken back instead: there is no turn yet.
func (m *Model) cancelTurn() tea.Cmd {
	a := &m.agent
	if a.queued != nil {
		a.queued, a.running = nil, false
		a.entries = append(a.entries, entry{kind: entryNote, text: "not sent: stopped before the thread existed"})
		return nil
	}
	target := a.threadID
	if len(a.approvals) > 0 {
		target = a.approvals[0].ThreadID
	}
	if target == "" {
		return nil
	}
	m.status = "stopping the turn"
	return m.cancelCmd(target)
}

// cancelled is thread.cancel's answer. One that stopped nothing means the panel missed the
// turn's end, which a lossy bus or a reconnect can do; waiting for it would refuse every
// later message.
//
// Its approvals go too: with no turn, nothing is waiting on them.
func (m *Model) cancelled(msg cancelledMsg) {
	a := &m.agent
	if msg.stopped {
		return
	}
	m.dropApprovals(msg.threadID)
	if msg.threadID != a.threadID || !a.running {
		return
	}
	a.running, a.thinking = false, false
	a.entries = append(a.entries, entry{kind: entryNote, text: "no turn was running"})
}

// dropApprovals takes a thread's approvals off the panel.
func (m *Model) dropApprovals(threadID string) {
	a := &m.agent
	kept := a.approvals[:0]
	for _, apr := range a.approvals {
		if apr.ThreadID != threadID {
			kept = append(kept, apr)
		}
	}
	a.approvals = kept
	if len(kept) == 0 {
		a.showDiff = false
	}
}

// respondFailed is an answer that did not go through. One the daemon never took — a
// timeout, a lost connection — puts the approval back, to be answered again; one it says
// was already decided or has expired leaves nothing to answer.
func (m *Model) respondFailed(msg respondFailedMsg) {
	a := &m.agent
	if errors.Is(msg.err, ports.ErrApprovalGone) {
		a.entries = append(a.entries, entry{kind: entryNote, text: "already decided or expired: " + msg.approval.Tool + " " + msg.approval.Summary})
		return
	}
	delete(a.answered, msg.approval.ID)
	a.approvals = append([]ports.Approval{msg.approval}, a.approvals...)
	m.status = "the answer did not go through: " + msg.err.Error()
}

// addApproval queues one approval, once: approval.list and approval.requested can both
// carry it.
func (m *Model) addApproval(apr ports.Approval) {
	a := &m.agent
	if a.answered[apr.ID] {
		return
	}
	for _, have := range a.approvals {
		if have.ID == apr.ID {
			return
		}
	}
	a.approvals = append(a.approvals, apr)
	a.open = true
}

// approvalsLoaded is approval.list's answer at start: what is still waiting from before
// this run, of any thread.
func (m *Model) approvalsLoaded(msg approvalsLoadedMsg) tea.Cmd {
	if msg.err != nil {
		m.status = "approvals: " + msg.err.Error()
		return nil
	}
	wasOpen := m.agent.open
	for _, apr := range msg.approvals {
		m.addApproval(apr)
	}
	if m.agent.open && !wasOpen {
		return m.resize(m.width, m.height)
	}
	return nil
}

// applyAgentEvent folds one of the agent's notifications into the panel. Approvals are
// every thread's, and a turn's end takes its thread's away; the transcript is the panel's
// own thread only.
func (m *Model) applyAgentEvent(ev ports.Event) {
	a := &m.agent
	switch ev.Kind {
	case ports.EventApprovalRequested:
		m.addApproval(ev.Approval)
		return

	case ports.EventTurnFinished:
		// A turn that ended leaves nothing of its own to answer: a cancel expires what was
		// waiting (API §5.25), and answering it would be a CONFLICT.
		m.dropApprovals(ev.ThreadID)
	}

	if a.threadID == "" || ev.ThreadID != a.threadID {
		return
	}
	switch ev.Kind {
	case ports.EventThreadDelta:
		if ev.Reasoning {
			a.thinking = true
			return
		}
		a.thinking = false
		if n := len(a.entries); n > 0 && a.entries[n-1].kind == entryAgent {
			a.entries[n-1].text += ev.Text
			return
		}
		a.entries = append(a.entries, entry{kind: entryAgent, text: ev.Text})

	case ports.EventToolCall:
		a.thinking = false
		line := ev.Tool + ": " + ev.Status
		// A call's status changes in place, on its own row, rather than adding a row per
		// change.
		for i := len(a.entries) - 1; i >= 0; i-- {
			if a.entries[i].kind == entryTool && a.entries[i].id == ev.ToolCallID {
				a.entries[i].text = line
				return
			}
		}
		a.entries = append(a.entries, entry{kind: entryTool, id: ev.ToolCallID, text: line})

	case ports.EventTurnFinished:
		a.running, a.thinking = false, false
		a.entries = append(a.entries, entry{kind: entryNote, text: "turn ended: " + ev.StopReason})
	}
}

// attachmentPrefixes are the input's tokens for thread.send's attachment kinds:
// REQ-CTX-002's `@directory`, and `@dir` for short.
var attachmentPrefixes = []struct{ prefix, kind string }{
	{"@block:", "block"}, {"@file:", "file"}, {"@directory:", "dir"}, {"@dir:", "dir"},
}

// parseAttachments finds `@block:<id>`, `@file:<path>` and `@directory:<path>` in the text,
// which is how the input names what thread.send carries as attachments (REQ-CTX-002).
func parseAttachments(text string) []ports.Attachment {
	var out []ports.Attachment
	for _, word := range strings.Fields(text) {
		for _, p := range attachmentPrefixes {
			ref, ok := strings.CutPrefix(word, p.prefix)
			if !ok {
				continue
			}
			if ref = strings.TrimRight(ref, ",;:)!?\"'"); ref != "" {
				out = append(out, ports.Attachment{Kind: p.kind, Ref: ref})
			}
			break
		}
	}
	return out
}

// renderAgentPanel draws the panel's column: a header, the transcript's tail, the pending
// approval, and the input line. It is never taller than height: a long diff is cut to the
// room left, so the keys that answer and the input line stay on screen.
func (m *Model) renderAgentPanel(height int) []string {
	a := &m.agent
	width := m.agentPanelWidth()

	state := "idle"
	switch {
	case len(a.approvals) > 0:
		state = "waiting for you"
	case a.thinking:
		state = "thinking"
	case a.running:
		state = "working"
	}
	header := "agent · " + state
	if a.threadID != "" {
		header += " · " + a.threadID
	}

	var approval, diff, keys []string
	if len(a.approvals) > 0 {
		apr := a.approvals[0]
		line := fmt.Sprintf("approve %s (%s): %s", apr.Tool, apr.Risk, apr.Summary)
		if apr.ThreadID != a.threadID {
			line += " · " + apr.ThreadID
		}
		approval = append([]string{strings.Repeat("─", width)}, wrap(sanitize(line), width)...)
		if a.showDiff && apr.Diff != "" {
			for _, line := range strings.Split(strings.TrimSuffix(sanitize(apr.Diff), "\n"), "\n") {
				diff = append(diff, wrap(line, width)...)
			}
		}
		hint := "ctrl+y approve · ctrl+r for this thread · ctrl+l always · ctrl+x deny"
		if apr.Diff != "" {
			hint += " · ctrl+e diff"
		}
		if n := len(a.approvals); n > 1 {
			hint += fmt.Sprintf(" · %d more", n-1)
		}
		keys = wrap(hint, width)
	}
	input := []string{strings.Repeat("─", width)}
	if a.mode {
		input = append(input, wrap("> "+string(a.input)+"█", width)...)
	} else {
		input = append(input, "ctrl+space to type here")
	}

	// The approval takes what the header, its keys and the input leave, keeping its head:
	// the tool, its risk and the start of what it does are what is being approved.
	room := height - 1 - len(keys) - len(input)
	if len(approval) > room && room >= 2 {
		approval = append(approval[:room-1], fmt.Sprintf("… %d more lines", len(approval)-room+1))
	}
	// The diff takes what is left after that.
	room -= len(approval)
	if len(diff) > room {
		cut := max(room-1, 0)
		more := fmt.Sprintf("… %d more lines", len(diff)-cut)
		diff = append(diff[:cut], more)
		if room < 1 {
			diff = nil
		}
	}
	bottom := append(append(append(approval, diff...), keys...), input...)

	var body []string
	for _, e := range a.entries {
		prefix := map[entryKind]string{entryUser: "you: ", entryAgent: "", entryTool: "⚙ ", entryNote: "— "}[e.kind]
		for _, line := range strings.Split(sanitize(prefix+e.text), "\n") {
			body = append(body, wrap(line, width)...)
		}
	}
	room = max(height-1-len(bottom), 0)
	if len(body) > room {
		body = body[len(body)-room:]
	}

	rows := append([]string{truncate(header, width)}, body...)
	for len(rows) < height-len(bottom) {
		rows = append(rows, "")
	}
	rows = append(rows, bottom...)
	if len(rows) > height {
		// A window too short even for the approval and the input: the bottom is what
		// matters.
		rows = rows[len(rows)-height:]
	}
	return rows
}

// sanitize makes text from the model or a tool's target safe to draw: escape sequences,
// other control characters and invisible format characters are dropped, since they would drive the terminal rather than be
// shown, and tabs become spaces, since the panel measures a row in runes.
func sanitize(s string) string {
	const tabStop = 8
	var b strings.Builder
	r := []rune(s)
	col := 0
	for i := 0; i < len(r); i++ {
		c := r[i]
		switch {
		case c == '\n':
			b.WriteRune(c)
			col = 0
		case c == '\t':
			n := tabStop - col%tabStop
			b.WriteString(strings.Repeat(" ", n))
			col += n
		case c == 0x1b && i+1 < len(r) && r[i+1] == '[', c == 0x9b:
			// CSI: parameters up to a final byte in @ through ~.
			if c == 0x1b {
				i++
			}
			for i+1 < len(r) && (r[i+1] < 0x40 || r[i+1] > 0x7e) {
				i++
			}
			i++
		case c == 0x1b && i+1 < len(r) && r[i+1] == ']', c == 0x9d:
			// OSC: up to and including BEL or ST, or to the end of an unterminated one.
			for i+1 < len(r) {
				next := r[i+1]
				if next == 0x07 || next == 0x9c {
					i++
					break
				}
				if next == 0x1b && i+2 < len(r) && r[i+2] == '\\' {
					i += 2
					break
				}
				i++
			}
		case c == 0x1b:
			// Any other escape: it and the character it introduces.
			i++
		case c < 0x20, c == 0x7f, c >= 0x80 && c < 0xa0, unicode.Is(unicode.Cf, c):
			// Format characters too: a bidi override or a zero-width space would show a
			// command in an order other than the one it runs in.
		default:
			b.WriteRune(c)
			col++
		}
	}
	return b.String()
}

// wrap breaks a line into rows of at most width runes, at spaces where it can.
func wrap(line string, width int) []string {
	r := []rune(line)
	if width < 1 || len(r) <= width {
		return []string{line}
	}
	var out []string
	for len(r) > width {
		cut := width
		for i := width; i > 0; i-- {
			if r[i] == ' ' {
				cut = i
				break
			}
		}
		out = append(out, strings.TrimRight(string(r[:cut]), " "))
		r = r[cut:]
		for len(r) > 0 && r[0] == ' ' {
			r = r[1:]
		}
	}
	return append(out, string(r))
}

func truncate(s string, width int) string {
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	return string(r[:width-1]) + "…"
}
