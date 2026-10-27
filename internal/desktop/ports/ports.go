// Package ports is what the desktop bridge renders from and emits to. The implementations
// live in `internal/desktop/adapters/` and in `cmd/umbral-desktop`, which picks them.
package ports

import sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"

// Frame is one session's visible screen, ready for a webview to draw: rows of styled runs and
// the cursor. It carries text, never markup, so what a program prints cannot become anything
// but text in the page (delta `2026-10-desktop-client`, decision 3).
type Frame struct {
	Cols   uint16  `json:"cols"`
	Rows   [][]Run `json:"rows"`
	Cursor Cursor  `json:"cursor"`
	// Fg and Bg are the terminal's default colours, `#rrggbb`.
	Fg string `json:"fg"`
	Bg string `json:"bg"`
}

// Run is a stretch of one row sharing a style. Fg and Bg are `#rrggbb`, or "" for the
// terminal's default; Inverse is already applied to them.
type Run struct {
	Text      string `json:"t"`
	Fg        string `json:"fg,omitempty"`
	Bg        string `json:"bg,omitempty"`
	Bold      bool   `json:"b,omitempty"`
	Faint     bool   `json:"f,omitempty"`
	Italic    bool   `json:"i,omitempty"`
	Underline bool   `json:"u,omitempty"`
	Strike    bool   `json:"s,omitempty"`
}

// Cursor is where the cursor is drawn, and whether it is.
type Cursor struct {
	X       uint16 `json:"x"`
	Y       uint16 `json:"y"`
	Visible bool   `json:"visible"`
}

// Screen is one session's VT state as the desktop client sees it: the same emulator the
// daemon runs, fed the same bytes (DD-001), as the TUI's is.
type Screen interface {
	// Write feeds it the bytes of a snapshot or of a `session.output` notification.
	Write(p []byte) (int, error)
	// Resize changes the screen's dimensions; telling the daemon is the caller's job.
	Resize(size sessdomain.Size) error
	// Frame renders the visible screen.
	Frame() (Frame, error)
	// Reset empties it, leaving a newly created screen of the same size: a snapshot is
	// replayed into an empty emulator (API §5.11).
	Reset() error
	// Close releases it.
	Close() error
}

// ScreenFactory builds a screen of a given size.
type ScreenFactory func(size sessdomain.Size) (Screen, error)

// Emitter delivers an event to the page. `cmd/umbral-desktop` gives it Wails' event bus, and
// a test a recorder.
type Emitter interface {
	Emit(name string, data any)
}
