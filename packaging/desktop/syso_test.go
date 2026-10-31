package desktop_test

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// kitICO is the kit's Windows icon; its 16 and 24 px entries are drawn from the hand-made
// sources, which the branding job's reproducibility check holds (REQ-PKG-005).
const kitICO = "../../assets/branding/umbral-icons/windows/umbral.ico"

// icoEntry is one image of an .ico: its 12-byte directory description (size, colours, planes,
// bit count, byte length) and its data.
type icoEntry struct {
	dir  []byte
	data []byte
}

func readICO(t *testing.T, path string) []icoEntry {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < 6 || binary.LittleEndian.Uint16(b[2:]) != 1 {
		t.Fatalf("%s is not an icon", path)
	}
	n := int(binary.LittleEndian.Uint16(b[4:]))
	entries := make([]icoEntry, n)
	for i := range n {
		d := b[6+16*i : 6+16*i+16]
		size := binary.LittleEndian.Uint32(d[8:])
		off := binary.LittleEndian.Uint32(d[12:])
		entries[i] = icoEntry{dir: d[:12], data: b[off : off+size]}
	}
	return entries
}

// TestTheWindowsResourceCarriesTheKitIcon_REQ_PKG_005 checks the resource objects Go links into
// the Windows executable (delta `2026-10-wails-icons`): every image of the kit's icon is in
// them byte for byte, and their icon group lists each of the kit's entries, at 16, 20, 24, 32, 40,
// 48, 64 and 256 px, once. That the executable embeds them is T-F2-50's check.
func TestTheWindowsResourceCarriesTheKitIcon_REQ_PKG_005(t *testing.T) {
	t.Parallel()
	kit := readICO(t, kitICO)
	sizes := make([]int, len(kit))
	for i, e := range kit {
		sizes[i] = int(e.dir[0])
		if sizes[i] == 0 {
			sizes[i] = 256
		}
	}
	if want := []int{16, 20, 24, 32, 40, 48, 64, 256}; !equalInts(sizes, want) {
		t.Fatalf("the kit's icon has entries %v, want %v", sizes, want)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		path := filepath.Join("..", "..", "cmd", "umbral-desktop", "rsrc_windows_"+arch+".syso")
		syso, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%v: run packaging/desktop/syso.sh", err)
		}
		for i, e := range kit {
			if !bytes.Contains(syso, e.data) {
				t.Errorf("%s: the %d px image is not the kit's", path, sizes[i])
			}
		}
		// The RT_GROUP_ICON data: reserved 0, type 1, the count, then 14 bytes per entry — the
		// .ico's 12-byte description and the RT_ICON id.
		head := []byte{0, 0, 1, 0, byte(len(kit)), 0}
		at := bytes.Index(syso, head)
		if at < 0 || len(syso) < at+6+14*len(kit) {
			t.Fatalf("%s: no icon group of %d entries", path, len(kit))
		}
		// Windows picks an entry by size, so the order is the generator's (largest first); each
		// of the kit's entries must be listed exactly once.
		listed := make([]bool, len(kit))
		for i := range kit {
			got := syso[at+6+14*i : at+6+14*i+12]
			found := false
			for j, e := range kit {
				if !listed[j] && bytes.Equal(got, e.dir) {
					listed[j], found = true, true
					break
				}
			}
			if !found {
				t.Errorf("%s: group entry %d (% x) is none of the kit's", path, i, got)
			}
		}
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
