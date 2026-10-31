package desktop_test

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// kitICO is the kit's Windows icon; its 16 and 24 px entries are drawn from the hand-made
// sources, which the branding job's reproducibility check holds (REQ-PKG-005).
const kitICO = "../../assets/branding/umbral-icons/windows/umbral.ico"

// Resource types of a Windows resource directory.
const (
	rtIcon      = 3
	rtGroupIcon = 14
)

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
	if len(b) < 6+16*n {
		t.Fatalf("%s: a directory of %d entries does not fit in %d bytes", path, n, len(b))
	}
	entries := make([]icoEntry, n)
	for i := range n {
		d := b[6+16*i : 6+16*i+16]
		size := uint64(binary.LittleEndian.Uint32(d[8:]))
		off := uint64(binary.LittleEndian.Uint32(d[12:]))
		if off+size > uint64(len(b)) {
			t.Fatalf("%s: entry %d lies outside the file", path, i)
		}
		entries[i] = icoEntry{dir: d[:12], data: b[off : off+size]}
	}
	return entries
}

// rsrc is the resource section of a COFF object: its machine and, by type and id, each
// resource's data in the first language listed.
type rsrc struct {
	machine uint16
	data    map[uint32]map[uint32][]byte
}

// readSyso reads a resource object as Go's linker does: the COFF header, the `.rsrc` section,
// and its three-level directory of type, id and language. A leaf's address is an offset into
// the section, which the linker relocates.
func readSyso(t *testing.T, path string) rsrc {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v: run packaging/desktop/syso.sh", err)
	}
	if len(b) < 20 {
		t.Fatalf("%s: no COFF header", path)
	}
	le := binary.LittleEndian
	nsec, opt := int(le.Uint16(b[2:])), int(le.Uint16(b[16:]))
	var sec []byte
	for i := range nsec {
		h := 20 + opt + 40*i
		if len(b) < h+40 {
			t.Fatalf("%s: section header %d is cut", path, i)
		}
		if string(bytes.TrimRight(b[h:h+8], "\x00")) != ".rsrc" {
			continue
		}
		size, ptr := uint64(le.Uint32(b[h+16:])), uint64(le.Uint32(b[h+20:]))
		if ptr+size > uint64(len(b)) {
			t.Fatalf("%s: .rsrc lies outside the file", path)
		}
		sec = b[ptr : ptr+size]
	}
	if sec == nil {
		t.Fatalf("%s: no .rsrc section", path)
	}
	at := func(off uint32, n int) []byte {
		if uint64(off)+uint64(n) > uint64(len(sec)) {
			t.Fatalf("%s: a resource entry at %d lies outside .rsrc", path, off)
		}
		return sec[off : int(off)+n]
	}
	// entries lists a directory's (id, offset) pairs; a subdirectory's offset has its high bit set.
	entries := func(off uint32) [][2]uint32 {
		h := at(off, 16)
		n := int(le.Uint16(h[12:])) + int(le.Uint16(h[14:]))
		list := make([][2]uint32, n)
		for i := range n {
			e := at(off+16+uint32(8*i), 8)
			list[i] = [2]uint32{le.Uint32(e), le.Uint32(e[4:])}
		}
		return list
	}
	const sub = 0x80000000
	res := rsrc{machine: le.Uint16(b), data: map[uint32]map[uint32][]byte{}}
	for _, typ := range entries(0) {
		if typ[1]&sub == 0 {
			t.Fatalf("%s: type %d is not a directory", path, typ[0])
		}
		res.data[typ[0]] = map[uint32][]byte{}
		for _, id := range entries(typ[1] &^ sub) {
			langs := entries(id[1] &^ sub)
			if id[1]&sub == 0 || len(langs) == 0 || langs[0][1]&sub != 0 {
				t.Fatalf("%s: resource %d/%d has no data entry", path, typ[0], id[0])
			}
			leaf := at(langs[0][1], 16)
			res.data[typ[0]][id[0]] = at(le.Uint32(leaf), int(le.Uint32(leaf[4:])))
		}
	}
	return res
}

// TestTheWindowsResourceCarriesTheKitIcon_REQ_PKG_005 checks the resource objects Go links into
// the Windows executable (delta `2026-10-wails-icons`): each is for its own machine, has one
// icon group listing each of the kit's entries at 16, 20, 24, 32, 40, 48, 64 and 256 px once,
// and every group entry's RT_ICON id holds that entry's image byte for byte. That the
// executable embeds them is T-F2-50's check.
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
	if want := []int{16, 20, 24, 32, 40, 48, 64, 256}; !slices.Equal(sizes, want) {
		t.Fatalf("the kit's icon has entries %v, want %v", sizes, want)
	}
	for arch, machine := range map[string]uint16{"amd64": 0x8664, "arm64": 0xaa64} {
		path := filepath.Join("..", "..", "cmd", "umbral-desktop", "rsrc_windows_"+arch+".syso")
		res := readSyso(t, path)
		if res.machine != machine {
			t.Errorf("%s: machine %#x, want %#x", path, res.machine, machine)
		}
		groups := res.data[rtGroupIcon]
		if len(groups) != 1 {
			t.Fatalf("%s: %d icon groups, want 1", path, len(groups))
		}
		for _, g := range groups {
			// GRPICONDIR: reserved 0, type 1, the count, then 14 bytes per entry — the .ico's
			// 12-byte description and the RT_ICON id. Windows picks an entry by size, so the
			// order is the generator's (largest first).
			if len(g) != 6+14*len(kit) || !bytes.Equal(g[:6], []byte{0, 0, 1, 0, byte(len(kit)), 0}) {
				t.Fatalf("%s: the icon group is not %d entries", path, len(kit))
			}
			listed := make([]bool, len(kit))
			for i := range kit {
				e := g[6+14*i : 6+14*i+14]
				j := slices.IndexFunc(kit, func(k icoEntry) bool { return bytes.Equal(k.dir, e[:12]) })
				if j < 0 || listed[j] {
					t.Errorf("%s: group entry %d (% x) is none of the kit's, or listed twice", path, i, e[:12])
					continue
				}
				listed[j] = true
				id := uint32(binary.LittleEndian.Uint16(e[12:]))
				if !bytes.Equal(res.data[rtIcon][id], kit[j].data) {
					t.Errorf("%s: icon %d, named for %d px, is not the kit's %d px image", path, id, sizes[j], sizes[j])
				}
			}
		}
		if len(res.data[rtIcon]) != len(kit) {
			t.Errorf("%s: %d icons, want %d", path, len(res.data[rtIcon]), len(kit))
		}
	}
}
