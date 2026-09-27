package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/client"
)

// bootstrapPrefix is what shellinteg names its directories. Spelled out here rather than
// imported, so that a rename in the adapter has to be made deliberately on both sides
// instead of silently turning this file's assertions into no-ops.
const bootstrapPrefix = "shellinteg-"

// TestStartSweepsOrphanedBootstrapDirectories_REQ_TERM_012 is the requirement's own
// sentence, against a real daemon: what a killed run left behind is gone once the next one
// is serving.
//
// The directory is planted rather than produced by a `kill -9`, because what the sweep can
// see is a directory with no owner, and that is exactly what a planted one is. The killed
// daemon is covered by the measured teeth check recorded in the task.
func TestStartSweepsOrphanedBootstrapDirectories_REQ_TERM_012(t *testing.T) {
	bin := buildDaemon(t)
	runtimeRoot, daemonDir := isolatedRuntime(t)

	orphan := filepath.Join(daemonDir, bootstrapPrefix+"fromacrash")
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "umbral.bash"), []byte("# rc"), 0o600); err != nil {
		t.Fatal(err)
	}

	startDaemon(t, bin, runtimeRoot)

	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("the orphaned bootstrap directory survived the start: %v", err)
	}
	// The daemon's own files live in that same directory, and a sweep that took them would
	// have destroyed the installation it was tidying.
	for _, name := range []string{"umbral.sock", "umbrald.lock"} {
		if _, err := os.Stat(filepath.Join(daemonDir, name)); err != nil {
			t.Errorf("the sweep removed %s, which belongs to the running daemon: %v", name, err)
		}
	}
}

// TestSweepLeavesTheRunningSessionsAlone_REQ_TERM_012 pins the ordering, which is the only
// thing keeping the sweep safe: it runs after the instance lock and *before* the restore, so
// the shells the restore launches cannot have their bootstrap swept from under them.
//
// Moving the call below `workspaceService.Restore` leaves every other test in this file
// green and breaks this one — the restored pane would come back without shell integration,
// so its blocks would stop working, silently, on every restart.
func TestSweepLeavesTheRunningSessionsAlone_REQ_TERM_012(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("no bash on this machine, so no session here gets a shell bootstrap at all: %v", err)
	}

	bin := buildDaemon(t)
	runtimeRoot, daemonDir := isolatedRuntime(t)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()

	// The panes run real bash, which reads ~/.bashrc and writes ~/.bash_history on the
	// hang-up a killed daemon delivers; neither may be the developer's.
	env := []string{"SHELL=" + bash, "HOME=" + t.TempDir()}

	// First run: a workspace with a pane, whose shell gets a bootstrap directory.
	stopFirst := startStoppableDaemon(t, bin, runtimeRoot, env...)
	conn, err := client.Connect(ctx, client.Options{DaemonPath: bin, ClientKind: client.ClientKindTUI})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	createWorkspace(ctx, t, conn, t.TempDir())
	if got := waitForBootstrapDirs(t, daemonDir, 1); len(got) == 0 {
		t.Fatal("the pane's shell got no bootstrap directory in the runtime directory: " +
			"either the adapter is not wired to it, or the shell has no integration")
	}
	_ = conn.Close()
	// Killed, not stopped: a clean stop removes its own bootstrap directories, so only a
	// crash leaves the orphans this requirement exists for.
	stopFirst(os.Kill)

	// Whatever survived the kill is now an orphan: nothing owns it.
	before := bootstrapDirs(t, daemonDir)
	if len(before) == 0 {
		t.Fatal("the killed daemon left no bootstrap directory behind, so there is nothing " +
			"for the restart to sweep and the assertions below would prove nothing")
	}

	// Second run: the restore rebuilds the pane and gives it a fresh shell, and that
	// shell's bootstrap must still be there once the daemon is serving.
	startDaemon(t, bin, runtimeRoot, env...)

	after := bootstrapDirs(t, daemonDir)
	fresh := 0
	for _, dir := range after {
		if !contains(before, dir) {
			fresh++
		}
	}
	if fresh == 0 {
		t.Errorf("no bootstrap directory belongs to the restored pane: before=%v after=%v; "+
			"the sweep ran after the restore and took the shell's own files", before, after)
	}
	for _, dir := range before {
		if contains(after, dir) {
			t.Errorf("%s was left by the previous run and survived this start", dir)
		}
	}
}

// TestSweepFailureDoesNotStopTheDaemon_REQ_TERM_012 is decision 4 of the delta: a few
// kilobytes of litter that cannot be removed must not become an outage. The daemon is what
// the user is waiting for.
func TestSweepFailureDoesNotStopTheDaemon_REQ_TERM_012(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so no removal can be made to fail here")
	}

	bin := buildDaemon(t)
	runtimeRoot, daemonDir := isolatedRuntime(t)

	stuck := filepath.Join(daemonDir, bootstrapPrefix+"stuck")
	if err := os.MkdirAll(stuck, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stuck, "umbral.bash"), []byte("# rc"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Without write permission the child cannot be unlinked, so the removal fails the way a
	// permission the user changed makes it fail.
	if err := os.Chmod(stuck, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stuck, 0o700) })

	// startDaemon fails the test if the socket never appears, which is the assertion: the
	// daemon served although the sweep could not finish.
	startDaemon(t, bin, runtimeRoot)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	conn, err := client.Connect(ctx, client.Options{DaemonPath: bin, ClientKind: client.ClientKindTUI})
	if err != nil {
		t.Fatalf("the daemon did not answer after a sweep it could not finish: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.Call(ctx, "session.snapshot", nil, nil); err != nil {
		t.Errorf("session.snapshot after a failed sweep: %v", err)
	}
	if _, err := os.Stat(stuck); err != nil {
		t.Errorf("the directory that could not be removed is gone, so the test proved nothing: %v", err)
	}
}

// isolatedRuntime redirects every XDG directory the daemon writes to and returns the
// runtime root together with the daemon's own directory inside it.
//
// Every one of them, not only XDG_RUNTIME_DIR: a daemon that inherits the developer's
// XDG_DATA_HOME opens the developer's database, which is how the suite came to hold 2490
// test sessions in it (docs/checkpoints/2026-09-20-f0-closure.md).
func isolatedRuntime(t *testing.T) (runtimeRoot, daemonDir string) {
	t.Helper()

	dir := socketDir(t)
	runtimeRoot = filepath.Join(dir, "run")
	daemonDir = filepath.Join(runtimeRoot, "umbral")
	if err := os.MkdirAll(daemonDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeRoot)
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	return runtimeRoot, daemonDir
}

// bootstrapDirs lists the bootstrap directories currently in the daemon's runtime directory.
func bootstrapDirs(t *testing.T, daemonDir string) []string {
	t.Helper()

	entries, err := os.ReadDir(daemonDir)
	if err != nil {
		t.Fatalf("read %s: %v", daemonDir, err)
	}
	var found []string
	for _, entry := range entries {
		if entry.IsDir() && len(entry.Name()) > len(bootstrapPrefix) && entry.Name()[:len(bootstrapPrefix)] == bootstrapPrefix {
			found = append(found, entry.Name())
		}
	}
	sort.Strings(found)
	return found
}

// waitForBootstrapDirs waits for at least n of them, because the shell is launched
// asynchronously from the call that created the pane.
func waitForBootstrapDirs(t *testing.T, daemonDir string, n int) []string {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for {
		found := bootstrapDirs(t, daemonDir)
		if len(found) >= n || time.Now().After(deadline) {
			return found
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func contains(haystack []string, needle string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}
