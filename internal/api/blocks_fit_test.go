package api

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// referencePrefix is the cut by bisection over the encoder itself, which plainPrefix must
// always agree with.
func referencePrefix(text string, room int) string {
	fits := func(n int) bool {
		encoded, _ := json.Marshal(text[:n])
		return len(encoded)-2 <= room
	}
	lo, hi := 0, len(text)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if fits(runeStart(text, mid)) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return text[:runeStart(text, lo)]
}

// TestAPlainCutMatchesTheEncoder_REQ_API_005: block.get's plain output is cut where the
// encoder itself would have cut it, for every character JSON escapes — quotes, backslashes,
// control characters, HTML's `<>&`, U+2028/U+2029 and invalid UTF-8 — and what it writes as
// is, DEL and a literal U+FFFD, at every room.
func TestAPlainCutMatchesTheEncoder_REQ_API_005(t *testing.T) {
	t.Parallel()

	texts := map[string]string{
		"ascii":     strings.Repeat("plain text ", 8),
		"escapes":   strings.Repeat("<\"\\>\t&\n\r\b\f\x01\x1f", 8),
		"runes":     strings.Repeat("é€😀a", 8),
		"separator": strings.Repeat("a\u2028b\u2029", 8),
		"invalid":   strings.Repeat("ok\xff\xc3(\xe2\x82", 8),
		"mixed":     strings.Repeat("x<é\"\u2028\xffz\t\x7f\ufffd", 8),
	}
	for name, text := range texts {
		for room := 0; room <= len(text)*6+2; room++ {
			if got, want := plainPrefix(text, room), referencePrefix(text, room); got != want {
				t.Fatalf("%s at room %d: cut at %d, the encoder cuts at %d", name, room, len(got), len(want))
			}
		}
	}
}

// TestAPlainCutEncodesAFewTimes_REQ_API_005: cutting a megabyte of transcript encodes it a
// bounded number of times, not once per step of a bisection — under the race detector on a
// loaded macOS runner twenty encodings of a megabyte took longer than a client waits. It
// swaps the package's measure, so it must not run in parallel.
func TestAPlainCutEncodesAFewTimes_REQ_API_005(t *testing.T) {
	text := strings.Repeat("<\"\\>\t", 20<<10) + strings.Repeat("é", 400<<10)
	calls := 0
	measure := jsonStringLen
	jsonStringLen = func(s string) int {
		calls++
		return measure(s)
	}
	t.Cleanup(func() { jsonStringLen = measure })

	got := plainPrefix(text, 1<<19)
	if !utf8.ValidString(got) || got != referencePrefix(text, 1<<19) {
		t.Fatalf("the cut at %d does not match the encoder's", len(got))
	}
	if calls > 3 {
		t.Errorf("the cut encoded the text %d times, want at most 3", calls)
	}
}
