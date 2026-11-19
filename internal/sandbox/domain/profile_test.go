package domain_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/ecrespo/umbral/internal/sandbox/domain"
)

// TestAWorktreesCommitPathsAreTheFourGitNeeds_REQ_SBX_001: a commit in a worktree writes into
// the main copy's `.git`, and the profile names the four paths it needs and no other — never
// the hooks or the configuration, which would run or apply as the user later.
func TestAWorktreesCommitPathsAreTheFourGitNeeds_REQ_SBX_001(t *testing.T) {
	const work, back = "/data/worktrees/r/thr_1", "/data/worktrees/r/thr_1/.git\n"
	protected := []string{"/data", "/run/umbral"}
	got := domain.GitWrites(work, "gitdir: /src/repo/.git/worktrees/thr_1\n", back, protected)
	want := []string{"/src/repo/.git/worktrees/thr_1", "/src/repo/.git/objects", "/src/repo/.git/refs", "/src/repo/.git/logs"}
	if !slices.Equal(got, want) {
		t.Fatalf("GitWrites = %v, want %v", got, want)
	}
	for _, p := range got {
		if p == "/src/repo/.git" || p == "/src/repo/.git/hooks" || p == "/src/repo/.git/config" {
			t.Errorf("the profile writes %s", p)
		}
	}
	for name, dotGit := range map[string]string{
		"empty":             "",
		"not a gitdir":      "ref: refs/heads/main\n",
		"relative":          "gitdir: ../.git/worktrees/x\n",
		"a submodule":       "gitdir: /src/repo/.git/modules/x\n",
		"the worktrees dir": "gitdir: /src/repo/.git/worktrees\n",
	} {
		if got := domain.GitWrites(work, dotGit, back, protected); got != nil {
			t.Errorf("%s: GitWrites = %v, want none", name, got)
		}
	}
}

// TestARewrittenDotGitGrantsNothing_REQ_SBX_001: the agent can write its worktree's `.git`, so
// what it names is believed only when that directory points back at this worktree. Rewritten
// to name the directory of the sibling worktrees, or any directory that does not point back,
// it grants no path; and no path that reaches Umbral's data is ever granted.
func TestARewrittenDotGitGrantsNothing_REQ_SBX_001(t *testing.T) {
	const work = "/data/worktrees/r/thr_1"
	protected := []string{"/data", "/run/umbral"}
	for name, c := range map[string]struct{ dotGit, back string }{
		"the siblings' directory, no back pointer": {"gitdir: /data/worktrees/r\n", ""},
		"another repository, pointing elsewhere":   {"gitdir: /src/other/.git/worktrees/x\n", "/src/other-tree/.git\n"},
		"a sibling's administrative directory":     {"gitdir: /src/repo/.git/worktrees/thr_2\n", "/data/worktrees/r/thr_2/.git\n"},
		"inside Umbral's data, pointing back":      {"gitdir: /data/worktrees/r\n", work + "/.git\n"},
		"a tree whose parent is Umbral's data":     {"gitdir: /data/x/worktrees/y\n", work + "/.git\n"},
	} {
		if got := domain.GitWrites(work, c.dotGit, c.back, protected); got != nil {
			t.Errorf("%s: GitWrites = %v, want none", name, got)
		}
	}
}

// TestAnAgentsProfileWritesItsOwnAndDeniesUmbrals_REQ_SBX_002: the profile writes the thread's
// directory and the agent's root, and denies reading Umbral's directories.
func TestAnAgentsProfileWritesItsOwnAndDeniesUmbrals_REQ_SBX_002(t *testing.T) {
	p, err := domain.AgentProfile(domain.Agent{
		WorkDir: "/data/worktrees/r/thr_1", OwnWorktree: true, Root: "/run/umbral/acp/thr_1",
		DotGit: "gitdir: /src/repo/.git/worktrees/thr_1", GitBack: "/data/worktrees/r/thr_1/.git",
		Extra: []string{"/home/u/.cache/go-build"}, Protected: []string{"/data", "/run/umbral"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"/data/worktrees/r/thr_1", "/run/umbral/acp/thr_1", "/src/repo/.git/objects", "/home/u/.cache/go-build"} {
		if !slices.Contains(p.Write, w) {
			t.Errorf("the profile does not write %s: %v", w, p.Write)
		}
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
