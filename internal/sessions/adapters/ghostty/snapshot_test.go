package ghostty

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mitchellh.com/libghostty"
)

var update = flag.Bool("update", false, "rewrite the golden files")

const (
	testCols = 80
	testRows = 24
)

// newTestTerminal builds an emulator and writes the given VT into it.
func newTestTerminal(t *testing.T, input string) *libghostty.Terminal {
	t.Helper()

	term, err := NewTerminal(testCols, testRows)
	if err != nil {
		t.Fatalf("NewTerminal: %v", err)
	}
	t.Cleanup(term.Close)

	if input != "" {
		term.VTWrite([]byte(input))
	}
	return term
}

// golden compares got against testdata/<name>.golden, rewriting it under -update.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()

	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with -update to create it)", path, err)
	}
	if string(got) != string(want) {
		t.Errorf("snapshot differs from %s\n got: %q\nwant: %q", path, got, want)
	}
}

// vtCases are the screens the round-trip property is checked against. Each one exercises
// a different piece of state a client cannot infer from the text alone.
var vtCases = []struct {
	name  string
	input string
}{
	{"plain", "hello world\r\nsecond line\r\n"},
	{"sgr-basic", "\x1b[1;32mbold green\x1b[0m normal\r\n\x1b[4munderline\x1b[0m\r\n"},
	{"sgr-truecolor", "\x1b[38;2;255;128;0mtruecolor fg\x1b[0m \x1b[48;5;27m256 bg\x1b[0m\r\n"},
	{"cursor-position", "line one\r\nline two\r\nline three\r\n\x1b[2;4H"},
	{"scroll-region", "\x1b[3;10r\x1b[5;1Hinside the region\r\n"},
	{"wide-chars", "日本語テキスト\r\nmixed 日本 text\r\n"},
	{"alt-screen", "primary\r\n\x1b[?1049halternate content\r\n"},
	{"scrollback", strings.Repeat("scrollback line\r\n", 60)},
}

// TestSnapshotRoundTrip_REQ_TERM_004 is the property the whole subscribe path rests on:
// the snapshot is not a description of the screen, it *is* the screen, so replaying it
// into an empty emulator must reproduce what the daemon holds.
//
// REQ-TERM-004 promises a subscriber the current screen before the live stream. If the
// round trip is lossy, every client that attaches to a running session sees something
// subtly different from the user's own terminal.
func TestSnapshotRoundTrip_REQ_TERM_004(t *testing.T) {
	t.Parallel()

	for _, tc := range vtCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			original := newTestTerminal(t, tc.input)
			snapshot, err := Snapshot(original, SnapshotOptions{})
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}

			replayed := newTestTerminal(t, "")
			replayed.VTWrite(snapshot)

			originalText, err := PlainText(original)
			if err != nil {
				t.Fatalf("PlainText(original): %v", err)
			}
			replayedText, err := PlainText(replayed)
			if err != nil {
				t.Fatalf("PlainText(replayed): %v", err)
			}
			if originalText != replayedText {
				t.Errorf("plain text differs after replay\n original: %q\nreplayed: %q",
					originalText, replayedText)
			}

			// Cursor position is state no amount of text comparison would catch, and a
			// wrong cursor puts the next keystroke in the wrong place.
			assertCursorMatches(t, original, replayed)

			// The snapshot must be a fixed point: snapshotting the replay yields the
			// same bytes. A difference here means replay lost or invented state that the
			// plain-text comparison happened not to show.
			again, err := Snapshot(replayed, SnapshotOptions{})
			if err != nil {
				t.Fatalf("Snapshot(replayed): %v", err)
			}
			if string(again) != string(snapshot) {
				t.Errorf("snapshot is not a fixed point\n first: %q\nsecond: %q", snapshot, again)
			}

			golden(t, tc.name, snapshot)
		})
	}
}

func assertCursorMatches(t *testing.T, original, replayed *libghostty.Terminal) {
	t.Helper()

	ox, err := original.CursorX()
	if err != nil {
		t.Fatalf("CursorX(original): %v", err)
	}
	oy, err := original.CursorY()
	if err != nil {
		t.Fatalf("CursorY(original): %v", err)
	}
	rx, err := replayed.CursorX()
	if err != nil {
		t.Fatalf("CursorX(replayed): %v", err)
	}
	ry, err := replayed.CursorY()
	if err != nil {
		t.Fatalf("CursorY(replayed): %v", err)
	}
	if ox != rx || oy != ry {
		t.Errorf("cursor after replay = (%d,%d), want (%d,%d)", rx, ry, ox, oy)
	}
}

