package domain_test

import (
	"errors"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ecrespo/umbral/internal/sandbox/domain"
)

// TestAnAgentsProfileWritesItsOwnAndDeniesUmbrals_REQ_SBX_002: the profile writes the thread's
// directory, the agent's root and `[sandbox] write`, and denies reading Umbral's directories.
func TestAnAgentsProfileWritesItsOwnAndDeniesUmbrals_REQ_SBX_002(t *testing.T) {
	posixPaths(t)
	p, err := domain.AgentProfile(domain.Agent{
		WorkDir: "/data/worktrees/r/thr_1", OwnWorktree: true, Root: "/run/umbral/acp/thr_1",
		Extra: []string{"/home/u/.cache/go-build"}, Protected: []string{"/data", "/run/umbral"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// These and nothing else: no path of the repository's `.git` is among them, whatever the
	// worktree's own `.git` file names (REQ-SBX-001).
	if want := []string{"/data/worktrees/r/thr_1", "/run/umbral/acp/thr_1", "/home/u/.cache/go-build"}; !slices.Equal(p.Write, want) {
		t.Errorf("the profile writes %v, want %v", p.Write, want)
	}
	if !slices.Equal(p.DenyRead, []string{"/data", "/run/umbral"}) {
		t.Errorf("DenyRead = %v", p.DenyRead)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	arg, err := p.Encode()
	if err != nil {
		t.Fatal(err)
	}
	back, err := domain.Decode(arg)
	if err != nil || !slices.Equal(back.Write, p.Write) || !slices.Equal(back.DenyRead, p.DenyRead) {
		t.Fatalf("Decode(Encode) = %+v, %v", back, err)
	}
	if _, err := domain.Decode(`{"write":["relative"]}`); err == nil {
		t.Error("a relative path was decoded")
	}
	if _, err := domain.Decode(`{"write":[],"network":"all"}`); err == nil {
		t.Error("an unknown member was decoded")
	}
}

// TestAThreadWhoseDirectoryReachesUmbralsCannotBeConfined_REQ_SBX_002: a rule on a directory
// covers all beneath it, so a thread working in a directory that holds Umbral's data or its
// runtime directory — the home, the root — would be given them. It gets no profile. Neither
// does one working inside Umbral's data, unless that directory is its own worktree.
func TestAThreadWhoseDirectoryReachesUmbralsCannotBeConfined_REQ_SBX_002(t *testing.T) {
	protected := []string{"/home/u/.local/share/umbral", "/run/user/1000/umbral"}
	for name, a := range map[string]domain.Agent{
		"the home":                        {WorkDir: "/home/u"},
		"the root":                        {WorkDir: "/"},
		"the data directory's parent":     {WorkDir: "/home/u/.local/share"},
		"the runtime directory's parent":  {WorkDir: "/run/user/1000"},
		"the data directory":              {WorkDir: "/home/u/.local/share/umbral"},
		"the worktrees of every thread":   {WorkDir: "/home/u/.local/share/umbral/worktrees"},
		"a sibling's worktree, unvouched": {WorkDir: "/home/u/.local/share/umbral/worktrees/r/thr_2"},
	} {
		a.Root, a.Protected = "/run/user/1000/umbral/acp/thr_1", protected
		if p, err := domain.AgentProfile(a); !errors.Is(err, domain.ErrProfile) {
			t.Errorf("%s: profile %+v, %v; want ErrProfile", name, p, err)
		}
	}
	own := domain.Agent{
		WorkDir: "/home/u/.local/share/umbral/worktrees/r/thr_1", OwnWorktree: true,
		Root: "/run/user/1000/umbral/acp/thr_1", Protected: protected,
	}
	if _, err := domain.AgentProfile(own); err != nil {
		t.Errorf("the thread's own worktree: %v", err)
	}
	if _, err := domain.AgentProfile(domain.Agent{WorkDir: "/home/u/src/repo", Root: own.Root, Protected: protected}); err != nil {
		t.Errorf("a directory beside Umbral's: %v", err)
	}
}

// TestSandboxWriteRefusesWhatReachesAProtectedDirectory_REQ_SBX_004: `[sandbox] write` takes
// clean absolute paths, and none that is, holds or lies inside a protected directory.
func TestSandboxWriteRefusesWhatReachesAProtectedDirectory_REQ_SBX_004(t *testing.T) {
	posixPaths(t)
	protected := []string{"/home/u/.local/share/umbral", "/run/user/1000/umbral", "/home/u/.cache/ffi", ""}
	if err := domain.ValidateWrite([]string{"/home/u/.cache/go-build", "/home/u/go/pkg/mod"}, protected); err != nil {
		t.Fatalf("build caches were refused: %v", err)
	}
	for name, entry := range map[string]string{
		"relative":                  "cache",
		"not clean":                 "/home/u/../u/.cache",
		"the root":                  "/",
		"a protected directory":     "/home/u/.local/share/umbral",
		"inside one":                "/home/u/.local/share/umbral/worktrees",
		"a parent of one":           "/home/u/.local",
		"the home, which holds one": "/home/u",
		"the code cache":            "/home/u/.cache/ffi",
		"its parent":                "/home/u/.cache",
	} {
		if err := domain.ValidateWrite([]string{entry}, protected); !errors.Is(err, domain.ErrWrite) {
			t.Errorf("%s (%q): %v, want ErrWrite", name, entry, err)
		}
	}
}

// TestAReadEntryOpensOnlyWhatLiesInsideADeniedDirectory_REQ_SBX_006: a thread's own shell reads
// its start-up files, which the daemon keeps in its runtime directory. The profile carries
// them as read entries; one that is not inside a denied directory, or that is a denied
// directory or holds one, opens nothing it should and is refused.
func TestAReadEntryOpensOnlyWhatLiesInsideADeniedDirectory_REQ_SBX_006(t *testing.T) {
	posixPaths(t)
	agent := domain.Agent{
		WorkDir: "/data/worktrees/r/thr_1", OwnWorktree: true, Root: "/run/umbral/shells/thr_1",
		Read: []string{"/run/umbral/shellinteg-1"}, Protected: []string{"/data", "/run/umbral"},
	}
	p, err := domain.AgentProfile(agent)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Read, []string{"/run/umbral/shellinteg-1"}) || slices.Contains(p.Write, "/run/umbral/shellinteg-1") {
		t.Errorf("Read = %v, Write = %v", p.Read, p.Write)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	arg, err := p.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if back, err := domain.Decode(arg); err != nil || !slices.Equal(back.Read, p.Read) {
		t.Fatalf("Decode(Encode) = %+v, %v", back, err)
	}
	for name, read := range map[string]string{
		"a denied directory itself":        "/run/umbral",
		"a directory holding a denied one": "/run",
		"the data directory":               "/data",
		"a sibling's worktree":             "/data/worktrees/r/thr_2",
		"every agent's home":               "/run/umbral/acp",
		"the token":                        "/run/umbral/token",
		"one named alike, deeper":          "/run/umbral/acp/shellinteg-1",
		"the bare prefix":                  "/run/umbral/shellinteg-",
		"a relative path":                  "shellinteg-1",
	} {
		agent.Read = []string{read}
		if p, err := domain.AgentProfile(agent); !errors.Is(err, domain.ErrProfile) {
			t.Errorf("%s as a read entry: %+v, %v; want ErrProfile", name, p, err)
		}
	}
	if err := (domain.Profile{Read: []string{"relative"}}).Validate(); err == nil {
		t.Error("a relative read entry was validated")
	}
}

// posixPaths skips a test whose paths are written as POSIX ones: `/data` is not absolute on
// Windows, where no adapter builds a profile (delta `2026-11-agent-sandbox`, decision 4).
func posixPaths(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the profile's paths are POSIX paths; Windows has no sandbox adapter")
	}
}

// TestAProfilesNetworkIsCarriedToTheHelper_REQ_SBX_008: the network a profile names reaches
// the helper as written, a profile without one says nothing of it, and port 0 — the kernel's
// choice of port, and so every port — is refused on either side of the encoding.
func TestAProfilesNetworkIsCarriedToTheHelper_REQ_SBX_008(t *testing.T) {
	p := domain.Profile{Net: &domain.Net{Bind: []uint16{4096}, Connect: []uint16{4096, 51234}}}
	arg, err := p.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := domain.Decode(arg)
	if err != nil {
		t.Fatal(err)
	}
	if got.Net == nil || len(got.Net.Bind) != 1 || got.Net.Bind[0] != 4096 || len(got.Net.Connect) != 2 || got.Net.Connect[1] != 51234 {
		t.Errorf("decoded network %+v from %s", got.Net, arg)
	}
	plain, err := domain.Profile{}.Encode()
	if err != nil || strings.Contains(plain, "net") {
		t.Errorf("a profile without a network encodes as %s (%v)", plain, err)
	}
	if again, err := domain.Decode(plain); err != nil || again.Net != nil {
		t.Errorf("a profile without a network decoded with one: %+v, %v", again.Net, err)
	}
	// An empty network is not no network: it is all of it refused.
	if none, err := domain.Decode(`{"net":{}}`); err != nil || none.Net == nil {
		t.Errorf("an empty network decoded as %+v, %v", none.Net, err)
	}
	for name, bad := range map[string]domain.Profile{"a bind": {Net: &domain.Net{Bind: []uint16{0}}}, "a connect": {Net: &domain.Net{Connect: []uint16{80, 0}}}} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%s on port 0 is valid", name)
		}
	}
	if _, err := domain.Decode(`{"net":{"connect":[0]}}`); err == nil {
		t.Error("port 0 was decoded")
	}
}
