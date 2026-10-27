package vtframe

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ecrespo/umbral/internal/desktop/ports"
	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
)

func rowText(r []ports.Run) string {
	var b strings.Builder
	for _, run := range r {
		b.WriteString(run.Text)
	}
	return strings.TrimRight(b.String(), " ")
}

// TestAFrameIsTheVisibleScreenAsText_REQ_DSK_001: a frame is the visible screen only, one row
// per screen row, with each style's text in a run of its own and markup a program prints kept
// as text; a wide character is one character, not one and a padding cell; inverse video swaps
// the colours; the cursor is where the emulator has it (delta `2026-10-desktop-client`,
// decision 3).
func TestAFrameIsTheVisibleScreenAsText_REQ_DSK_001(t *testing.T) {
	t.Parallel()
	s, err := New(sessdomain.Size{Cols: 40, Rows: 5})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for i := 1; i <= 8; i++ {
		fmt.Fprintf(s, "line %d\r\n", i)
	}
	if _, err := s.Write([]byte("\x1b[1;31mred\x1b[0m <b>&amp; \x1b[7mrev\x1b[0m 日本x")); err != nil {
		t.Fatal(err)
	}
	f, err := s.Frame()
	if err != nil {
		t.Fatal(err)
	}
	if f.Cols != 40 || len(f.Rows) != 5 {
		t.Fatalf("frame is %d cols and %d rows, want 40 and 5", f.Cols, len(f.Rows))
	}
	if got := rowText(f.Rows[0]); got != "line 5" {
		t.Errorf("first visible row %q, want line 5: the scrollback is not the screen", got)
	}
	last := f.Rows[4]
	if got := rowText(last); got != "red <b>&amp; rev 日本x" {
		t.Errorf("last row %q", got)
	}
	var red, rev *ports.Run
	for i := range last {
		switch last[i].Text {
		case "red":
			red = &last[i]
		case "rev":
			rev = &last[i]
		}
	}
	if red == nil || !red.Bold || red.Fg == "" || red.Fg == f.Fg {
		t.Errorf("the red run: %+v (default fg %s)", red, f.Fg)
	}
	if rev == nil || rev.Fg != f.Bg || rev.Bg != f.Fg {
		t.Errorf("the inverse run: %+v, want fg %s bg %s", rev, f.Bg, f.Fg)
	}
	if f.Cursor.Y != 4 || f.Cursor.X != uint16(len("red <b>&amp; rev ")+5) || !f.Cursor.Visible {
		t.Errorf("cursor %+v", f.Cursor)
	}
	if f.Fg == "" || f.Bg == "" || !strings.HasPrefix(f.Fg, "#") {
		t.Errorf("default colours %q %q", f.Fg, f.Bg)
	}

	if err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.Frame(); rowText(f.Rows[0]) != "" || rowText(f.Rows[4]) != "" {
		t.Error("a reset screen still shows text")
	}
	if err := s.Resize(sessdomain.Size{Cols: 30, Rows: 7}); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.Frame(); f.Cols != 30 || len(f.Rows) != 7 {
		t.Errorf("after a resize: %d cols %d rows", f.Cols, len(f.Rows))
	}
}