// TestScrollbackLimitNeedsBothBudgets is the regression test for the spike's main
// finding. libghostty applies the byte budget and the line budget together, and its
// default byte budget is small, so setting only the line limit silently keeps a fraction
// of the history REQ-TERM-004 promises.
//
// It writes far fewer lines than the 10,000 cap on purpose. The contrast is what matters,
// and a terminal whose tiny default budget is pruned 20,000 times takes 40 seconds to
// fill, which is not a price a unit suite should pay. The measured behaviour at the full
// cap is in docs/spikes/q01-snapshot.md.
func TestScrollbackLimitNeedsBothBudgets(t *testing.T) {
	t.Parallel()

	const cols, rows, written = 120, 40, 3_000
	payload := []byte(strings.Repeat(strings.Repeat("x", 70)+"\r\n", written))

	retained := func(opts ...libghostty.TerminalOption) int {
		t.Helper()

		term, err := libghostty.NewTerminal(append([]libghostty.TerminalOption{
			libghostty.WithSize(cols, rows),
		}, opts...)...)
		if err != nil {
			t.Fatalf("NewTerminal: %v", err)
		}
		defer term.Close()

		term.VTWrite(payload)
		text, err := PlainText(term)
		if err != nil {
			t.Fatalf("PlainText: %v", err)
		}
		return strings.Count(text, "\n") + 1
	}

	lineLimitOnly := retained(libghostty.WithMaxScrollbackLines(ScrollbackLines))

	// The second terminal comes from NewTerminal rather than from hand-written options,
	// so dropping either budget from the production constructor fails this test. A test
	// that reassembled the options itself would pass while the daemon lost its history.
	production, err := NewTerminal(cols, rows)
	if err != nil {
		t.Fatalf("NewTerminal: %v", err)
	}
	defer production.Close()
	production.VTWrite(payload)
	productionText, err := PlainText(production)
	if err != nil {
		t.Fatalf("PlainText: %v", err)
	}
	bothBudgets := strings.Count(productionText, "\n") + 1

	// Every written line is inside the 10,000-line cap, so both budgets together must
	// keep all of them.
	if bothBudgets < written {
		t.Errorf("with both budgets the terminal kept %d of %d lines, all of which fit the %d cap",
			bothBudgets, written, ScrollbackLines)
	}
	if lineLimitOnly >= written {
		t.Fatalf("the line limit alone kept all %d lines. If libghostty's default byte budget grew,"+
			" ScrollbackBytes may no longer be needed and this test should be revisited", written)
	}
	if bothBudgets <= lineLimitOnly {
		t.Errorf("adding the byte budget changed nothing: %d lines alone, %d with both",
			lineLimitOnly, bothBudgets)
	}
}

// TestSnapshotPaletteIsOptional documents why the palette is off by default: it is a flat
// cost that dwarfs a small screen.
func TestSnapshotPaletteIsOptional(t *testing.T) {
	t.Parallel()

	term := newTestTerminal(t, "hello\r\n")

	without, err := Snapshot(term, SnapshotOptions{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	with, err := Snapshot(term, SnapshotOptions{Palette: true})
	if err != nil {
		t.Fatalf("Snapshot with palette: %v", err)
	}

	if len(with) <= len(without) {
		t.Fatalf("the palette added nothing: %d bytes with, %d without", len(with), len(without))
	}
	if !strings.Contains(string(with), "\x1b]4;") {
		t.Error("the palette snapshot carries no OSC 4 sequences")
	}
	if strings.Contains(string(without), "\x1b]4;") {
		t.Error("the default snapshot carries OSC 4 sequences; the palette must be opt-in")
	}
	if len(with)-len(without) < 4096 {
		t.Errorf("the palette cost %d bytes; the comment claiming it is expensive needs revisiting",
			len(with)-len(without))
	}
}

// BenchmarkSnapshotFullScrollback sizes the subscribe path for T-F0-06, which sends one
// of these per client and has an 8 MiB queue to fit it in.
func BenchmarkSnapshotFullScrollback(b *testing.B) {
	term, err := NewTerminal(120, 40)
	if err != nil {
		b.Fatalf("NewTerminal: %v", err)
	}
	defer term.Close()

	term.VTWrite([]byte(strings.Repeat(strings.Repeat("x", 70)+"\r\n", 20_000)))

	snapshot, err := Snapshot(term, SnapshotOptions{})
	if err != nil {
		b.Fatalf("Snapshot: %v", err)
	}
	b.ReportMetric(float64(len(snapshot)), "snapshot_bytes")

	b.ResetTimer()
	for b.Loop() {
		if _, err := Snapshot(term, SnapshotOptions{}); err != nil {
			b.Fatalf("Snapshot: %v", err)
		}
	}
}
