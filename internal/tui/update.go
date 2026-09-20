package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/ecrespo/umbral/internal/tui/ports"
)

// handleKey routes a keypress. The control shortcuts belong to the TUI; everything else
// is the session's input, because the whole point of a terminal is that the program inside
// it receives what was typed.
func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// The status line is a reply to the last thing that happened, so any key clears it.
	m.status = ""

	switch msg.String() {
	case "ctrl+q":
		m.quitting = true
		return m, tea.Quit

	case "ctrl+t":
		return m, m.openTabCmd()

	case "ctrl+n":
		if len(m.tabs) > 0 {
			m.current = (m.current + 1) % len(m.tabs)
			return m, m.loadBlocksForFocus()
		}
		return m, nil

	case "ctrl+s":
		return m, m.split()

	case "ctrl+o":
		if t := m.focusedTab(); t != nil && len(t.panes) > 0 {
			t.focused = (t.focused + 1) % len(t.panes)
			return m, m.loadBlocksForFocus()
		}
		return m, nil

	case "ctrl+b":
		m.showBlocks = !m.showBlocks
		cmds := []tea.Cmd{m.resize(m.width, m.height)}
		if m.showBlocks {
			cmds = append(cmds, m.loadBlocksForFocus())
		}
		return m, tea.Batch(cmds...)

	case "ctrl+p":
		return m, m.jumpBlock(-1)

	case "ctrl+g":
		return m, m.jumpBlock(+1)
	}

	p := m.focusedPane()
	if p == nil || p.exited {
		return m, nil
	}
	if data := keyBytes(msg); len(data) > 0 {
		return m, m.inputCmd(p.sessionID, data)
	}
	return m, nil
}

// keyBytes turns a keypress into what the shell should receive.
//
// Bubble Tea has already decoded the terminal's escape sequences into a key; this turns
// the common ones back into the bytes a PTY expects. It is deliberately small: F0's job is
// a usable terminal for shell work, and the full keyboard — function keys, the Kitty
// protocol, mouse reporting — belongs with the pane tree in T-F0-14 and the agent panel in
// T-F1-20, where there is something to test them against.
func keyBytes(msg tea.KeyPressMsg) []byte {
	switch msg.String() {
	case "enter":
		return []byte("\r")
	case "tab":
		return []byte("\t")
	case "backspace":
		return []byte{0x7f}
	case "delete":
		return []byte("\x1b[3~")
	case "esc":
		return []byte{0x1b}
	case "up":
		return []byte("\x1b[A")
	case "down":
		return []byte("\x1b[B")
	case "right":
		return []byte("\x1b[C")
	case "left":
		return []byte("\x1b[D")
	case "home":
		return []byte("\x1b[H")
	case "end":
		return []byte("\x1b[F")
	case "space":
		return []byte(" ")
	}

	key := tea.Key(msg)
	// A control combination the TUI did not claim goes to the shell: ctrl+c has to reach
	// the program, or a running command could never be interrupted.
	if key.Mod&tea.ModCtrl != 0 && key.Code >= 'a' && key.Code <= 'z' {
		return []byte{byte(key.Code - 'a' + 1)}
	}
	if key.Text != "" {
		return []byte(key.Text)
	}
	return nil
}

// addTab installs a session created for a new tab and attaches to it.
func (m *Model) addTab(msg tabOpenedMsg) tea.Cmd {
	size := m.paneSize(1)
	screen, err := m.screens(size)
	if err != nil {
		m.status = err.Error()
		return nil
	}
	p := &pane{sessionID: msg.session.ID, screen: screen, size: size}
	m.tabs = append(m.tabs, &tab{panes: []*pane{p}})
	m.current = len(m.tabs) - 1
	return m.subscribeCmd(p.sessionID)
}

// attach installs a session created for a split.
func (m *Model) attach(msg paneAttachedMsg) tea.Cmd {
	if msg.tab < 0 || msg.tab >= len(m.tabs) {
		return nil
	}
	t := m.tabs[msg.tab]
	if len(t.panes) >= maxPanesPerTab {
		return nil
	}

	size := m.paneSize(len(t.panes) + 1)
	screen, err := m.screens(size)
	if err != nil {
		m.status = err.Error()
		return nil
	}
	p := &pane{sessionID: msg.session.ID, screen: screen, size: size}
	t.panes = append(t.panes, p)
	t.focused = len(t.panes) - 1

	// The pane that was already there just got narrower.
	cmds := []tea.Cmd{m.subscribeCmd(p.sessionID), m.resize(m.width, m.height)}
	return tea.Batch(cmds...)
}

func (m *Model) split() tea.Cmd {
	t := m.focusedTab()
	if t == nil {
		return nil
	}
	if len(t.panes) >= maxPanesPerTab {
		m.status = fmt.Sprintf("a tab holds %d panes in F0; the pane tree arrives with T-F0-14", maxPanesPerTab)
		return nil
	}
	return m.splitCmd(m.current)
}

// applyBlocks installs a block list, keeping the selection on the block it was on when
// that block is still there. A list that jumped to the newest entry every time a command
// finished would move the selection out from under whoever was reading it.
func (m *Model) applyBlocks(msg blocksLoadedMsg) {
	p := m.paneByID(msg.sessionID)
	if p == nil {
		return
	}
	var selectedID string
	if p.selected >= 0 && p.selected < len(p.blocks) {
		selectedID = p.blocks[p.selected].ID
	}

	p.blocks = msg.blocks
	p.selected = 0
	for i, b := range p.blocks {
		if b.ID == selectedID {
			p.selected = i
			break
		}
	}
}

