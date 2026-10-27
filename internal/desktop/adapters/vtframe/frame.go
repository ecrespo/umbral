// Package vtframe renders a session's byte stream for the desktop client with the libghostty
// the daemon and the TUI use: two emulators reading one stream are two chances to disagree
// about what the user is looking at, so there is only one implementation (DD-001).
//
// It walks libghostty's render state rather than formatting HTML, so the page receives text
// and styles and builds the DOM from them: a program's output never reaches the page as markup.
package vtframe

import (
	"fmt"
	"sync"

	"go.mitchellh.com/libghostty"

	"github.com/ecrespo/umbral/internal/desktop/ports"
	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
)

// The client keeps what the daemon keeps, as the TUI does: a snapshot may hold exactly that
// much, and libghostty prunes on whichever of the two budgets binds first.
const (
	scrollbackLines = sessdomain.MaxScrollbackLines
	scrollbackBytes = 64 << 20
)

// Screen is one pane's view of a session. libghostty's terminal and render state are not safe
// for concurrent use, and the bridge renders from its own goroutine, so every call holds mu.
type Screen struct {
	mu    sync.Mutex
	term  *libghostty.Terminal
	state *libghostty.RenderState
	rows  *libghostty.RenderStateRowIterator
	cells *libghostty.RenderStateRowCells
	size  sessdomain.Size
}

// New builds a screen of the given size. It satisfies ports.ScreenFactory.
func New(size sessdomain.Size) (ports.Screen, error) {
	if err := size.Validate(); err != nil {
		return nil, err
	}
	s := &Screen{size: size}
	var err error
	if s.term, err = newTerminal(size); err != nil {
		return nil, err
	}
	if s.state, err = libghostty.NewRenderState(); err == nil {
		if s.rows, err = libghostty.NewRenderStateRowIterator(); err == nil {
			s.cells, err = libghostty.NewRenderStateRowCells()
		}
	}
	if err != nil {
		s.release()
		return nil, fmt.Errorf("vtframe: create the render state: %w", err)
	}
	return s, nil
}

func newTerminal(size sessdomain.Size) (*libghostty.Terminal, error) {
	term, err := libghostty.NewTerminal(
		libghostty.WithSize(size.Cols, size.Rows),
		libghostty.WithMaxScrollbackLines(scrollbackLines),
		libghostty.WithMaxScrollbackBytes(scrollbackBytes),
	)
	if err != nil {
		return nil, fmt.Errorf("vtframe: create a %dx%d screen: %w", size.Cols, size.Rows, err)
	}
	return term, nil
}

// Write feeds it snapshot or live bytes. Device queries go unanswered: the daemon's emulator
// is the one talking to the program.
func (s *Screen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.term == nil {
		return 0, fmt.Errorf("vtframe: write to a closed screen")
	}
	s.term.VTWrite(p)
	return len(p), nil
}

// Resize changes what the client draws, with zero pixel dimensions as the daemon passes.
func (s *Screen) Resize(size sessdomain.Size) error {
	if err := size.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.term == nil {
		return fmt.Errorf("vtframe: resize a closed screen")
	}
	if err := s.term.Resize(size.Cols, size.Rows, 0, 0); err != nil {
		return fmt.Errorf("vtframe: resize to %dx%d: %w", size.Cols, size.Rows, err)
	}
	s.size = size
	return nil
}

// Reset empties the screen and its scrollback by building a fresh terminal of the same size:
// libghostty has no clear-everything call, and a snapshot is replayed into an empty emulator.
func (s *Screen) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.term == nil {
		return fmt.Errorf("vtframe: reset a closed screen")
	}
	fresh, err := newTerminal(s.size)
	if err != nil {
		return err
	}
	s.term.Close()
	s.term = fresh
	return nil
}

// Frame renders the visible screen: the render state is the viewport, which follows the
// bottom of the scrollback as output arrives.
func (s *Screen) Frame() (ports.Frame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.term == nil {
		return ports.Frame{}, fmt.Errorf("vtframe: read a closed screen")
	}
	if err := s.state.Update(s.term); err != nil {
		return ports.Frame{}, fmt.Errorf("vtframe: update the render state: %w", err)
	}
	colors, err := s.state.Colors()
	if err != nil {
		return ports.Frame{}, fmt.Errorf("vtframe: colours: %w", err)
	}
	f := ports.Frame{Cols: s.size.Cols, Fg: hex(colors.Foreground), Bg: hex(colors.Background)}
	if cur, err := s.state.Cursor(); err == nil && cur.ViewportHasValue {
		f.Cursor = ports.Cursor{X: cur.ViewportX, Y: cur.ViewportY, Visible: cur.Visible}
	}
	if err := s.state.RowIterator(s.rows); err != nil {
		return ports.Frame{}, fmt.Errorf("vtframe: rows: %w", err)
	}
	for s.rows.Next() {
		row, err := s.row(f)
		if err != nil {
			return ports.Frame{}, err
		}
		f.Rows = append(f.Rows, row)
	}
	for len(f.Rows) < int(s.size.Rows) {
		f.Rows = append(f.Rows, nil)
	}
	return f, s.state.Clean()
}

// row reads one row into runs, merging neighbouring cells of one style and skipping the
// spacer cell after a wide character.
func (s *Screen) row(f ports.Frame) ([]ports.Run, error) {
	if err := s.rows.Cells(s.cells); err != nil {
		return nil, fmt.Errorf("vtframe: cells: %w", err)
	}
	var (
		runs []ports.Run
		text []byte
		cur  ports.Run
		st   libghostty.RenderCellStyle
	)
	flush := func() {
		if len(text) > 0 {
			cur.Text = string(text)
			runs = append(runs, cur)
			text = text[:0]
		}
	}
	for s.cells.Next() {
		if raw, err := s.cells.Raw(); err == nil {
			if w, err := raw.Wide(); err == nil && (w == libghostty.CellWideSpacerTail || w == libghostty.CellWideSpacerHead) {
				continue
			}
		}
		if err := s.cells.StyleInto(&st); err != nil {
			return nil, fmt.Errorf("vtframe: style: %w", err)
		}
		next := style(st, f)
		if next != cur {
			flush()
			cur = next
		}
		n := len(text)
		var err error
		if text, err = s.cells.AppendGraphemes(text); err != nil {
			return nil, fmt.Errorf("vtframe: text: %w", err)
		}
		if len(text) == n {
			text = append(text, ' ')
		}
	}
	flush()
	return runs, nil
}

// style is a cell's run style, inverse already applied.
func style(st libghostty.RenderCellStyle, f ports.Frame) ports.Run {
	r := ports.Run{Bold: st.Bold, Faint: st.Faint, Italic: st.Italic, Underline: st.Underline, Strike: st.Strikethrough}
	if st.HasForeground {
		r.Fg = hex(st.Foreground)
	}
	if st.HasBackground {
		r.Bg = hex(st.Background)
	}
	if st.Inverse {
		fg, bg := r.Fg, r.Bg
		if fg == "" {
			fg = f.Fg
		}
		if bg == "" {
			bg = f.Bg
		}
		r.Fg, r.Bg = bg, fg
	}
	return r
}

func hex(c libghostty.ColorRGB) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// Close releases the terminal and the render state. Calling it twice is not an error.
func (s *Screen) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.release()
	return nil
}

func (s *Screen) release() {
	if s.cells != nil {
		s.cells.Close()
		s.cells = nil
	}
	if s.rows != nil {
		s.rows.Close()
		s.rows = nil
	}
	if s.state != nil {
		s.state.Close()
		s.state = nil
	}
	if s.term != nil {
		s.term.Close()
		s.term = nil
	}
}
