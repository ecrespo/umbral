package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/ecrespo/umbral/internal/tui/ports"
)

// The agent panel (REQ-TUI-001's agent half, REQ-TUI-002, REQ-TUI-003, REQ-TUI-004): a column
// beside the panes that shows what the agent can reach beyond its built-in tools, the focused
// workspace's threads (REQ-ORC-003), the selected
// thread's turn as it streams, the approvals pending on every thread, and the line the user
// is typing to it. ctrl+space moves input between the shell and the
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

// threadView is what the panel shows of one thread: its transcript and its turn. The
// selected thread's is the panel's own; every other thread's is kept in agentPanel.views, so
// switching back finds what arrived meanwhile.
type threadView struct {
	running  bool
	thinking bool
	entries  []entry
	// turnTools and turnSkills are the MCP tools the running turn called and the skills it
	// loaded, each once, in the order first used.
	turnTools  []string
	turnSkills []string
}

// agentPanel is the panel's state. The model owns one. Its thread is the selected one: the
// first message creates one when none is selected, and up and down select another of the
// focused workspace's threads (REQ-ORC-003).
type agentPanel struct {
	open bool
	// mode routes the keyboard to the panel instead of the focused shell (REQ-TUI-002).
	mode bool

	threadView
	threadID string
	// views are the transcripts of the threads not selected, by thread id.
	views map[string]*threadView
	// workspace is the daemon's focused workspace, "" for none, and threads every thread it
	// listed with it; the panel lists those of that workspace.
	workspace string
	threads   []ports.Thread
	// listedSeq is the snapshot `seq` of the listing applied; an older one arriving later is
	// stale and dropped. conn counts the connections this client has had: a seq is only
	// comparable within one daemon's run, so a listing asked on an earlier connection is
	// dropped whatever its seq (API §6).
	listedSeq uint64
	conn      int
	// viewing is the thread the daemon was last told this client views: the panel's own
	// while the panel is on screen, none otherwise (REQ-AGT-016).
	viewing  string
	creating bool
	// queued is a message typed before the thread existed, sent once it does.
	queued *outgoing

	input []rune

	// approvals are every thread's, answered oldest first, one chord each. answered keeps
	// what was answered here, so a late approval.list cannot bring one back.
	approvals []ports.Approval
	answered  map[string]bool
	showDiff  bool

	// servers and skills are the extensions view (REQ-TUI-004), listed at start, whenever
	// the panel opens and on every mcp.server_state or skill.changed.
	servers []ports.McpServer
	skills  []ports.Skill
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
	serversLoadedMsg struct {
		servers []ports.McpServer
		err     error
	}
	skillsLoadedMsg struct {
		skills []ports.Skill
		err    error
	}
	threadsLoadedMsg struct {
		list ports.ThreadList
		err  error
		// conn is the connection the listing was asked on (agentState.conn).
		conn int
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
		return tea.Batch(m.resize(m.width, m.height), m.syncView(), m.loadExtensionsCmd(), m.loadThreadsCmd())
	}
	return nil
}

// closePanel hides the panel and gives the keyboard back to the shell.
func (m *Model) closePanel() tea.Cmd {
	m.agent.open, m.agent.mode = false, false
	return tea.Batch(m.resize(m.width, m.height), m.syncView())
}

// syncView tells the daemon which thread this client views, when that changed: a thread the
// panel shows is seen, so its finished turns are not left `done` (REQ-AGT-016).
func (m *Model) syncView() tea.Cmd {
	a := &m.agent
	want := ""
	if a.open {
		want = a.threadID
	}
	if want == a.viewing {
		return nil
	}
	a.viewing = want
	return m.focusCmd(want)
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
		return tea.Batch(m.resize(m.width, m.height), m.syncView(), m.loadExtensionsCmd(), m.loadThreadsCmd())
	}
	return nil
}

