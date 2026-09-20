package tui

import (
	tea "charm.land/bubbletea/v2"

	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/tui/ports"
)

// The messages the model sends itself. Every daemon call happens in a tea.Cmd and comes
// back as one of these, so Update stays the only place state changes and nothing blocks
// the redraw.
type (
	// tabOpenedMsg carries a session created for a brand-new tab.
	tabOpenedMsg struct{ session sessdomain.Session }
	// paneAttachedMsg carries a session created for a split, plus the tab it belongs to.
	paneAttachedMsg struct {
		session sessdomain.Session
		tab     int
	}
	// subscribedMsg carries the snapshot a pane starts from (REQ-TERM-004).
	subscribedMsg struct {
		sessionID string
		snapshot  []byte
		seq       uint64
	}
	// blocksLoadedMsg carries a pane's block list.
	blocksLoadedMsg struct {
		sessionID string
		blocks    []sessdomain.Block
	}
	// daemonEventMsg carries one notification, already flattened by the adapter.
	daemonEventMsg struct{ event ports.Event }
	// errorMsg is anything that went wrong, shown on the status line rather than
	// crashing the program: a TUI that exited on a failed call would take the user's
	// other sessions down with it.
	errorMsg struct{ err error }
)

// waitForEvent blocks on the daemon's event channel in a command, which is how Bubble Tea
// takes input from something other than the keyboard. It re-arms itself in Update, so
// there is exactly one of these outstanding at a time.
func (m *Model) waitForEvent() tea.Cmd {
	events := m.daemon.Events()
	return func() tea.Msg {
		ev, ok := <-events
		if !ok {
			return daemonEventMsg{event: ports.Event{Kind: ports.EventDisconnected}}
		}
		return daemonEventMsg{event: ev}
	}
}

// openTabCmd creates the session a new tab shows.
func (m *Model) openTabCmd() tea.Cmd {
	size := m.paneSize(1)
	daemon := m.daemon
	return func() tea.Msg {
		ctx, cancel := withTimeout()
		defer cancel()

		s, err := daemon.CreateSession(ctx, size)
		if err != nil {
			return errorMsg{err: err}
		}
		return tabOpenedMsg{session: s}
	}
}

// splitCmd creates the session a second pane shows.
func (m *Model) splitCmd(tabIndex int) tea.Cmd {
	size := m.paneSize(maxPanesPerTab)
	daemon := m.daemon
	return func() tea.Msg {
		ctx, cancel := withTimeout()
		defer cancel()

		s, err := daemon.CreateSession(ctx, size)
		if err != nil {
			return errorMsg{err: err}
		}
		return paneAttachedMsg{session: s, tab: tabIndex}
	}
}

// subscribeCmd attaches to a session and fetches the screen as it stands.
func (m *Model) subscribeCmd(sessionID string) tea.Cmd {
	daemon := m.daemon
	return func() tea.Msg {
		ctx, cancel := withTimeout()
		defer cancel()

		snapshot, seq, err := daemon.Subscribe(ctx, sessionID)
		if err != nil {
			return errorMsg{err: err}
		}
		return subscribedMsg{sessionID: sessionID, snapshot: snapshot, seq: seq}
	}
}

// loadBlocksCmd fetches a pane's block list.
func (m *Model) loadBlocksCmd(sessionID string) tea.Cmd {
	daemon := m.daemon
	return func() tea.Msg {
		ctx, cancel := withTimeout()
		defer cancel()

		blocks, err := daemon.Blocks(ctx, sessionID, blockListLimit)
		if err != nil {
			return errorMsg{err: err}
		}
		return blocksLoadedMsg{sessionID: sessionID, blocks: blocks}
	}
}

// inputCmd sends keystrokes to the focused session.
func (m *Model) inputCmd(sessionID string, data []byte) tea.Cmd {
	daemon := m.daemon
	return func() tea.Msg {
		ctx, cancel := withTimeout()
		defer cancel()

		if err := daemon.Input(ctx, sessionID, data); err != nil {
			return errorMsg{err: err}
		}
		return nil
	}
}

// resizeSessionCmd tells the daemon a pane changed size (REQ-TERM-007).
func (m *Model) resizeSessionCmd(sessionID string, size sessdomain.Size) tea.Cmd {
	daemon := m.daemon
	return func() tea.Msg {
		ctx, cancel := withTimeout()
		defer cancel()

		if err := daemon.Resize(ctx, sessionID, size); err != nil {
			return errorMsg{err: err}
		}
		return nil
	}
}
