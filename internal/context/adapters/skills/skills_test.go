package skills

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/context/domain"
	"github.com/ecrespo/umbral/internal/context/ports"
	"github.com/ecrespo/umbral/internal/store"
)

const skillMD = "---\nname: release-check\ndescription: Steps to cut a release.\nversion: \"1.2\"\n---\n# Release\n\nRun the checklist.\n"

type rig struct {
	svc    *Service
	dir    string
	db     *store.Store
	events *bus.Subscription
}

func newRig(t *testing.T) *rig {
	t.Helper()
	data := t.TempDir()
	db, err := store.Open(t.Context(), store.Options{Path: filepath.Join(data, "umbral.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	b := bus.New()
	t.Cleanup(b.Close)
	events := b.SubscribeBuffered(64, ports.KindSkillChanged)
	var mu sync.Mutex
	n := 0
	svc, err := New(Config{
		DB: db, Dir: filepath.Join(data, "skills"), Bus: b,
		NewID: func() string { mu.Lock(); defer mu.Unlock(); n++; return store.NewID(store.PrefixSkill) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return &rig{svc: svc, dir: filepath.Join(data, "skills"), db: db, events: events}
}

// bundle writes a skill directory from path → content; a path ending in "/" is a directory.
func bundle(t *testing.T, files map[string]string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "src")
	for p, content := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if strings.HasSuffix(p, "/") {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

type tarEntry struct {
	name     string
	typeflag byte
	body     []byte
	link     string
}

func tarball(t *testing.T, entries []tarEntry) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Typeflag: e.typeflag, Mode: 0o755, Size: int64(len(e.body)), Linkname: e.link}
		if e.typeflag != tar.TypeReg {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if e.typeflag == tar.TypeReg {
			if _, err := tw.Write(e.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "skill.tar.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func (r *rig) rows(t *testing.T) int {
	t.Helper()
	var n int
	if err := r.db.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM skills`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// stored lists what the skills directory holds, so a refused bundle can be shown to leave it
// as it was.
func (r *rig) stored(t *testing.T) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(r.dir, func(p string, _ os.DirEntry, err error) error {
		if err == nil && p != r.dir && filepath.Base(p) != ownerMarker {
			rel, _ := filepath.Rel(r.dir, p)
			out = append(out, rel)
		}
		return nil
	})
	return out
}

func (r *rig) changes() []ports.SkillChanged {
	var out []ports.SkillChanged
	for {
		select {
		case ev := <-r.events.C():
			out = append(out, ev.(ports.SkillChanged))
		case <-time.After(50 * time.Millisecond):
			return out
		}
	}
}

// TestInstallCopiesRecordsAndRunsNothing_REQ_SKL_001: an inspected bundle is installed
// exactly as inspected: its row records the manifest, its files are copied with normalised
// modes, a script that would leave a mark if it ran leaves none, the digest is the same from
// a directory and from a tarball, and skill.changed announces it after the row is written.
func TestInstallCopiesRecordsAndRunsNothing_REQ_SKL_001(t *testing.T) {
	r := newRig(t)
	marker := filepath.Join(t.TempDir(), "ran")
	script := "#!/bin/sh\ntouch " + marker + "\n"
	src := bundle(t, map[string]string{"SKILL.md": skillMD, "scripts/run.sh": script, "ref/notes.txt": "notes"})
	if err := os.Chmod(filepath.Join(src, "scripts", "run.sh"), 0o4755); err != nil { // setuid and exec bits
		t.Fatal(err)
	}

	m, err := r.svc.Inspect(t.Context(), src)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if m.Name != "release-check" || m.Description != "Steps to cut a release." || m.Version != "1.2" || len(m.Files) != 3 || len(m.SHA256) != 64 {
		t.Fatalf("manifest = %+v", m)
	}
	if r.rows(t) != 0 || len(r.stored(t)) != 0 {
		t.Fatal("Inspect copied or recorded something")
	}
	tgz := tarball(t, []tarEntry{
		{name: "release-check/", typeflag: tar.TypeDir},
		{name: "release-check/SKILL.md", typeflag: tar.TypeReg, body: []byte(skillMD)},
		{name: "release-check/scripts/run.sh", typeflag: tar.TypeReg, body: []byte(script)},
		{name: "release-check/ref/notes.txt", typeflag: tar.TypeReg, body: []byte("notes")},
	})
	if mt, err := r.svc.Inspect(t.Context(), tgz); err != nil || mt.SHA256 != m.SHA256 {
		t.Fatalf("the tarball's digest %q (%v) differs from the directory's %q", mt.SHA256, err, m.SHA256)
	}

	sk, err := r.svc.Install(t.Context(), domain.InstallParams{Path: src, ExpectedSHA256: m.SHA256})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !strings.HasPrefix(sk.ID, "skl_") || sk.Name != m.Name || sk.SHA256 != m.SHA256 || sk.Source != src || !sk.Enabled || sk.SizeBytes != m.SizeBytes || sk.InstalledAt == 0 {
		t.Fatalf("skill = %+v", sk)
	}
	if got, err := r.svc.List(t.Context()); err != nil || len(got) != 1 || got[0] != sk {
		t.Fatalf("List = %+v %v", got, err)
	}
	installed := filepath.Join(r.dir, "release-check")
	for p, want := range map[string]os.FileMode{"": 0o755, "scripts": 0o755, "SKILL.md": 0o644, "scripts/run.sh": 0o644, "ref/notes.txt": 0o644} {
		info, err := os.Stat(filepath.Join(installed, filepath.FromSlash(p)))
		if err != nil {
			t.Fatalf("%s not installed: %v", p, err)
		}
		if mode := info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky); mode != want {
			t.Errorf("%s has mode %v, want %v", p, mode, want)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(installed, "scripts", "run.sh")); string(got) != script {
		t.Errorf("the script was not copied as it was: %q", got)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("installing ran the bundle's script")
	}
	if ch := r.changes(); len(ch) != 1 || ch[0] != (ports.SkillChanged{Name: "release-check", Change: "installed"}) {
		t.Errorf("skill.changed = %+v", ch)
	}
	// Editing the source changes nothing: the installed copy is what is used.
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, body, err := r.svc.Get(t.Context(), "release-check", true); err != nil || body != "# Release\n\nRun the checklist.\n" {
		t.Errorf("Get's body = %q, %v", body, err)
	}
}

// TestABadBundleIsRefusedWhole_REQ_SKL_002: a bundle breaking any check is refused with a
// validation error naming the entry, and leaves no row and no file; a changed digest and a
// duplicate name without replace are conflicts.
func TestABadBundleIsRefusedWhole_REQ_SKL_002(t *testing.T) {
	r := newRig(t)
	big := strings.Repeat("x", 2<<20)

	symlinked := bundle(t, map[string]string{"SKILL.md": skillMD})
	if err := os.Symlink("/etc/passwd", filepath.Join(symlinked, "link")); err != nil {
		t.Fatal(err)
	}
	linkedTwice := bundle(t, map[string]string{"SKILL.md": skillMD, "a.txt": "a"})
	if err := os.Link(filepath.Join(linkedTwice, "a.txt"), filepath.Join(linkedTwice, "b.txt")); err != nil {
		t.Fatal(err)
	}
	fifo := bundle(t, map[string]string{"SKILL.md": skillMD})
	if err := syscall.Mkfifo(filepath.Join(fifo, "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	bomb := make([]tarEntry, 0, 10)
	bomb = append(bomb, tarEntry{name: "SKILL.md", typeflag: tar.TypeReg, body: []byte(skillMD)})
	for i := range 9 { // nine 1 MiB files: each within the file limit, together past the bundle's
		bomb = append(bomb, tarEntry{name: "f" + string(rune('0'+i)), typeflag: tar.TypeReg, body: bytes.Repeat([]byte{0}, 1<<20)})
	}
	many := make([]tarEntry, 0, 301)
	many = append(many, tarEntry{name: "SKILL.md", typeflag: tar.TypeReg, body: []byte(skillMD)})
	for i := range 300 {
		many = append(many, tarEntry{name: "d/" + strings.Repeat("a", 1+i%50) + string(rune('A'+i%26)) + string(rune('a'+i/26)), typeflag: tar.TypeReg, body: []byte("x")})
	}

	for _, tc := range []struct{ name, path, want string }{
		{"a symlink", symlinked, "link"},
		{"a hardlink", linkedTwice, ".txt"},
		{"a FIFO", fifo, "pipe"},
		{"a .. entry", tarball(t, []tarEntry{{name: "SKILL.md", typeflag: tar.TypeReg, body: []byte(skillMD)}, {name: "../escape", typeflag: tar.TypeReg, body: []byte("x")}}), "../escape"},
		{"an absolute entry", tarball(t, []tarEntry{{name: "/etc/x", typeflag: tar.TypeReg, body: []byte("x")}, {name: "SKILL.md", typeflag: tar.TypeReg, body: []byte(skillMD)}}), "/etc/x"},
		{"a symlink in a tarball", tarball(t, []tarEntry{{name: "SKILL.md", typeflag: tar.TypeReg, body: []byte(skillMD)}, {name: "l", typeflag: tar.TypeSymlink, link: "/etc/passwd"}}), "l"},
		{"a 2 MiB file", bundle(t, map[string]string{"SKILL.md": skillMD, "big.txt": big}), "big.txt"},
		{"a tar bomb", tarball(t, bomb), "8 MiB"},
		{"too many entries", tarball(t, many), "256"},
		{"no SKILL.md", bundle(t, map[string]string{"README.md": "x"}), "SKILL.md"},
		{"a bad name", bundle(t, map[string]string{"SKILL.md": strings.Replace(skillMD, "release-check", "Release_Check", 1)}), "name"},
		{"no description", bundle(t, map[string]string{"SKILL.md": "---\nname: x\n---\nbody\n"}), "description"},
		{"no front matter", bundle(t, map[string]string{"SKILL.md": "# just markdown\n"}), "front matter"},
		{"a relative path", "relative/dir", "absolute"},
	} {
		_, err := r.svc.Inspect(t.Context(), tc.path)
		if !errors.Is(err, domain.ErrSkillValidation) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Inspect = %v, want a validation error naming %q", tc.name, err, tc.want)
		}
		_, err = r.svc.Install(t.Context(), domain.InstallParams{Path: tc.path, ExpectedSHA256: strings.Repeat("0", 64)})
		if !errors.Is(err, domain.ErrSkillValidation) {
			t.Errorf("%s: Install = %v, want a validation error", tc.name, err)
		}
	}
	if n, left := r.rows(t), r.stored(t); n != 0 || len(left) != 0 {
		t.Fatalf("refused bundles left %d rows and %v", n, left)
	}

	good := bundle(t, map[string]string{"SKILL.md": skillMD})
	m, err := r.svc.Inspect(t.Context(), good)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(good, "late.txt"), []byte("added after the look"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Install(t.Context(), domain.InstallParams{Path: good, ExpectedSHA256: m.SHA256}); !errors.Is(err, domain.ErrSkillConflict) {
		t.Fatalf("a bundle changed after inspection: %v, want a conflict", err)
	}
	if r.rows(t) != 0 || len(r.stored(t)) != 0 {
		t.Fatal("a changed bundle was installed")
	}
	m, _ = r.svc.Inspect(t.Context(), good)
	if _, err := r.svc.Install(t.Context(), domain.InstallParams{Path: good, ExpectedSHA256: m.SHA256}); err != nil {
		t.Fatal(err)
	}
	r.changes()
	second := bundle(t, map[string]string{"SKILL.md": strings.Replace(skillMD, "Run the checklist.", "Run it twice.", 1)})
	m2, _ := r.svc.Inspect(t.Context(), second)
	if _, err := r.svc.Install(t.Context(), domain.InstallParams{Path: second, ExpectedSHA256: m2.SHA256}); !errors.Is(err, domain.ErrSkillConflict) {
		t.Fatalf("a duplicate name without replace: %v, want a conflict", err)
	}
	if _, body, _ := r.svc.Get(t.Context(), "release-check", true); !strings.Contains(body, "Run the checklist.") {
		t.Fatalf("a refused duplicate changed the stored copy: %q", body)
	}
	sk, err := r.svc.Install(t.Context(), domain.InstallParams{Path: second, ExpectedSHA256: m2.SHA256, Replace: true})
	if err != nil || sk.SHA256 != m2.SHA256 {
		t.Fatalf("replace: %+v %v", sk, err)
	}
	if _, body, _ := r.svc.Get(t.Context(), "release-check", true); !strings.Contains(body, "Run it twice.") {
		t.Errorf("replace left the old copy: %q", body)
	}
	if left := r.stored(t); len(left) != 2 { // release-check and its SKILL.md, nothing set aside
		t.Errorf("after a replace the store holds %v", left)
	}
	if ch := r.changes(); len(ch) != 1 || ch[0].Change != "replaced" {
		t.Errorf("skill.changed after a replace = %+v", ch)
	}
}

// TestACrashMidInstallLeavesNothingAfterRestart_REQ_SKL_002: what an interrupted install,
// replace or remove leaves behind — a staging directory, a directory without a row, an old
// copy set aside — is resolved at the next start (Data Model §6 step 9).
func TestACrashMidInstallLeavesNothingAfterRestart_REQ_SKL_002(t *testing.T) {
	r := newRig(t)
	src := bundle(t, map[string]string{"SKILL.md": skillMD})
	m, _ := r.svc.Inspect(t.Context(), src)
	if _, err := r.svc.Install(t.Context(), domain.InstallParams{Path: src, ExpectedSHA256: m.SHA256}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{".staging-skl_crashed", "orphan", ".old-orphan2"} {
		if err := os.MkdirAll(filepath.Join(r.dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(r.dir, d, "SKILL.md"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A replace interrupted after its old copy was set aside and before the new one arrived.
	if err := os.Rename(filepath.Join(r.dir, "release-check"), filepath.Join(r.dir, ".old-release-check")); err != nil {
		t.Fatal(err)
	}

	restarted, err := New(Config{DB: r.db, Dir: r.dir, Bus: bus.New(), NewID: func() string { return store.NewID(store.PrefixSkill) }})
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Recover(t.Context()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if left := r.stored(t); strings.Join(left, ",") != "release-check,release-check/SKILL.md" {
		t.Fatalf("after recovery the store holds %v", left)
	}
	if _, body, err := restarted.Get(t.Context(), "release-check", true); err != nil || !strings.Contains(body, "Run the checklist.") {
		t.Errorf("the recorded skill after recovery: %q %v", body, err)
	}
}

// TestARemoteSourceIsRefused_REQ_SKL_006: a URL, a git remote or scp-like syntax is refused
// as not local before any path is resolved.
func TestARemoteSourceIsRefused_REQ_SKL_006(t *testing.T) {
	r := newRig(t)
	for _, src := range []string{"https://example.com/skill.tar.gz", "file:///tmp/skill", "git@github.com:a/b.git", "user@host:skills/x", "ssh://host/x"} {
		_, err := r.svc.Inspect(t.Context(), src)
		if !errors.Is(err, domain.ErrSkillValidation) || !strings.Contains(err.Error(), "local") {
			t.Errorf("Inspect(%q) = %v, want a validation error saying it is not local", src, err)
		}
		_, err = r.svc.Install(t.Context(), domain.InstallParams{Path: src, ExpectedSHA256: strings.Repeat("0", 64)})
		if !errors.Is(err, domain.ErrSkillValidation) || !strings.Contains(err.Error(), "local") {
			t.Errorf("Install(%q) = %v, want a validation error saying it is not local", src, err)
		}
	}
	if !domain.IsRemoteSource("me@host:path") || domain.IsRemoteSource("/home/me/skill") || domain.IsRemoteSource("/srv/a@b/skill") {
		t.Error("IsRemoteSource misclassifies")
	}
}

// TestTheSixtyFifthSkillIsStoredDisabled_REQ_SKL_004: at most 64 skills are enabled; a 65th
// install is stored disabled and enabling it is refused until one is disabled.
func TestTheSixtyFifthSkillIsStoredDisabled_REQ_SKL_004(t *testing.T) {
	r := newRig(t)
	install := func(name string) domain.Skill {
		t.Helper()
		src := bundle(t, map[string]string{"SKILL.md": strings.Replace(skillMD, "release-check", name, 1)})
		m, err := r.svc.Inspect(t.Context(), src)
		if err != nil {
			t.Fatal(err)
		}
		sk, err := r.svc.Install(t.Context(), domain.InstallParams{Path: src, ExpectedSHA256: m.SHA256})
		if err != nil {
			t.Fatal(err)
		}
		return sk
	}
	for i := range domain.MaxEnabledSkills {
		if sk := install("s" + strings.Repeat("x", i)); !sk.Enabled {
			t.Fatalf("skill %d was stored disabled", i+1)
		}
	}
	extra := install("extra")
	if extra.Enabled {
		t.Fatal("the 65th skill was stored enabled")
	}
	r.changes()
	if _, err := r.svc.SetEnabled(t.Context(), "extra", true); !errors.Is(err, domain.ErrSkillValidation) || !strings.Contains(err.Error(), "64") {
		t.Fatalf("enabling a 65th: %v, want a validation error naming the limit", err)
	}
	if sk, err := r.svc.SetEnabled(t.Context(), "s", false); err != nil || sk.Enabled {
		t.Fatalf("disable: %+v %v", sk, err)
	}
	if sk, err := r.svc.SetEnabled(t.Context(), "extra", true); err != nil || !sk.Enabled {
		t.Fatalf("enable after a place freed: %+v %v", sk, err)
	}
	if ch := r.changes(); len(ch) != 2 || ch[0].Change != "disabled" || ch[1].Change != "enabled" {
		t.Errorf("skill.changed = %+v", ch)
	}
	if err := r.svc.Remove(t.Context(), "extra"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "extra")); !errors.Is(err, os.ErrNotExist) {
		t.Error("remove left the directory")
	}
	if err := r.svc.Remove(t.Context(), "extra"); !errors.Is(err, domain.ErrSkillNotFound) {
		t.Errorf("removing it again: %v, want not found", err)
	}
	if _, _, err := r.svc.Get(t.Context(), "nope", false); !errors.Is(err, domain.ErrSkillNotFound) {
		t.Errorf("Get of an unknown skill: %v", err)
	}
}

// TestAnInterruptedReplaceKeepsTheRecordedCopy_REQ_SKL_002: a replace that crashed after its
// new copy moved in and before the row was updated leaves files the row does not describe;
// recovery puts the recorded copy back (delta `2026-10-skill-store`, decision 9).
func TestAnInterruptedReplaceKeepsTheRecordedCopy_REQ_SKL_002(t *testing.T) {
	r := newRig(t)
	src := bundle(t, map[string]string{"SKILL.md": skillMD})
	m, _ := r.svc.Inspect(t.Context(), src)
	if _, err := r.svc.Install(t.Context(), domain.InstallParams{Path: src, ExpectedSHA256: m.SHA256}); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(r.dir, "release-check")
	if err := os.Rename(name, filepath.Join(r.dir, ".old-release-check")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(name, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(name, "SKILL.md"), []byte(strings.Replace(skillMD, "Run the checklist.", "Unrecorded.", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, body, err := r.svc.Get(t.Context(), "release-check", true); err != nil || !strings.Contains(body, "Run the checklist.") {
		t.Fatalf("after recovery the stored copy is %q (%v), want the recorded one", body, err)
	}
	if left := r.stored(t); strings.Join(left, ",") != "release-check,release-check/SKILL.md" {
		t.Errorf("after recovery the store holds %v", left)
	}
}

// TestAForeignSkillsDirectoryIsNeverSwept_REQ_SKL_002: with --db pointing elsewhere, a
// `skills/` directory Umbral did not create is not Umbral's to sweep; the store refuses it
// rather than deleting the user's files (decision 2).
func TestAForeignSkillsDirectoryIsNeverSwept_REQ_SKL_002(t *testing.T) {
	data := t.TempDir()
	db, err := store.Open(t.Context(), store.Options{Path: filepath.Join(data, "umbral.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mine := filepath.Join(data, "skills", "my-drafts")
	if err := os.MkdirAll(mine, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mine, "SKILL.md"), []byte("draft"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = New(Config{DB: db, Dir: filepath.Join(data, "skills"), NewID: func() string { return store.NewID(store.PrefixSkill) }})
	if err == nil || !strings.Contains(err.Error(), "not Umbral's") {
		t.Fatalf("New over a foreign directory: %v, want a refusal", err)
	}
	if _, err := os.Stat(filepath.Join(mine, "SKILL.md")); err != nil {
		t.Fatalf("the user's file is gone: %v", err)
	}
	// An empty or missing directory becomes Umbral's.
	r := newRig(t)
	if _, err := os.Stat(filepath.Join(r.dir, ownerMarker)); err != nil {
		t.Errorf("a new store has no owner marker: %v", err)
	}
}

// TestMoreBadBundles_REQ_SKL_002: the checks hold for a tarball as for a directory, a
// truncated tarball is refused whole, and a bundle install could not write is refused at
// inspection, naming the entry.
func TestMoreBadBundles_REQ_SKL_002(t *testing.T) {
	r := newRig(t)
	md := tarEntry{name: "SKILL.md", typeflag: tar.TypeReg, body: []byte(skillMD)}
	for _, tc := range []struct{ name, path, want string }{
		{"a 2 MiB file in a tarball", tarball(t, []tarEntry{md, {name: "big", typeflag: tar.TypeReg, body: bytes.Repeat([]byte{1}, 2<<20)}}), "big"},
		{"a hardlink in a tarball", tarball(t, []tarEntry{md, {name: "h", typeflag: tar.TypeLink, link: "SKILL.md"}}), "h"},
		{"a device in a tarball", tarball(t, []tarEntry{md, {name: "dev", typeflag: tar.TypeChar}}), "dev"},
		{"a file under a file", tarball(t, []tarEntry{md, {name: "a", typeflag: tar.TypeReg, body: []byte("x")}, {name: "a/b", typeflag: tar.TypeReg, body: []byte("y")}}), "a/b"},
		{"a backslash in a name", tarball(t, []tarEntry{md, {name: `a\b`, typeflag: tar.TypeReg, body: []byte("x")}}), "backslash"},
	} {
		if _, err := r.svc.Inspect(t.Context(), tc.path); !errors.Is(err, domain.ErrSkillValidation) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want a validation error naming %q", tc.name, err, tc.want)
		}
	}
}

// TestATruncatedStreamIsRefused_REQ_SKL_002: a tarball whose stream reaches the bound exactly
// on a header block — long PAX names add header bytes the content limits do not count — reads
// as a clean end to the tar reader. It is refused whole, never read as the entries before the
// cut.
func TestATruncatedStreamIsRefused_REQ_SKL_002(t *testing.T) {
	r := newRig(t)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	raw := &countingWriter{}
	tw := tar.NewWriter(io.MultiWriter(gz, raw))
	add := func(name string, data []byte) {
		t.Helper()
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := tw.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	add("SKILL.md", []byte(skillMD))
	for i := 0; raw.n < maxTarStream-(2<<20); i++ {
		add(fmt.Sprintf("d/%s%03d", strings.Repeat("n", 30000), i), bytes.Repeat([]byte{3}, 180<<10))
	}
	// Fillers with short names land the stream exactly on the bound.
	for i := 0; raw.n < maxTarStream; i++ {
		left := maxTarStream - raw.n - 512 // one ustar header block
		add(fmt.Sprintf("f%d", i), bytes.Repeat([]byte{3}, min(left, 512<<10)))
	}
	if raw.n != maxTarStream {
		t.Fatalf("the stream is %d bytes, want exactly %d", raw.n, maxTarStream)
	}
	add("after-the-cut", []byte{1})
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "cut.tar.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if m, err := r.svc.Inspect(t.Context(), path); !errors.Is(err, domain.ErrSkillValidation) {
		t.Fatalf("a stream cut at the bound: %d files, %v; want a validation error", len(m.Files), err)
	}
}

type countingWriter struct{ n int }

func (c *countingWriter) Write(p []byte) (int, error) { c.n += len(p); return len(p), nil }
