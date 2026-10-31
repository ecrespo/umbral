package tui

import (
	"sync"

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

// createThreadCmd starts the panel's thread in the focused pane's directory, in the focused
// workspace (REQ-ORC-003).
func (m *Model) createThreadCmd(cwd, workspaceID string) tea.Cmd {
	daemon := m.daemon
	return func() tea.Msg {
		ctx, cancel := withTimeout()
		defer cancel()

		id, err := daemon.CreateThread(ctx, cwd, workspaceID)
		if err != nil {
			return threadFailedMsg{err: err}
		}
		return threadCreatedMsg{threadID: id}
	}
}

// focusOrder keeps thread.focus calls in the order the panel made them. Bubble Tea runs
// commands concurrently, so a focus made before an unfocus could otherwise reach the daemon
// after it and leave a view the panel no longer has.
type focusOrder struct {
	mu sync.Mutex
	// made numbers the calls as they are made; sent is the newest one sent.
	made, sent uint64
}

// focusCmd tells the daemon which thread this client views, "" for none. The calls are sent
// one at a time, and one that finds a newer call already sent sends nothing. A failure is not
// reported: a failed focus leaves the thread `done`, which an unviewed thread shows anyway,
// and a failed unfocus leaves the view until the next one or until the TUI disconnects.
func (m *Model) focusCmd(threadID string) tea.Cmd {
	daemon, order := m.daemon, m.focus
	order.mu.Lock()
	order.made++
	n := order.made
	order.mu.Unlock()
	return func() tea.Msg {
		order.mu.Lock()
		defer order.mu.Unlock()
		if n < order.sent {
			return nil
		}
		order.sent = n
		ctx, cancel := withTimeout()
		defer cancel()

		_ = daemon.Focus(ctx, threadID)
		return nil
	}
}

// sendCmd sends a message to the panel's thread.
func (m *Model) sendCmd(threadID string, out outgoing) tea.Cmd {
	daemon := m.daemon
	return func() tea.Msg {
		ctx, cancel := withTimeout()
		defer cancel()

		_, err := daemon.Send(ctx, threadID, out.text, out.attachments)
		return sentMsg{err: err}
	}
}

// respondCmd answers an approval.
func (m *Model) respondCmd(apr ports.Approval, decision, scope string) tea.Cmd {
	daemon := m.daemon
	return func() tea.Msg {
		ctx, cancel := withTimeout()
		defer cancel()

		if err := daemon.Respond(ctx, apr.ID, decision, scope); err != nil {
			return respondFailedMsg{approval: apr, err: err}
		}
		return nil
	}
}

// cancelCmd stops a thread's running turn.
func (m *Model) cancelCmd(threadID string) tea.Cmd {
	daemon := m.daemon
	return func() tea.Msg {
		ctx, cancel := withTimeout()
		defer cancel()

		stopped, err := daemon.Cancel(ctx, threadID)
		if err != nil {
			return errorMsg{err: err}
		}
		return cancelledMsg{threadID: threadID, stopped: stopped}
	}
}

// loadApprovalsCmd lists the approvals already pending when the TUI starts.
func (m *Model) loadApprovalsCmd() tea.Cmd {
	daemon := m.daemon
	return func() tea.Msg {
		ctx, cancel := withTimeout()
		defer cancel()

		list, err := daemon.Approvals(ctx)
		return approvalsLoadedMsg{approvals: list, err: err}
	}
}

// loadThreadsCmd lists the threads and the focused workspace (REQ-ORC-003).
func (m *Model) loadThreadsCmd() tea.Cmd {
	daemon, conn := m.daemon, m.agent.conn
	return func() tea.Msg {
		ctx, cancel := withTimeout()
		defer cancel()

		list, err := daemon.Threads(ctx)
		return threadsLoadedMsg{list: list, err: err, conn: conn}
	}
}

// loadExtensionsCmd lists both of the extensions view's lists, which every opening of the
// panel does: removing a server publishes nothing (Tech §5.3e).
func (m *Model) loadExtensionsCmd() tea.Cmd { return tea.Batch(m.loadServersCmd(), m.loadSkillsCmd()) }

// loadServersCmd and loadSkillsCmd list the extensions view's MCP servers and skills.
func (m *Model) loadServersCmd() tea.Cmd {
	daemon := m.daemon
	return func() tea.Msg {
		ctx, cancel := withTimeout()
		defer cancel()

		list, err := daemon.McpServers(ctx)
		return serversLoadedMsg{servers: list, err: err}
	}
}

func (m *Model) loadSkillsCmd() tea.Cmd {
	daemon := m.daemon
	return func() tea.Msg {
		ctx, cancel := withTimeout()
		defer cancel()

		list, err := daemon.Skills(ctx)
		return skillsLoadedMsg{skills: list, err: err}
	}
}
