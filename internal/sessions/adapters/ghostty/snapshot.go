// Package ghostty adapts libghostty-vt to the sessions module: VT emulation and the
// replayable screen snapshot that `session.subscribe` sends before the live stream
// (REQ-TERM-004, DD-001).
//
// The daemon owns the VT state and clients render from bytes, so a client that connects
// to a running session needs the screen as VT rather than as a description of it. That is
// what Snapshot produces: a byte sequence which, fed to an empty emulator, reproduces the
// screen the daemon holds.
//
// Building this package requires libghostty-vt on PKG_CONFIG_PATH. See
// `scripts/build_libghostty.sh` and `task deps:ghostty`.
package ghostty

import (
	"fmt"

	"go.mitchellh.com/libghostty"

	"github.com/ecrespo/umbral/internal/sessions/domain"
)

// ScrollbackLines is the snapshot's history cap, taken from the domain so the emulator
// cannot quietly disagree with the promise the API makes (REQ-TERM-004).
const ScrollbackLines = domain.MaxScrollbackLines

// ScrollbackBytes is the memory budget that must accompany ScrollbackLines.
//
// It is not a second, independent knob: libghostty applies the byte budget and the line
// budget together and prunes on whichever binds first. Its default byte budget is small,
// so setting only the line limit is inert. Measured on 120-column lines: the default
// retains 588 lines, `WithMaxScrollbackLines(10_000)` alone still retains 588, and only
// with a byte budget this size does the line limit take effect, at 9,876 lines. Anyone
// who sets the line limit alone gets a terminal that silently keeps 6 % of the history
// the spec promises. See docs/spikes/q01-snapshot.md.
const ScrollbackBytes = 64 << 20 // 64 MiB

// NewTerminal builds an emulator configured the way REQ-TERM-004 needs, with both
// scrollback limits set.
func NewTerminal(cols, rows uint16) (*libghostty.Terminal, error) {
	term, err := libghostty.NewTerminal(
		libghostty.WithSize(cols, rows),
		libghostty.WithMaxScrollbackLines(ScrollbackLines),
		libghostty.WithMaxScrollbackBytes(ScrollbackBytes),
	)
	if err != nil {
		return nil, fmt.Errorf("ghostty: create terminal %dx%d: %w", cols, rows, err)
	}
	return term, nil
}

// SnapshotOptions tunes what a snapshot carries.
type SnapshotOptions struct {
	// Palette includes the 256-entry colour palette as OSC 4 sequences.
	//
	// Off by default because it costs a flat 5.5 KiB regardless of screen content, which
	// on a small screen is 98 % of the message. It is only needed when the program
	// running in the session redefined the palette; callers that track that turn it on.
	Palette bool
}

// Snapshot renders the terminal as replayable VT.
//
// Feeding the result to an empty emulator of the same size reproduces the screen: text,
// scrollback, SGR styles, cursor position, modes, scrolling region, tabstops, working
// directory, hyperlinks and keyboard state. The operation is a fixed point, so
// snapshotting the replayed terminal yields the same bytes again.
//
// It does not include the primary screen while the alternate screen is active. That is
// correct rather than a limitation: the alternate screen is what the user is looking at,
// and the snapshot carries mode 1049 so the replaying emulator enters it too.
func Snapshot(term *libghostty.Terminal, opts SnapshotOptions) ([]byte, error) {
	formatter, err := libghostty.NewFormatter(term,
		libghostty.WithFormatterFormat(libghostty.FormatterFormatVT),
		libghostty.WithFormatterTrim(true),
		libghostty.WithFormatterExtraPalette(opts.Palette),
		// Everything below is screen state a client cannot infer from the text, and
		// omitting any of it makes the replayed screen subtly wrong rather than
		// obviously broken, which is the worse failure.
		libghostty.WithFormatterExtraModes(true),
		libghostty.WithFormatterExtraScrollingRegion(true),
		libghostty.WithFormatterExtraTabstops(true),
		libghostty.WithFormatterExtraPwd(true),
		libghostty.WithFormatterExtraKeyboard(true),
		libghostty.WithFormatterExtraCursor(true),
		libghostty.WithFormatterExtraStyle(true),
		libghostty.WithFormatterExtraHyperlink(true),
		libghostty.WithFormatterExtraProtection(true),
		libghostty.WithFormatterExtraKittyKeyboard(true),
		libghostty.WithFormatterExtraCharsets(true),
	)
	if err != nil {
		return nil, fmt.Errorf("ghostty: create snapshot formatter: %w", err)
	}
	defer formatter.Close()

	out, err := formatter.Format()
	if err != nil {
		return nil, fmt.Errorf("ghostty: format snapshot: %w", err)
	}
	return out, nil
}

// PlainText renders the screen as text with no escape sequences. It is what `block`
// storage keeps (REQ-BLK-007) and what the snapshot tests compare.
func PlainText(term *libghostty.Terminal) (string, error) {
	formatter, err := libghostty.NewFormatter(term,
		libghostty.WithFormatterFormat(libghostty.FormatterFormatPlain),
		libghostty.WithFormatterTrim(true),
	)
	if err != nil {
		return "", fmt.Errorf("ghostty: create plain formatter: %w", err)
	}
	defer formatter.Close()

	out, err := formatter.FormatString()
	if err != nil {
		return "", fmt.Errorf("ghostty: format plain text: %w", err)
	}
	return out, nil
}
