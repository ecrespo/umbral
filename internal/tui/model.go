// Package tui is the Bubble Tea v2 client: tabs, splits, a block list and the keys that
// move between them (REQ-TUI-001).
//
// It knows no wire format. Everything it needs from `umbrald` is the narrow `ports.Daemon`
// interface, and everything it draws comes from `ports.Screen`; `cmd/umbral-tui` picks the
// implementations, the same way `cmd/umbrald` wires the daemon's adapters.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/tui/ports"
)

// callTimeout bounds one request to the daemon. A TUI that blocked forever on a wedged
// daemon would stop redrawing, which looks like a hung terminal rather than a hung daemon.
const callTimeout = 5 * time.Second

// Layout limits. Two panes per tab is what "splits" means in F0: the pane tree arrives
// with T-F0-14, and building half of it here would be work thrown away.
const (
	maxPanesPerTab = 2
	// blockListWidth is the block list's column when it is open.
	blockListWidth = 34
	// blockListLimit is how many blocks the list holds. It is a screenful, not a
	// history: `block.search` is what finds an old one.
	blockListLimit = 200
)

// Model is the whole client.
type Model struct {
	daemon  ports.Daemon
	screens ports.ScreenFactory

	tabs    []*tab
	current int

	width, height int

	// showBlocks toggles the block list column.
	showBlocks bool

	// status is the one-line message under the panes: what just happened, or what went
	// wrong. Empty means the key hints are shown instead.
	status string

	// disconnected is set when the stream ends. The panes keep their last screen rather
	// than going blank, because a frozen screen with a banner is more useful than an
	// empty one.
	disconnected bool

	quitting bool
}

// tab is one screenful: up to two panes and which of them has focus.
type tab struct {
	panes   []*pane
	focused int
}

// pane is one session, its screen, and the block list that belongs to it.
type pane struct {
	sessionID string
	screen    ports.Screen
	size      sessdomain.Size

	blocks   []sessdomain.Block
	selected int

	// lastSeq is the highest `session.output` sequence applied. A chunk at or below it
	// is one the snapshot already contained (REQ-API-002), and applying it again would
	// print the same bytes twice.
	lastSeq uint64

	// subscribed says whether the snapshot has arrived. Until it has, output is held in
	// pending rather than drawn.
	//
	// The daemon writes the subscribe reply before any notification (REQ-TERM-004), but
	// the two reach this model as independent Bubble Tea messages produced by different
	// goroutines, so the wire order is not the arrival order. Drawing a chunk first and
	// then the snapshot would overwrite live output with an older screen, which is the
	// client-side mirror of the gap T-F0-06 had on the daemon side.
	subscribed bool
	pending    []ports.Event

	exited bool
}

// New builds the model. It does not talk to the daemon: Init does, so that a failure is a
// message the model can show rather than an error before the program starts.
func New(daemon ports.Daemon, screens ports.ScreenFactory) *Model {
	return &Model{
		daemon:  daemon,
		screens: screens,
		// A sensible size until the terminal reports its own, so the first session is
		// created with something valid rather than 0x0.
		width:  80,
		height: 24,
	}
}

// Init opens the first session and starts listening for daemon events.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.openTabCmd(), m.waitForEvent())
}

// Update is the single place the model changes.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m, m.resize(msg.Width, msg.Height)

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tabOpenedMsg:
		return m, m.addTab(msg)

	case paneAttachedMsg:
		return m, m.attach(msg)

	case subscribedMsg:
		m.applySubscription(msg)
		return m, nil

	case blocksLoadedMsg:
		m.applyBlocks(msg)
		return m, nil

	case daemonEventMsg:
		return m, m.applyEvent(msg)

	case errorMsg:
		m.status = msg.err.Error()
		return m, nil
	}
	return m, nil
}

// View draws the frame: the tab bar, the panes side by side, the block list if it is open,
// and the status line.
func (m *Model) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}

	var b strings.Builder
	b.WriteString(m.renderTabBar())
	b.WriteByte('\n')

	body := m.renderBody()
	b.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString(m.renderStatus())

	v := tea.NewView(b.String())
	// The alternate screen, so quitting leaves the shell's scrollback as it was rather
	// than strewn with a session's output. It is a property of the view in Bubble Tea v2
	// rather than a program option.
	v.AltScreen = true
	return v
}

// CurrentTab reports which tab has focus. It exists for the tests, which have no screen to
// look at and should assert on state rather than on rendered text.
func (m *Model) CurrentTab() int { return m.current }

// SelectedBlock reports the focused pane's selected block, or false when the pane has no
// blocks yet. This is what `TestTUIBlockNavigation_REQ_TUI_001` asserts on.
func (m *Model) SelectedBlock() (sessdomain.Block, bool) {
	p := m.focusedPane()
	if p == nil || len(p.blocks) == 0 {
		return sessdomain.Block{}, false
	}
	if p.selected < 0 || p.selected >= len(p.blocks) {
		return sessdomain.Block{}, false
	}
	return p.blocks[p.selected], true
}

// Status reports the status line, for the tests.
func (m *Model) Status() string { return m.status }

// Disconnected reports whether the daemon connection ended.
func (m *Model) Disconnected() bool { return m.disconnected }

func (m *Model) focusedTab() *tab {
	if m.current < 0 || m.current >= len(m.tabs) {
		return nil
	}
	return m.tabs[m.current]
}

func (m *Model) focusedPane() *pane {
	t := m.focusedTab()
	if t == nil || t.focused < 0 || t.focused >= len(t.panes) {
		return nil
	}
	return t.panes[t.focused]
}

