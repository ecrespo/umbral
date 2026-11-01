package domain

import (
	"strings"
	"testing"
)

// TestKittyGraphicsBecomeAPlaceholder_REQ_PLG_003: for a client without `graphics`, every
// Kitty graphics escape is taken out of a pane's output, and an image the pane displays leaves
// a placeholder where it would have been; everything else passes as it came, however the
// stream is cut (delta `2026-10-pane-graphics`).
func TestKittyGraphicsBecomeAPlaceholder_REQ_PLG_003(t *testing.T) {
	t.Parallel()
	const st = "\x1b\\"
	for _, tc := range []struct {
		name, in, want string
	}{
		{"plain output", "ls\r\nfile\r\n", "ls\r\nfile\r\n"},
		{"other escapes", "\x1b[31mred\x1b[0m\x1b]0;title\x07\x1b_Xother" + st + "\x1bP1$r" + st, "\x1b[31mred\x1b[0m\x1b]0;title\x07\x1b_Xother" + st + "\x1bP1$r" + st},
		{"transmit and display", "a\x1b_Ga=T,f=100;iVBORw0KGgo=" + st + "b", "a" + GraphicsPlaceholder + "b"},
		{"put a stored image", "\x1b_Ga=p,i=7" + st, GraphicsPlaceholder},
		{"transmit only", "x\x1b_Gf=100,i=7;iVBORw0KGgo=" + st + "y", "xy"},
		{"query", "\x1b_Ga=q,i=31,s=1,v=1,f=24;AAAA" + st, ""},
		{"delete", "\x1b_Ga=d" + st, ""},
		{"chunked display", "\x1b_Ga=T,f=100,m=1;AAAA" + st + "\x1b_Gm=1;BBBB" + st + "\x1b_Gm=0;CC" + st + "!", GraphicsPlaceholder + "!"},
		{"chunked transmit", "\x1b_Gf=100,m=1;AAAA" + st + "\x1b_Gm=0;CC" + st, ""},
		{"a unicode placeholder placement", "\x1b_Ga=T,U=1,f=100;AAAA" + st + "\U0010EEEE", "\U0010EEEE"},
		{"an escape ends a payload", "\x1b_Ga=T;AAAA\x1b[1m", GraphicsPlaceholder + "\x1b[1m"},
		{"an escape twice", "\x1b\x1b[1m", "\x1b\x1b[1m"},
		// CAN and SUB cancel an escape from any state, as the VT parser libghostty follows
		// reads them: the image is discarded and what follows is text.
		{"CAN cancels a payload", "\x1b_Ga=T;AAAA\x18text", "\x18text"},
		{"SUB cancels the control data", "\x1b_Ga=T\x1atext", "\x1atext"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var whole GraphicsFilter
			if got := string(whole.Filter(nil, []byte(tc.in))); got != tc.want {
				t.Errorf("whole: %q, want %q", got, tc.want)
			}
			// Byte by byte: a PTY read may end anywhere, inside an escape included.
			var split GraphicsFilter
			var out []byte
			for i := range len(tc.in) {
				out = split.Filter(out, []byte{tc.in[i]})
			}
			if string(out) != tc.want {
				t.Errorf("byte by byte: %q, want %q", out, tc.want)
			}
		})
	}
}

// TestOutputWithoutAnEscapeIsNotCopied: almost every batch holds no escape at all, and every
// client that does not ask for graphics filters it, so such a batch passes without a copy.
func TestOutputWithoutAnEscapeIsNotCopied(t *testing.T) {
	src := []byte(strings.Repeat("plain output\r\n", 2000))
	var f GraphicsFilter
	if allocs := testing.AllocsPerRun(100, func() { _ = f.Filter(nil, src) }); allocs != 0 {
		t.Fatalf("filtering plain output allocated %v times", allocs)
	}
}

// TestAnUnendedGraphicsEscapeHoldsNothingBack: a payload that never ends is dropped as it comes,
// not buffered, so a pane cannot grow a client's filter without bound.
func TestAnUnendedGraphicsEscapeHoldsNothingBack(t *testing.T) {
	t.Parallel()
	var f GraphicsFilter
	f.Filter(nil, []byte("\x1b_Ga=T,"+strings.Repeat("k=v,", 1000)+";"))
	for range 100 {
		if out := f.Filter(nil, []byte(strings.Repeat("A", 4096))); len(out) != 0 {
			t.Fatalf("a payload byte reached the client: %q", out[:16])
		}
	}
	if len(f.control) > maxGraphicsControl {
		t.Fatalf("the filter keeps %d bytes of control data", len(f.control))
	}
	if out := f.Filter(nil, []byte("\x1b\\after")); string(out) != GraphicsPlaceholder+"after" {
		t.Fatalf("after the end: %q", out)
	}
}
