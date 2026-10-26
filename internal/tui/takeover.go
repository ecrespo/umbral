package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
)

// The take-over (REQ-FTU-002): ctrl+] in the agent panel gives the user the selected thread's
// terminal, the PTY its run_command and terminal_interact calls use, and shows it in place of
// the panel. Every key but ctrl+] and ctrl+q goes to that session — ctrl+c too, because the
// program the agent left running is the user's to stop now — and ctrl+] again hands it back
// (delta `2026-10-terminal-interact`, decision 6).

// keyTakeOver is the chord. A terminal sends ctrl+] as GS, which no shell binds by default.
const keyTakeOver = "ctrl+]"

type (
	takenOverMsg struct {
		threadID  string
		sessionID string
	}
	handedBackMsg struct{ err error }
)

// takeOverKey is ctrl+]: hand the held terminal back, or take the panel's thread's.
func (m *Model) takeOverKey() tea.Cmd {
	if m.held != nil {
		return m.handBack()
	}
	if !m.agent.mode {
		return nil
	}
	if m.agent.threadID == "" {
		m.status = "no thread selected: up and down select one"
		return nil
	}
	return m.takeOverCmd(m.agent.threadID)
}

// tookOver installs the thread's session in place of the panel and subscribes to it.
func (m *Model) tookOver(msg takenOverMsg) tea.Cmd {
	if m.held != nil || msg.threadID != m.agent.threadID {
		// Taken over twice, or the panel moved to another thread while the call was in
		// flight: the daemon holds the terminal for the user either way, so give it back.
		return m.handBackCmd(msg.threadID)
	}
	size := m.heldSize()
	screen, err := m.screens(size)
	if err != nil {
		m.status = err.Error()
		return m.handBackCmd(msg.threadID)
	}
	m.held = &pane{sessionID: msg.sessionID, screen: screen, size: size}
	m.heldThread = msg.threadID
	return tea.Batch(m.subscribeCmd(msg.sessionID), m.resizeSessionCmd(msg.sessionID, size))
}

// handBack drops the held view at once, so no further key reaches the terminal, and tells the
// daemon.
func (m *Model) handBack() tea.Cmd {
	threadID := m.heldThread
	if m.held.screen != nil {
		_ = m.held.screen.Close()
	}
	m.held, m.heldThread = nil, ""
	return m.handBackCmd(threadID)
}

// heldSize is the panel's column, less the header row: the held terminal takes its place.
func (m *Model) heldSize() sessdomain.Size {
	return clampSize(m.agentPanelWidth(), m.height-3)
}

// heldKey sends a key to the held terminal.
func (m *Model) heldKey(msg tea.KeyPressMsg) tea.Cmd {
	if m.held.exited {
		return nil
	}
	if data := keyBytes(msg); len(data) > 0 {
		return m.inputCmd(m.held.sessionID, data)
	}
	return nil
}

// renderHeld is the held terminal under a line naming whose it is.
func (m *Model) renderHeld() []string {
	lines := m.renderPane(m.held)
	header := fmt.Sprintf("%s's terminal · ctrl+] hand it back", m.heldThread)
	return append([]string{truncate(header, int(m.held.size.Cols))}, lines...)
}

func (m *Model) takeOverCmd(threadID string) tea.Cmd {
	daemon := m.daemon
	return func() tea.Msg {
		ctx, cancel := withTimeout()
		defer cancel()

		sessionID, err := daemon.TakeOver(ctx, threadID)
		if err != nil {
			return errorMsg{err: err}
		}
		return takenOverMsg{threadID: threadID, sessionID: sessionID}
	}
}

func (m *Model) handBackCmd(threadID string) tea.Cmd {
	daemon := m.daemon
	return func() tea.Msg {
		ctx, cancel := withTimeout()
		defer cancel()

		return handedBackMsg{err: daemon.HandBack(ctx, threadID)}
	}
}
