// Package ghosttyvt renders a session's byte stream for the TUI, using the same
// libghostty the daemon uses.
//
// It is a second libghostty call site, and deliberately so: DD-001 sends the client bytes
// rather than cells, so somebody on this side has to parse them, and parsing them with a
// different emulator than the daemon's would let the screen drift from the block history
// the daemon recorded from the same stream. The VT conformance suite of T-F0-07 covers
// this implementation for free; it would cover nothing of a second one.
//
// The price is that `umbral-tui` links cgo and needs `task deps:ghostty`, which is
// recorded in `changes/2026-09-tui-renderer/`.
package ghosttyvt

import (
	"fmt"
	"strings"
	"sync"

	"go.mitchellh.com/libghostty"

	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/tui/ports"
)

// scrollbackLines is what the client keeps of a session's history.
//
// It matches the daemon's cap, because `session.subscribe` may deliver a snapshot holding
// exactly that much and a client that kept less would throw away what it had just been
// sent. The byte budget has to be set with it: libghostty prunes on whichever budget
// binds first, and its default byte budget is small enough that a terminal asked for
// 10,000 lines keeps 588 — the silent failure the Q-01 spike found.
const (
	scrollbackLines = sessdomain.MaxScrollbackLines
	scrollbackBytes = 64 << 20
)

// Screen is one pane's view of a session.
//
// libghostty's terminal is not safe for concurrent use. In the TUI everything happens on
// Bubble Tea's update goroutine, so the mutex is not strictly needed today; it is here
// because the type is exported and because the day someone renders from a second
// goroutine, the failure would be memory corruption rather than a test failure.
type Screen struct {
	mu   sync.Mutex
	term *libghostty.Terminal
	size sessdomain.Size
}

// New builds a screen of the given size. It satisfies ports.ScreenFactory.
func New(size sessdomain.Size) (ports.Screen, error) {
	if err := size.Validate(); err != nil {
		return nil, err
	}
	term, err := newTerminal(size)
	if err != nil {
		return nil, err
	}
	return &Screen{term: term, size: size}, nil
}

func newTerminal(size sessdomain.Size) (*libghostty.Terminal, error) {
	term, err := libghostty.NewTerminal(
		libghostty.WithSize(size.Cols, size.Rows),
		libghostty.WithMaxScrollbackLines(scrollbackLines),
		libghostty.WithMaxScrollbackBytes(scrollbackBytes),
	)
	if err != nil {
		return nil, fmt.Errorf("ghosttyvt: create a %dx%d screen: %w", size.Cols, size.Rows, err)
	}
	return term, nil
}

// Write feeds it snapshot or live bytes.
//
// Device queries are deliberately unanswered: the daemon's emulator is the one talking to
// the program, and a client that replied would be a second terminal answering the same
// question, with the answer going nowhere.
func (s *Screen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.term == nil {
		return 0, fmt.Errorf("ghosttyvt: write to a closed screen")
	}
	s.term.VTWrite(p)
	return len(p), nil
}

// Resize changes what the client draws. Telling the daemon is the caller's job.
func (s *Screen) Resize(size sessdomain.Size) error {
	if err := size.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.term == nil {
		return fmt.Errorf("ghosttyvt: resize a closed screen")
	}
	// Zero pixel dimensions, as the daemon passes: a client's font metrics are not the
	// daemon's business, and a made-up cell size would make a program's size report wrong
	// rather than absent.
	if err := s.term.Resize(size.Cols, size.Rows, 0, 0); err != nil {
		return fmt.Errorf("ghosttyvt: resize to %dx%d: %w", size.Cols, size.Rows, err)
	}
	s.size = size
	return nil
}

// Lines renders the visible screen, one entry per row.
//
// It returns exactly size.Rows entries, padding with empty strings, so the caller can
// place a pane without counting. Trailing blank rows are what an empty terminal is made
// of, and a caller that had to distinguish "no line" from "blank line" would get it wrong
// at the bottom of every screen.
func (s *Screen) Lines() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.term == nil {
		return nil, fmt.Errorf("ghosttyvt: read a closed screen")
	}

	// Trim is off: it would collapse the blank rows this function has to report, and the
	// caller needs the screen's shape, not its content.
	formatter, err := libghostty.NewFormatter(s.term,
		libghostty.WithFormatterFormat(libghostty.FormatterFormatPlain),
		libghostty.WithFormatterTrim(false),
	)
	if err != nil {
		return nil, fmt.Errorf("ghosttyvt: create the formatter: %w", err)
	}
	defer formatter.Close()

	out, err := formatter.FormatString()
	if err != nil {
		return nil, fmt.Errorf("ghosttyvt: render the screen: %w", err)
	}

	rows := int(s.size.Rows)
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")

	// The formatter renders the scrollback as well, and a pane shows the screen. The
	// visible rows are the last ones.
	if len(lines) > rows {
		lines = lines[len(lines)-rows:]
	}
	for len(lines) < rows {
		lines = append(lines, "")
	}
	return lines, nil
}

// Reset empties the screen and its scrollback by building a fresh terminal of the same
// size and dropping the old one. libghostty has no clear-everything call, and a
// `CSI 2J CSI 3J` would clear the screen while leaving the terminal's modes, scrolling
// region and tabstops as the previous session left them — a snapshot replayed into that
// is not replayed into an empty emulator.
func (s *Screen) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.term == nil {
		return fmt.Errorf("ghosttyvt: reset a closed screen")
	}

	fresh, err := newTerminal(s.size)
	if err != nil {
		return err
	}
	s.term.Close()
	s.term = fresh
	return nil
}

// Cursor reports the cursor's cell.
func (s *Screen) Cursor() (x, y uint16, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.term == nil {
		return 0, 0, fmt.Errorf("ghosttyvt: read a closed screen")
	}
	x, err = s.term.CursorX()
	if err != nil {
		return 0, 0, fmt.Errorf("ghosttyvt: cursor x: %w", err)
	}
	y, err = s.term.CursorY()
	if err != nil {
		return 0, 0, fmt.Errorf("ghosttyvt: cursor y: %w", err)
	}
	return x, y, nil
}

// Close releases the terminal. Calling it twice is not an error.
func (s *Screen) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.term == nil {
		return nil
	}
	s.term.Close()
	s.term = nil
	return nil
}
