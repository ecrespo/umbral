package ghostty

import (
	"fmt"
	"sync"

	"go.mitchellh.com/libghostty"

	"github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/sessions/ports"
)

// Emulator adapts a libghostty terminal to the sessions module's Emulator port.
//
// libghostty's terminal is not safe for concurrent use, and a session has at least two
// goroutines touching it: the one draining the PTY and whichever connection asks for a
// snapshot. The mutex here is what makes the port's contract true.
type Emulator struct {
	mu   sync.Mutex
	term *libghostty.Terminal
}

// NewEmulator builds an emulator for the given size. It satisfies ports.EmulatorFactory.
func NewEmulator(size domain.Size) (ports.Emulator, error) {
	if err := size.Validate(); err != nil {
		return nil, err
	}
	term, err := NewTerminal(size.Cols, size.Rows)
	if err != nil {
		return nil, err
	}
	return &Emulator{term: term}, nil
}

// Write feeds PTY output into the emulator.
func (e *Emulator) Write(p []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.term == nil {
		return 0, fmt.Errorf("ghostty: write to a closed emulator")
	}
	e.term.VTWrite(p)
	return len(p), nil
}

// Resize changes the screen dimensions (REQ-TERM-007).
func (e *Emulator) Resize(size domain.Size) error {
	if err := size.Validate(); err != nil {
		return err
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.term == nil {
		return fmt.Errorf("ghostty: resize a closed emulator")
	}
	// The pixel dimensions are only used by image protocols and size reports. The daemon
	// never sees the client's font metrics, so zero is the honest value: a made-up cell
	// size would make a program's size report wrong rather than absent.
	if err := e.term.Resize(size.Cols, size.Rows, 0, 0); err != nil {
		return fmt.Errorf("ghostty: resize to %dx%d: %w", size.Cols, size.Rows, err)
	}
	return nil
}

// Snapshot renders the screen as replayable VT (REQ-TERM-004).
func (e *Emulator) Snapshot() ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.term == nil {
		return nil, fmt.Errorf("ghostty: snapshot a closed emulator")
	}
	return Snapshot(e.term, SnapshotOptions{})
}

// PlainText renders the screen without escape sequences (REQ-BLK-007).
func (e *Emulator) PlainText() (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.term == nil {
		return "", fmt.Errorf("ghostty: read a closed emulator")
	}
	return PlainText(e.term)
}

// Cursor reports the cursor's cell.
func (e *Emulator) Cursor() (x, y uint16, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.term == nil {
		return 0, 0, fmt.Errorf("ghostty: read the cursor of a closed emulator")
	}

	x, err = e.term.CursorX()
	if err != nil {
		return 0, 0, fmt.Errorf("ghostty: cursor x: %w", err)
	}
	y, err = e.term.CursorY()
	if err != nil {
		return 0, 0, fmt.Errorf("ghostty: cursor y: %w", err)
	}
	return x, y, nil
}

// Close releases the terminal. It is safe to call more than once.
func (e *Emulator) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.term == nil {
		return nil
	}
	e.term.Close()
	e.term = nil
	return nil
}