// acceptSuggestion is alt+s on the focused pane's suggestion (REQ-AAI-002): the panel opens in
// agent mode on a new thread of the focused workspace, created at once, with the block and the
// suggestion in the input as a draft. Nothing is sent.
func (m *Model) acceptSuggestion(p *pane) tea.Cmd {
	a := &m.agent
	if a.creating {
		m.status = "a thread is being created: wait for it"
		return nil
	}
	sg := p.suggestion
	p.suggestion = nil
	if a.threadID != "" {
		kept := a.threadView
		if a.views == nil {
			a.views = map[string]*threadView{}
		}
		a.views[a.threadID] = &kept
	}
	a.threadView, a.threadID, a.queued = threadView{}, "", nil
	a.input = []rune("@block:" + sg.blockID + " " + inert(oneLine(sg.text)))
	a.mode, a.creating = true, true
	cmds := []tea.Cmd{m.createThreadCmd(p.cwd, a.workspace), m.syncView()}
	if !a.open {
		a.open = true
		cmds = append(cmds, m.resize(m.width, m.height), m.loadExtensionsCmd(), m.loadThreadsCmd())
	}
	return tea.Batch(cmds...)
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
	case "up":
		return m.selectThread(-1)
	case "down":
		return m.selectThread(+1)
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
	return m.createThreadCmd(cwd, a.workspace)
}

// threadCreated installs the thread and sends what was waiting for it.
func (m *Model) threadCreated(msg threadCreatedMsg) tea.Cmd {
	a := &m.agent
	a.threadID, a.creating = msg.threadID, false
	view := tea.Batch(m.syncView(), m.loadThreadsCmd())
	if a.queued == nil {
		return view
	}
	out := *a.queued
	a.queued = nil
	return tea.Batch(view, m.sendCmd(a.threadID, out))
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
	if msg.threadID != a.threadID {
		if v, ok := a.views[msg.threadID]; ok {
			v.running, v.thinking = false, false
		}
		return
	}
	if !a.running {
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
		return tea.Batch(m.resize(m.width, m.height), m.loadExtensionsCmd())
	}
	return nil
}

// threadsLoaded replaces the listing. A failed one keeps the last and says so.
//
// Listings run concurrently, so one older than the listing applied is dropped. A thread the
// listing shows running is running in its view too, whoever started the turn; one shown idle
// is left to its own turn's end, since a send of this panel may not be listed yet. The views
// of threads no longer listed are dropped, which bounds them: an ephemeral thread, `umb ai`'s,
// is never listed.
func (m *Model) threadsLoaded(msg threadsLoadedMsg) {
	a := &m.agent
	if msg.err != nil {
		m.status = "threads: " + msg.err.Error()
		return
	}
	if msg.conn != a.conn || msg.list.Seq < a.listedSeq {
		return
	}
	a.listedSeq = msg.list.Seq
	a.workspace, a.threads = msg.list.WorkspaceID, msg.list.Threads
	for id := range a.views {
		if _, ok := m.listed(id); !ok {
			delete(a.views, id)
		}
	}
	for id, v := range a.views {
		if m.listedRunning(id) {
			v.running = true
		}
	}
	if a.threadID != "" && m.listedRunning(a.threadID) {
		a.running = true
	}
}

// shownThreads are the threads the panel lists: the focused workspace's, or with none focused
// those of no workspace (REQ-ORC-003), in the daemon's order, most recently updated first.
// The selected thread is listed even when it belongs elsewhere or is not listed yet.
func (m *Model) shownThreads() []ports.Thread {
	a := &m.agent
	var shown []ports.Thread
	selected := false
	for _, t := range a.threads {
		if t.ID == a.threadID {
			selected = true
			shown = append(shown, t)
			continue
		}
		if t.WorkspaceID == a.workspace {
			shown = append(shown, t)
		}
	}
	if !selected && a.threadID != "" {
		shown = append(shown, ports.Thread{ID: a.threadID, WorkspaceID: a.workspace})
	}
	return shown
}

