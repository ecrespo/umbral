package ghosttyvt

import (
	"strings"
	"testing"

	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
)

func newScreen(t *testing.T, cols, rows uint16) *Screen {
	t.Helper()
	s, err := New(sessdomain.Size{Cols: cols, Rows: rows})
	if err != nil {
		t.Fatalf("New(%dx%d): %v", cols, rows, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s.(*Screen)
}

// TestLinesAlwaysReportsTheWholeScreen is the contract a pane is placed on: exactly
// size.Rows entries, whatever is on screen. A renderer that returned only the non-empty
// rows would make every pane the height of its content, and two panes in a split would
// overlap the moment one of them scrolled.
func TestLinesAlwaysReportsTheWholeScreen(t *testing.T) {
	t.Parallel()

	s := newScreen(t, 40, 10)
	if _, err := s.Write([]byte("one\r\ntwo\r\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	lines, err := s.Lines()
	if err != nil {
		t.Fatalf("Lines: %v", err)
	}
	if len(lines) != 10 {
		t.Fatalf("Lines returned %d rows, want 10 whatever is on screen", len(lines))
	}
	if strings.TrimSpace(lines[0]) != "one" || strings.TrimSpace(lines[1]) != "two" {
		t.Errorf("the first two rows are %q and %q, want one and two", lines[0], lines[1])
	}
	for i, l := range lines[2:] {
		if strings.TrimSpace(l) != "" {
			t.Errorf("row %d should be blank, got %q", i+2, l)
		}
	}
}

// TestLinesShowsTheScreenAndNotTheScrollback covers the other half: once more has been
// written than fits, a pane shows the bottom. Returning the scrollback too would make
// Lines grow without bound and place a pane's first row somewhere in the session's past.
func TestLinesShowsTheScreenAndNotTheScrollback(t *testing.T) {
	t.Parallel()

	s := newScreen(t, 40, 5)
	var b strings.Builder
	for i := 1; i <= 20; i++ {
		b.WriteString("line")
		b.WriteString(string(rune('0' + i%10)))
		b.WriteString("\r\n")
	}
	if _, err := s.Write([]byte(b.String())); err != nil {
		t.Fatalf("Write: %v", err)
	}

	lines, err := s.Lines()
	if err != nil {
		t.Fatalf("Lines: %v", err)
	}
	if len(lines) != 5 {
		t.Fatalf("Lines returned %d rows, want 5", len(lines))
	}
	// The 20th line was written last, so it is the one above the final blank row.
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "line0") {
		t.Errorf("the visible screen does not hold the last line written: %q", joined)
	}
	if strings.Contains(joined, "line1\n") && strings.Contains(joined, "line2\n") {
		t.Errorf("the visible screen still holds scrolled-off lines: %q", joined)
	}
}

// TestSnapshotBytesReproduceTheScreen_REQ_TERM_004 is the property the whole subscribe
// flow depends on: what `session.subscribe` returns is replayable VT, and this renderer
// is what replays it. If the client could not reproduce the daemon's screen from those
// bytes, reconnecting would show something other than what is running.
func TestSnapshotBytesReproduceTheScreen_REQ_TERM_004(t *testing.T) {
	t.Parallel()

	// The bytes a daemon snapshot is made of: positioning, styles, text.
	const snapshot = "\x1b[H\x1b[2J\x1b[1;31mred bold\x1b[0m\r\nplain\r\n\x1b[3;7H"

	first := newScreen(t, 40, 8)
	if _, err := first.Write([]byte(snapshot)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	want, err := first.Lines()
	if err != nil {
		t.Fatalf("Lines: %v", err)
	}
	wantX, wantY, err := first.Cursor()
	if err != nil {
		t.Fatalf("Cursor: %v", err)
	}

	second := newScreen(t, 40, 8)
	if _, err := second.Write([]byte(snapshot)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := second.Lines()
	if err != nil {
		t.Fatalf("Lines: %v", err)
	}
	gotX, gotY, err := second.Cursor()
	if err != nil {
		t.Fatalf("Cursor: %v", err)
	}

	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("replaying the same bytes produced a different screen:\n%q\nvs\n%q", got, want)
	}
	if gotX != wantX || gotY != wantY {
		t.Errorf("cursor = (%d,%d), want (%d,%d): a client would place it wrong after reconnecting",
			gotX, gotY, wantX, wantY)
	}
	// The cursor was put at row 3, column 7 by the snapshot, one-based on the wire and
	// zero-based in the cell coordinates the API reports.
	if wantX != 6 || wantY != 2 {
		t.Errorf("cursor = (%d,%d), want (6,2) for `CSI 3;7H`", wantX, wantY)
	}
}

// TestResizeReflowsRatherThanTruncating checks the case a split makes routine: a pane
// changes width when its neighbour appears. The screen has to follow, or the client draws
// a pane at one size while holding text laid out for another.
func TestResizeReflowsRatherThanTruncating(t *testing.T) {
	t.Parallel()

	s := newScreen(t, 40, 6)
	if _, err := s.Write([]byte("abcdefghij\r\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := s.Resize(sessdomain.Size{Cols: 20, Rows: 5}); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	lines, err := s.Lines()
	if err != nil {
		t.Fatalf("Lines: %v", err)
	}
	if len(lines) != 5 {
		t.Errorf("Lines returned %d rows after resizing to 5, want 5", len(lines))
	}
	for i, l := range lines {
		if len([]rune(l)) > 20 {
			t.Errorf("row %d is %d cells wide after resizing to 20: %q", i, len([]rune(l)), l)
		}
	}
}

// TestAClosedScreenFailsRatherThanCrashing: the TUI closes a screen when its pane goes
// away, and a stray render afterwards must be an error, not a use-after-free in cgo.
func TestAClosedScreenFailsRatherThanCrashing(t *testing.T) {
	t.Parallel()

	s, err := New(sessdomain.Size{Cols: 20, Rows: 5})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("closing twice is an error: %v", err)
	}

	if _, err := s.Write([]byte("x")); err == nil {
		t.Error("writing to a closed screen was accepted")
	}
	if _, err := s.Lines(); err == nil {
		t.Error("rendering a closed screen was accepted")
	}
	if _, _, err := s.Cursor(); err == nil {
		t.Error("reading the cursor of a closed screen was accepted")
	}
	// A valid size, so the error can only come from the screen being closed. An invalid
	// one would be rejected by Size.Validate before the closed check ever ran, and the
	// assertion would pass without proving anything.
	if err := s.Resize(sessdomain.Size{Cols: 20, Rows: 5}); err == nil {
		t.Error("resizing a closed screen was accepted")
	}
}