// paneByID finds a pane across every tab, because a notification names a session and says
// nothing about which tab is showing it.
func (m *Model) paneByID(sessionID string) *pane {
	for _, t := range m.tabs {
		for _, p := range t.panes {
			if p.sessionID == sessionID {
				return p
			}
		}
	}
	return nil
}

// paneSize is how big one pane is, given how many share the tab and whether the block list
// is open. The daemon is told this through `session.resize`, so what the shell believes
// about its terminal matches what the user sees.
func (m *Model) paneSize(paneCount int) sessdomain.Size {
	width := m.width
	if m.showBlocks {
		width -= blockListWidth
	}
	if paneCount > 1 {
		// One column of separator between the panes.
		width = (width - (paneCount - 1)) / paneCount
	}
	// The tab bar and the status line each take a row.
	height := m.height - 2

	return clampSize(width, height)
}

// clampSize keeps a size inside the API Spec §5.9 bounds. A terminal smaller than the
// minimum is a real situation — a split in a short window — and the daemon would reject
// the resize, so the client clamps and draws what fits rather than failing.
func clampSize(width, height int) sessdomain.Size {
	const (
		minCols, maxCols = 20, 1000
		minRows, maxRows = 5, 500
	)
	return sessdomain.Size{
		Cols: uint16(clamp(width, minCols, maxCols)),
		Rows: uint16(clamp(height, minRows, maxRows)),
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (m *Model) resize(width, height int) tea.Cmd {
	m.width, m.height = width, height

	var cmds []tea.Cmd
	for _, t := range m.tabs {
		size := m.paneSize(len(t.panes))
		for _, p := range t.panes {
			if p.size == size || p.screen == nil {
				continue
			}
			if err := p.screen.Resize(size); err != nil {
				m.status = err.Error()
				continue
			}
			p.size = size
			cmds = append(cmds, m.resizeSessionCmd(p.sessionID, size))
		}
	}
	return tea.Batch(cmds...)
}

func (m *Model) renderTabBar() string {
	if len(m.tabs) == 0 {
		return "umbral · no sessions"
	}
	var b strings.Builder
	for i := range m.tabs {
		if i > 0 {
			b.WriteString(" ")
		}
		label := fmt.Sprintf(" %d ", i+1)
		if i == m.current {
			b.WriteString("[" + strings.TrimSpace(label) + "]")
		} else {
			b.WriteString(" " + strings.TrimSpace(label) + " ")
		}
	}
	if m.disconnected {
		b.WriteString("   — disconnected from umbrald —")
	}
	return b.String()
}

// renderBody puts the panes side by side and the block list beside them.
func (m *Model) renderBody() string {
	t := m.focusedTab()
	if t == nil || len(t.panes) == 0 {
		return "starting a session…"
	}

	columns := make([][]string, 0, len(t.panes)+1)
	for _, p := range t.panes {
		columns = append(columns, m.renderPane(p))
	}
	if m.showBlocks {
		columns = append(columns, m.renderBlockList())
	}
	return joinColumns(columns, m.height-2)
}

func (m *Model) renderPane(p *pane) []string {
	if p.screen == nil {
		return padTo([]string{"attaching…"}, int(p.size.Rows))
	}
	lines, err := p.screen.Lines()
	if err != nil {
		return padTo([]string{err.Error()}, int(p.size.Rows))
	}
	if p.exited {
		lines = append([]string{"— session exited —"}, lines...)
	}
	return padTo(lines, int(p.size.Rows))
}

func (m *Model) renderBlockList() []string {
	p := m.focusedPane()
	if p == nil {
		return nil
	}
	rows := make([]string, 0, len(p.blocks)+1)
	rows = append(rows, "blocks")
	for i, blk := range p.blocks {
		marker := "  "
		if i == p.selected {
			marker = "> "
		}
		exit := "  ·"
		if blk.ExitCode != nil {
			exit = fmt.Sprintf("%3d", *blk.ExitCode)
		}
		cmd := blk.Command
		if len(cmd) > blockListWidth-8 {
			cmd = cmd[:blockListWidth-9] + "…"
		}
		rows = append(rows, fmt.Sprintf("%s%s %s", marker, exit, cmd))
	}
	return rows
}

func (m *Model) renderStatus() string {
	if m.status != "" {
		return m.status
	}
	return "ctrl+t new tab · ctrl+n next tab · ctrl+s split · ctrl+o next pane · " +
		"ctrl+b blocks · ctrl+p/ctrl+g prev/next block · ctrl+q quit"
}

// joinColumns places rendered columns side by side, separated by a single space, and pads
// every column to the same height so the second one does not climb into the first.
func joinColumns(columns [][]string, height int) string {
	if height < 1 {
		height = 1
	}
	widths := make([]int, len(columns))
	for i, col := range columns {
		for _, line := range col {
			if n := len([]rune(line)); n > widths[i] {
				widths[i] = n
			}
		}
	}

	var b strings.Builder
	for row := range height {
		for i, col := range columns {
			if i > 0 {
				b.WriteString(" ")
			}
			line := ""
			if row < len(col) {
				line = col[row]
			}
			b.WriteString(line)
			// Pad every column but the last: trailing spaces on the last one are just
			// bytes the terminal has to draw.
			if i < len(columns)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-len([]rune(line))))
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func padTo(lines []string, rows int) []string {
	if rows < 1 {
		rows = 1
	}
	if len(lines) > rows {
		return lines[len(lines)-rows:]
	}
	out := make([]string, rows)
	copy(out, lines)
	return out
}

// withTimeout is the context every daemon call gets.
func withTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), callTimeout)
}