// selectThread is up or down in agent mode: the panel moves to the previous or next listed
// thread, which becomes the one viewed, shown and sent to. With none selected, down takes the
// first and up the last.
func (m *Model) selectThread(step int) tea.Cmd {
	a := &m.agent
	shown := m.shownThreads()
	if len(shown) == 0 {
		return nil
	}
	if a.creating || a.queued != nil {
		m.status = "the thread is being created: wait for it"
		return nil
	}
	at := slices.IndexFunc(shown, func(t ports.Thread) bool { return t.ID == a.threadID })
	switch {
	case at < 0 && step > 0:
		at = 0
	case at < 0:
		at = len(shown) - 1
	default:
		at = (at + step + len(shown)) % len(shown)
	}
	return m.switchTo(shown[at].ID)
}

// switchTo selects a thread: the current view is kept, and the selected thread's is the one
// it last had, or a fresh one that knows from the listing whether a turn is running.
func (m *Model) switchTo(threadID string) tea.Cmd {
	a := &m.agent
	if threadID == a.threadID {
		return nil
	}
	if a.threadID != "" {
		kept := a.threadView
		if a.views == nil {
			a.views = map[string]*threadView{}
		}
		a.views[a.threadID] = &kept
	}
	a.threadView = *m.viewOf(threadID)
	delete(a.views, threadID)
	a.threadID = threadID
	return m.syncView()
}

// threadLines are the thread list's rows: the selected one marked, each with its attention
// state, `queued`, its worktree, the external agent hosting it and `unobserved`, and its
// pending approvals, at most maxThreadRows of them. A selected hosted thread adds a line saying
// what Umbral does not record of it (REQ-ACP-007).
func (m *Model) threadLines(width int) []string {
	shown := m.shownThreads()
	if len(shown) == 0 {
		return nil
	}
	pending := map[string]int{}
	for _, apr := range m.agent.approvals {
		pending[apr.ThreadID]++
	}
	var rows []string
	for i, t := range shown {
		if i == maxThreadRows {
			rows = append(rows, fmt.Sprintf("  … %d more", len(shown)-i))
			break
		}
		mark := "  "
		if t.ID == m.agent.threadID {
			mark = "> "
		}
		attention := t.Attention
		if attention == "" {
			attention = "unknown"
		}
		parts := []string{t.ID, attention}
		if t.Agent != "" {
			parts = append(parts, oneLine(t.Agent)+" "+oneLine(t.EgressVisibility))
		}
		if t.Queued {
			parts = append(parts, "queued")
		}
		if t.WorktreeID != "" {
			parts = append(parts, t.WorktreeID)
		} else {
			parts = append(parts, "no worktree")
		}
		switch n := pending[t.ID]; n {
		case 0:
		case 1:
			parts = append(parts, "1 approval")
		default:
			parts = append(parts, fmt.Sprintf("%d approvals", n))
		}
		if title := oneLine(t.Title); title != "" {
			parts = append(parts, title)
		}
		rows = append(rows, truncate(mark+strings.Join(parts, " · "), width))
	}
	for _, t := range shown {
		if t.ID == m.agent.threadID && t.Agent != "" {
			for _, line := range wrap(oneLine(t.Agent)+" runs this thread: Umbral does not record its own network traffic or file reads; "+oneLine(t.Agent)+" can read the repository's main copy and every other thread's worktree", width-2) {
				rows = append(rows, "  "+line)
			}
		}
	}
	return rows
}

// maxThreadRows bounds the thread list, so the transcript keeps the panel's height.
const maxThreadRows = 6

// serversLoaded and skillsLoaded replace the extensions view's lists. A failed listing
// keeps the last one and says so: the daemon is answering, only not this.
func (m *Model) serversLoaded(msg serversLoadedMsg) {
	if msg.err != nil {
		m.status = "mcp servers: " + msg.err.Error()
		return
	}
	m.agent.servers = msg.servers
}

func (m *Model) skillsLoaded(msg skillsLoadedMsg) {
	if msg.err != nil {
		m.status = "skills: " + msg.err.Error()
		return
	}
	m.agent.skills = msg.skills
}

