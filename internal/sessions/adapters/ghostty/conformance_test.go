package ghostty

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mitchellh.com/libghostty"

	"github.com/ecrespo/umbral/internal/sessions/domain"
)

// vtDir is where the suite's fixtures live (Tech Design §5.1). The path climbs out of the
// package because the fixtures are a repository-level artifact: they describe the terminal
// Umbral promises, not this adapter.
var vtDir = filepath.Join("..", "..", "..", "..", "testdata", "vt")

var updateVT = flag.Bool("update-vt", false, "rewrite the VT conformance fixtures")

// priority marks whether a case is required by REQ-TERM-002.
type priority string

const (
	must   priority = "MUST"
	should priority = "SHOULD"
)

// conformanceCase is one entry of the closed list in Tech Design §8.1.
//
// The list is here rather than only on disk so that a missing fixture is a failure instead
// of a silently smaller suite: REQ-TERM-002 is measured against the twenty MUST cases, and
// a suite that quietly shrinks would still report success.
type conformanceCase struct {
	id       string
	slug     string
	priority priority
	// input is the byte stream fed to the emulator.
	input string
	// resize, when set, is applied after the input. VT-14 is about what survives it.
	resize *domain.Size
	// size overrides the default screen, for cases that need a small one to be legible.
	size *domain.Size
}

func sz(cols, rows uint16) *domain.Size { return &domain.Size{Cols: cols, Rows: rows} }