// jumpBlock moves the selection. The list is newest first, so "previous block" — older —
// is a step towards the end of the slice.
func (m *Model) jumpBlock(direction int) tea.Cmd {
	p := m.focusedPane()
	if p == nil {
		return nil
	}
	if len(p.blocks) == 0 {
		// The list may simply not have been fetched yet, which is the common case before
		// ctrl+b has ever been pressed.
		return m.loadBlocksForFocus()
	}

	next := p.selected - direction
	if next < 0 {
		next = 0
		m.status = "already at the newest block"
	}
	if next >= len(p.blocks) {
		next = len(p.blocks) - 1
		m.status = "already at the oldest block held; `umb block search` finds older ones"
	}
	p.selected = next

	// Jumping is what the block list is for, so make it visible if it is not.
	if !m.showBlocks {
		m.showBlocks = true
		return m.resize(m.width, m.height)
	}
	return nil
}

func (m *Model) loadBlocksForFocus() tea.Cmd {
	p := m.focusedPane()
	if p == nil {
		return nil
	}
	return m.loadBlocksCmd(p.sessionID)
}

// applyEvent folds one daemon event into the model and re-arms the listener.
func (m *Model) applyEvent(msg daemonEventMsg) tea.Cmd {
	ev := msg.event

	switch ev.Kind {
	case ports.EventOutput:
		m.applyOutput(ev)

	case ports.EventBlockClosed:
		// A command finished, so the list changed. Refetching is cheaper to get right
		// than merging: the daemon's ordering is the one the list is defined by.
		if m.showBlocks {
			return tea.Batch(m.loadBlocksCmd(ev.SessionID), m.waitForEvent())
		}

	case ports.EventSessionExited:
		if p := m.paneByID(ev.SessionID); p != nil {
			p.exited = true
		}

	case ports.EventUnsubscribed:
		// The subscription was dropped, not the connection. API Spec §6: subscribe
		// again, and the fresh snapshot carries everything the dropped subscription had
		// not delivered. The seq is reset first, or the new snapshot's own sequence
		// number would be judged against the old stream's.
		if p := m.paneByID(ev.SessionID); p != nil {
			// The screen is emptied before the fresh snapshot arrives, because a
			// snapshot only reproduces the daemon's screen when it is replayed into an
			// empty emulator. Writing it over the pre-drop screen would leave stale rows
			// underneath. `subscribed` also goes back to false, so any chunk that
			// arrives before the new snapshot waits for it instead of being drawn on a
			// blank screen.
			if p.screen != nil {
				if err := p.screen.Reset(); err != nil {
					m.status = err.Error()
				}
			}
			p.lastSeq = 0
			p.subscribed = false
			p.pending = nil
		}
		m.status = "the daemon dropped a subscription; re-attaching"
		return tea.Batch(m.subscribeCmd(ev.SessionID), m.waitForEvent())

	case ports.EventDisconnected:
		m.disconnected = true
		if ev.Err != nil {
			m.status = "lost the daemon: " + ev.Err.Error()
		} else {
			m.status = "lost the daemon"
		}
		// No re-arm: the channel is closed and nothing more will come from it.
		return nil
	}

	return m.waitForEvent()
}

// maxPendingChunks caps what a pane holds while it waits for its snapshot. The window is
// milliseconds wide, so anything past this is a snapshot that is not coming; dropping then
// is better than growing, and the screen is correct again as soon as one arrives.
const maxPendingChunks = 256

// applyOutput feeds a chunk to the pane's screen, or holds it until the snapshot lands.
func (m *Model) applyOutput(ev ports.Event) {
	p := m.paneByID(ev.SessionID)
	if p == nil || p.screen == nil {
		return
	}
	if !p.subscribed {
		if len(p.pending) < maxPendingChunks {
			p.pending = append(p.pending, ev)
		}
		return
	}
	// A chunk the snapshot already contained would print twice. The daemon promises not
	// to send one (REQ-API-002), and the client checks rather than trusts, because the
	// consequence is visible corruption of the screen rather than an error.
	if ev.Seq != 0 && ev.Seq <= p.lastSeq {
		return
	}
	if _, err := p.screen.Write(ev.Data); err != nil {
		m.status = err.Error()
		return
	}
	if ev.Seq > p.lastSeq {
		p.lastSeq = ev.Seq
	}
}

// applySubscription installs a snapshot: the screen as the daemon has it, and the sequence
// number everything after it is measured against.
func (m *Model) applySubscription(msg subscribedMsg) {
	p := m.paneByID(msg.sessionID)
	if p == nil || p.screen == nil {
		return
	}
	if _, err := p.screen.Write(msg.snapshot); err != nil {
		m.status = err.Error()
		return
	}
	p.lastSeq = msg.seq
	p.subscribed = true

	// Whatever arrived while the snapshot was in flight, now in order and measured
	// against the seq the snapshot is current as of.
	pending := p.pending
	p.pending = nil
	for _, ev := range pending {
		m.applyOutput(ev)
	}
}

// sessionIDs reports every session the model is showing, for the tests.
func (m *Model) sessionIDs() []string {
	var out []string
	for _, t := range m.tabs {
		for _, p := range t.panes {
			out = append(out, p.sessionID)
		}
	}
	return out
}