// applyAgentEvent folds one of the agent's notifications into the panel. Approvals are
// every thread's, and a turn's end takes its thread's away. A transcript event goes to its
// thread's view, the panel's own when it is the selected thread. It returns the listing to
// run again, when the event changed what the list shows (REQ-ORC-003).
func (m *Model) applyAgentEvent(ev ports.Event) tea.Cmd {
	a := &m.agent
	switch ev.Kind {
	case ports.EventApprovalRequested:
		m.addApproval(ev.Approval)
		return m.loadThreadsCmd()

	case ports.EventAttentionChanged, ports.EventWorkspaceFocused:
		return m.loadThreadsCmd()

	case ports.EventTurnFinished:
		// A turn that ended leaves nothing of its own to answer: a cancel expires what was
		// waiting (API §5.25), and answering it would be a CONFLICT.
		m.dropApprovals(ev.ThreadID)
	}
	if ev.ThreadID == "" {
		return nil
	}

	// A held turn that started announces nothing but its first event: list again to drop
	// its `queued` mark.
	var relist tea.Cmd
	if ev.Kind == ports.EventTurnFinished || m.listedQueued(ev.ThreadID) {
		relist = m.loadThreadsCmd()
	}
	if ev.ThreadID == a.threadID {
		a.apply(ev)
	} else {
		m.viewOf(ev.ThreadID).apply(ev)
	}
	return relist
}

// viewOf is a thread's kept view, made when the panel first hears of it.
func (m *Model) viewOf(threadID string) *threadView {
	a := &m.agent
	if a.views == nil {
		a.views = map[string]*threadView{}
	}
	v, ok := a.views[threadID]
	if !ok {
		v = &threadView{running: m.listedRunning(threadID)}
		a.views[threadID] = v
	}
	return v
}

// listedRunning and listedQueued read the thread's last listing.
func (m *Model) listedRunning(threadID string) bool {
	t, ok := m.listed(threadID)
	return ok && (t.State == "running" || t.State == "awaiting_approval")
}

func (m *Model) listedQueued(threadID string) bool {
	t, ok := m.listed(threadID)
	return ok && t.Queued
}

func (m *Model) listed(threadID string) (ports.Thread, bool) {
	for _, t := range m.agent.threads {
		if t.ID == threadID {
			return t, true
		}
	}
	return ports.Thread{}, false
}

// apply folds a transcript event into one thread's view.
func (a *threadView) apply(ev ports.Event) {
	switch ev.Kind {
	case ports.EventThreadDelta:
		// Deltas and tool calls come only from a running turn, which this panel may not
		// have started.
		a.running = true
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
		a.running, a.thinking = true, false
		// An MCP tool was called when its call reached the server, whatever it answered; a
		// skill was loaded only when skill_load succeeded. A call refused or malformed did
		// neither, and a failed skill_load may name a skill that does not exist.
		switch {
		case strings.HasPrefix(ev.Tool, "mcp_") && (ev.Status == statusOK || ev.Status == "error"):
			a.turnTools = appendOnce(a.turnTools, ev.Tool)
		case ev.Tool == "skill_load" && ev.Status == statusOK && ev.Skill != "":
			a.turnSkills = appendOnce(a.turnSkills, oneLine(ev.Skill))
		}
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
		if len(a.turnTools) > 0 || len(a.turnSkills) > 0 {
			a.entries = append(a.entries, entry{kind: entryNote, text: "mcp tools: " + listOrNone(a.turnTools) + " · skills loaded: " + listOrNone(a.turnSkills)})
		}
		a.turnTools, a.turnSkills = nil, nil
		a.entries = append(a.entries, entry{kind: entryNote, text: "turn ended: " + ev.StopReason})
	}
}

func appendOnce(list []string, s string) []string {
	if slices.Contains(list, s) {
		return list
	}
	return append(list, s)
}

func listOrNone(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	return strings.Join(list, ", ")
}