// conformanceCases is Tech Design §8.1, VT-01 to VT-22. Adding or removing a MUST case requires a
// Delta, because REQ-TERM-002 is measured against this list.
var conformanceCases = []conformanceCase{
	{
		id: "VT-01", slug: "cursor-movement", priority: must,
		input: "\x1b[10;20Hanchor" +
			"\x1b[5;5H" + "at-5-5" +
			"\x1b[2A" + "up2" +
			"\x1b[4B" + "down4" +
			"\x1b[10C" + "right10" +
			"\x1b[3D" + "left3" +
			// Past the edges: the cursor clamps to the screen rather than wrapping.
			"\x1b[1;1H\x1b[10A\x1b[10Dtop-left-clamped" +
			"\x1b[24;80H\x1b[10B\x1b[10Cbr",
	},
	{
		id: "VT-02", slug: "erase", priority: must,
		size: sz(40, 8),
		input: "AAAAAAAAAA\r\nBBBBBBBBBB\r\nCCCCCCCCCC\r\nDDDDDDDDDD\r\n" +
			// EL 0 from the middle of line 1, EL 1 to the middle of line 2, EL 2 on line 3.
			"\x1b[1;5H\x1b[0K" +
			"\x1b[2;5H\x1b[1K" +
			"\x1b[3;5H\x1b[2K" +
			// ED 0 from line 4 onwards.
			"\x1b[4;5H\x1b[0J",
	},
	{
		id: "VT-03", slug: "scroll-region", priority: must,
		size: sz(40, 10),
		input: "L1\r\nL2\r\nL3\r\nL4\r\nL5\r\nL6\r\n" +
			// Region rows 3-6, then scroll it both ways from inside.
			"\x1b[3;6r" +
			"\x1b[6;1Hbottom\x1bD" + // IND at the bottom scrolls the region up
			"\x1b[3;1Htop\x1bM" + // RI at the top scrolls it down
			"\x1bE" + "after-nel",
	},
	{
		id: "VT-04", slug: "sgr-basic", priority: must,
		input: "\x1b[1mbold\x1b[0m \x1b[3mitalic\x1b[0m \x1b[4munderline\x1b[0m " +
			"\x1b[7minverse\x1b[0m\r\n" +
			"\x1b[31mred\x1b[32mgreen\x1b[33myellow\x1b[34mblue\x1b[0m\r\n" +
			"\x1b[90mbright-black\x1b[97mbright-white\x1b[0m\r\n" +
			"\x1b[41;37mred-bg\x1b[0m normal",
	},
	{
		id: "VT-05", slug: "sgr-256", priority: must,
		input: "\x1b[38;5;196mfg196\x1b[0m \x1b[48;5;27mbg27\x1b[0m " +
			"\x1b[38;5;15;48;5;0mfg15bg0\x1b[0m\r\n" +
			"\x1b[38;5;232mdark\x1b[38;5;255mlight\x1b[0m",
	},
	{
		id: "VT-06", slug: "sgr-truecolor", priority: must,
		input: "\x1b[38;2;255;128;0morange\x1b[0m \x1b[48;2;0;64;128mblue-bg\x1b[0m " +
			"\x1b[38;2;0;0;0;48;2;255;255;255minverted\x1b[0m",
	},
	{
		id: "VT-07", slug: "alt-screen", priority: must,
		size:  sz(40, 6),
		input: "primary line\r\nsecond primary\r\n" + "\x1b[?1049h" + "alternate content\r\n",
	},
	{
		id: "VT-08", slug: "bracketed-paste", priority: must,
		// Enabling 2004 must not print anything; the markers around a paste are the
		// client's business, and what lands on screen is the pasted text alone.
		input: "\x1b[?2004hprompt> \x1b[200~pasted text\x1b[201~\r\n\x1b[?2004l",
	},
	{
		id: "VT-09", slug: "mouse-sgr-1006", priority: must,
		// Enabling mouse reporting changes modes, not the screen.
		input: "before\x1b[?1000h\x1b[?1006hafter\r\n",
	},
	{
		id: "VT-10", slug: "wide-chars", priority: must,
		size:  sz(20, 4),
		input: "日本語テキスト\r\nab日本cd\r\n漢字漢字漢字漢字漢字漢",
	},
	{
		id: "VT-11", slug: "grapheme-zwj", priority: must,
		size: sz(20, 4),
		// Family and profession emoji built with zero-width joiners: one logical cell each.
		input: "\U0001F468\u200D\U0001F469\u200D\U0001F467 family\r\n" +
			"\U0001F469\u200D\U0001F4BB dev\r\n" +
			"\U0001F3F4\u200D\u2620\uFE0F flag",
	},
	{
		id: "VT-12", slug: "combining", priority: must,
		size:  sz(20, 4),
		input: "e\u0301 a\u0300 o\u0308\r\nn\u0303 c\u0327\r\na\u0301\u0302\u0303 stacked",
	},
	{
		id: "VT-13", slug: "autowrap", priority: must,
		size: sz(10, 6),
		// DECAWM on: the text wraps. Off: it piles up in the last column.
		input: "\x1b[?7h0123456789WRAPPED\r\n" +
			"\x1b[?7l0123456789NOWRAP\r\n" +
			"\x1b[?7h",
	},
	{
		id: "VT-14", slug: "reflow-on-resize", priority: must,
		size:   sz(20, 6),
		resize: sz(40, 6),
		input: "this line is long enough to wrap at twenty columns\r\n" +
			"short\r\n",
	},
	{
		id: "VT-15", slug: "tabs", priority: must,
		size: sz(40, 6),
		input: "a\tb\tc\td\r\n" +
			// Clear every stop, set one at column 5, then use it.
			"\x1b[3g\x1b[1;5H\x1bH\x1b[2;1Hx\ty\r\n" +
			// TBC 0 clears the stop under the cursor.
			"\x1b[1;5H\x1b[0g\x1b[3;1Hp\tq",
	},
	{
		id: "VT-16", slug: "cursor-save-restore", priority: must,
		size: sz(40, 6),
		input: "line one\r\nline two\r\n" +
			"\x1b[1;3H\x1b7" + // DECSC at row 1 col 3, with a style
			"\x1b[3;1H\x1b[1;31mmoved and styled\x1b[0m" +
			"\x1b8" + "RESTORED",
	},
	{
		id: "VT-17", slug: "insert-delete", priority: must,
		size: sz(30, 8),
		input: "AAAA\r\nBBBB\r\nCCCC\r\nDDDD\r\n" +
			"\x1b[2;1H\x1b[L" + // IL: blank line before BBBB
			"\x1b[5;1H\x1b[M" + // DL: delete a line
			"\x1b[1;2H\x1b[3@" + // ICH: three blanks inside AAAA
			"\x1b[3;2H\x1b[2P", // DCH: delete two characters
	},
	{
		id: "VT-18", slug: "osc-title", priority: must,
		input: "\x1b]0;icon and window title\x07before\r\n" +
			"\x1b]2;window title only\x1b\\after\r\n",
	},
	{
		id: "VT-19", slug: "osc-cwd", priority: must,
		input: "\x1b]7;file://host/home/user/project\x07visible text\r\n",
	},
	{
		id: "VT-20", slug: "osc-shell-integration", priority: must,
		// The sequences the block state machine reads must leave no mark on the screen.
		input: "\x1b]133;A\x07prompt> \x1b]133;B\x07" +
			"\x1b]633;E;go test ./...\x07\x1b]133;C\x07" +
			"ok  github.com/ecrespo/umbral\r\n" +
			"\x1b]133;D;0\x07",
	},
	{
		id: "VT-21", slug: "kitty-keyboard", priority: should,
		input: "before\x1b[>1u\x1b[=5;1umiddle\x1b[<u\r\nafter\r\n",
	},
	{
		id: "VT-22", slug: "osc8-hyperlink", priority: should,
		input: "see \x1b]8;;https://umbral.example/docs\x07the docs\x1b]8;;\x07 for more\r\n",
	},
}

// TestConformance_REQ_TERM_002 runs the closed list of Tech Design §8.1 against the
// emulator and compares the result to a golden fixture.
//
// REQ-TERM-002 requires 100 % of the MUST cases. A missing fixture therefore fails rather
// than skips: a suite that silently shrinks would keep reporting success while covering
// less every time someone forgot to commit a file.
func TestConformance_REQ_TERM_002(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{}, len(conformanceCases))
	for _, tc := range conformanceCases {
		if _, dup := seen[tc.id]; dup {
			t.Fatalf("%s appears twice in the case list", tc.id)
		}
		seen[tc.id] = struct{}{}

		t.Run(tc.id+"-"+tc.slug, func(t *testing.T) {
			t.Parallel()
			runConformanceCase(t, tc)
		})
	}

	assertEveryMustCaseIsPresent(t, seen)
}