// statusOK is API §4's ToolCall status of a call that succeeded.
const statusOK = "ok"

// extensionRows bounds each of the extensions view's lists, so 64 skills cannot push the
// transcript off the panel; what does not fit is counted as `… n more`.
const extensionRows = 2

// extensionLines is the extensions view (REQ-TUI-004): every MCP server with its state,
// and its last error when it has one, and every skill, on or off.
func (m *Model) extensionLines(width int) []string {
	a := &m.agent
	servers := make([]string, 0, len(a.servers))
	for _, s := range a.servers {
		line := oneLine(s.Name) + " " + oneLine(s.State)
		if s.LastError != "" {
			line += " (" + truncate(oneLine(s.LastError), 40) + ")"
		}
		servers = append(servers, line)
	}
	skills := make([]string, 0, len(a.skills))
	for _, s := range a.skills {
		state := "off"
		if s.Enabled {
			state = "on"
		}
		skills = append(skills, oneLine(s.Name)+" "+state)
	}
	return append(wrapItems("mcp: ", servers, width), wrapItems("skills: ", skills, width)...)
}

// oneLine is text a remote side or the model chose, made safe for one row: sanitized, then
// every run of whitespace, newlines included, one space.
func oneLine(s string) string { return strings.Join(strings.Fields(sanitize(s)), " ") }

// itemRow is one row of wrapItems: a prefix and the items on it.
type itemRow struct {
	prefix string
	items  []string
}

func (r itemRow) String() string { return r.prefix + strings.Join(r.items, " · ") }

func (r itemRow) with(item string) itemRow {
	return itemRow{prefix: r.prefix, items: append(slices.Clone(r.items), item)}
}

// wrapItems lays items out after label, separated by " · ", breaking rows between items so
// that a name is never split from its state, in at most extensionRows rows.
func wrapItems(label string, items []string, width int) []string {
	if len(items) == 0 {
		items = []string{"none"}
	}
	runes := func(s string) int { return len([]rune(s)) }
	rows := []itemRow{{prefix: label}}
	for _, item := range items {
		item = truncate(item, max(width-runes(label), 1))
		last := &rows[len(rows)-1]
		if next := last.with(item); len(last.items) == 0 || runes(next.String()) <= width {
			*last = next
			continue
		}
		rows = append(rows, itemRow{prefix: "  ", items: []string{item}})
	}
	if len(rows) > extensionRows {
		shown := 0
		for _, r := range rows[:extensionRows] {
			shown += len(r.items)
		}
		rows = rows[:extensionRows]
		last := &rows[extensionRows-1]
		for {
			next := last.with(fmt.Sprintf("… %d more", len(items)-shown))
			if len(last.items) == 0 || runes(next.String()) <= width {
				*last = next
				break
			}
			last.items = last.items[:len(last.items)-1]
			shown--
		}
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, truncate(r.String(), width))
	}
	return out
}

// attachmentPrefixes are the input's tokens for thread.send's attachment kinds:
// REQ-CTX-002's `@directory`, and `@dir` for short.
var attachmentPrefixes = []struct{ prefix, kind string }{
	{"@block:", "block"}, {"@file:", "file"}, {"@directory:", "dir"}, {"@dir:", "dir"},
}

// inert is model text placed in the input with no attachment token of its own: a suggestion
// was written by a model that read a command's output, and `@file:~/.ssh/id_rsa` in it must not
// attach a file when the user sends the draft. The `@` of such a word is followed by a space.
func inert(text string) string {
	words := strings.Fields(text)
	for i, w := range words {
		if len(parseAttachments(w)) > 0 {
			words[i] = "@ " + strings.TrimPrefix(w, "@")
		}
	}
	return strings.Join(words, " ")
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
	top := append(append([]string{truncate(header, width)}, m.extensionLines(width)...), m.threadLines(width)...)
	room = max(height-len(top)-len(bottom), 0)
	if len(body) > room {
		body = body[len(body)-room:]
	}

	rows := append(top, body...)
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