func runConformanceCase(t *testing.T, tc conformanceCase) {
	t.Helper()

	size := domain.Size{Cols: 80, Rows: 24}
	if tc.size != nil {
		size = *tc.size
	}

	term, err := NewTerminal(size.Cols, size.Rows)
	if err != nil {
		t.Fatalf("NewTerminal: %v", err)
	}
	defer term.Close()

	term.VTWrite([]byte(tc.input))
	if tc.resize != nil {
		if err := term.Resize(tc.resize.Cols, tc.resize.Rows, 0, 0); err != nil {
			t.Fatalf("resize to %dx%d: %v", tc.resize.Cols, tc.resize.Rows, err)
		}
	}

	got, err := renderCase(term, tc, size)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	base := tc.id + "-" + tc.slug
	inPath := filepath.Join(vtDir, base+".in")
	goldenPath := filepath.Join(vtDir, base+".golden")

	if *updateVT {
		writeFixture(t, inPath, []byte(tc.input))
		writeFixture(t, goldenPath, []byte(got))
		return
	}

	// The .in file is the byte stream, kept on disk so a failure can be replayed outside
	// Go with `cat`. It must match the case, or the fixture describes a different test.
	wantInput, err := os.ReadFile(inPath)
	if err != nil {
		t.Fatalf("%s: %v (run `go test -update-vt ./internal/sessions/adapters/ghostty/` to create the fixtures)",
			base, err)
	}
	if string(wantInput) != tc.input {
		t.Errorf("%s: the .in fixture does not match the case's input; regenerate it", base)
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("%s: %v (run `go test -update-vt ./internal/sessions/adapters/ghostty/` to create the fixtures)",
			base, err)
	}
	if got != string(want) {
		t.Errorf("%s differs from its golden fixture\n--- got ---\n%s\n--- want ---\n%s", base, got, want)
	}
}

// renderCase produces the fixture's content: the screen as plain text, the state a client
// cannot see in the text, and the replayable VT with its escapes made visible.
//
// The VT section is what covers "and attributes": plain text alone would pass a terminal
// that dropped every colour.
func renderCase(term *libghostty.Terminal, tc conformanceCase, size domain.Size) (string, error) {
	plain, err := PlainText(term)
	if err != nil {
		return "", err
	}
	snapshot, err := Snapshot(term, SnapshotOptions{})
	if err != nil {
		return "", err
	}
	cursorX, err := term.CursorX()
	if err != nil {
		return "", err
	}
	cursorY, err := term.CursorY()
	if err != nil {
		return "", err
	}
	title, err := term.Title()
	if err != nil {
		return "", err
	}
	pwd, err := term.Pwd()
	if err != nil {
		return "", err
	}

	dimensions := fmt.Sprintf("%dx%d", size.Cols, size.Rows)
	if tc.resize != nil {
		dimensions = fmt.Sprintf("%dx%d resized to %dx%d",
			size.Cols, size.Rows, tc.resize.Cols, tc.resize.Rows)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# %s %s (%s)\n", tc.id, tc.slug, tc.priority)
	fmt.Fprintf(&b, "# screen %s\n", dimensions)
	fmt.Fprintf(&b, "cursor: %d,%d\n", cursorX, cursorY)
	fmt.Fprintf(&b, "title: %q\n", title)
	fmt.Fprintf(&b, "pwd: %q\n", pwd)
	b.WriteString("--- plain ---\n")
	b.WriteString(plain)
	b.WriteString("\n--- vt ---\n")
	b.WriteString(escapeVT(snapshot))
	b.WriteString("\n")
	return b.String(), nil
}

// escapeVT renders a VT stream so a diff is readable: ESC becomes \e, other controls
// become \xNN, and printable bytes stay as they are.
func escapeVT(data []byte) string {
	var b strings.Builder
	for _, c := range data {
		switch {
		case c == 0x1b:
			b.WriteString("\\e")
		case c == '\\':
			b.WriteString("\\\\")
		case c == '\n':
			b.WriteString("\\n\n")
		case c < 0x20 || c == 0x7f:
			fmt.Fprintf(&b, "\\x%02x", c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// assertEveryMustCaseIsPresent checks the list against Tech Design §8.1 itself, so a case
// deleted from the code is caught rather than quietly reducing what REQ-TERM-002 means.
func assertEveryMustCaseIsPresent(t *testing.T, seen map[string]struct{}) {
	t.Helper()

	const mustCases = 20 // VT-01 … VT-20, Tech Design §8.1
	var found int
	for _, tc := range conformanceCases {
		if tc.priority == must {
			found++
		}
	}
	if found != mustCases {
		t.Errorf("the suite has %d MUST cases, want the %d of Tech Design §8.1;"+
			" adding or removing one requires a Delta", found, mustCases)
	}

	for i := 1; i <= mustCases; i++ {
		id := fmt.Sprintf("VT-%02d", i)
		if _, ok := seen[id]; !ok {
			t.Errorf("%s is missing from the suite", id)
		}
	}
}

func writeFixture(t *testing.T, path string, data []byte) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
