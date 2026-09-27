package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/client"
	"github.com/ecrespo/umbral/internal/store"
)

// TestASecondRuntimeCannotRecoverALiveDatabase is delta `2026-09-database-lock` against real
// daemons.
//
// Recovery (Data Model §6) marks every `alive` session `exited`, on the premise that the
// process that owned it is gone. The instance lock that protects that premise lives in the
// runtime directory, so a second daemon started with another `--socket` and the same `--db`
// took a lock of its own and recovered over the first one's running sessions. Run with
// `--check` — open, recover, exit — the second daemon must now refuse with 75 and leave the
// first one's session alive.
func TestASecondRuntimeCannotRecoverALiveDatabase(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("no bash: %v", err)
	}
	bin := buildDaemon(t)
	runtimeRoot, _ := isolatedRuntime(t)
	env := []string{"SHELL=" + bash, "HOME=" + t.TempDir()}

	startDaemon(t, bin, runtimeRoot, env...)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	conn, err := client.Connect(ctx, client.Options{DaemonPath: bin, ClientKind: client.ClientKindTUI})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close() }()
	createWorkspace(ctx, t, conn, t.TempDir())

	db := filepath.Join(os.Getenv("XDG_DATA_HOME"), "umbral", "umbral.db")
	if got := aliveSessions(t, db); got == 0 {
		t.Fatal("the first daemon has no alive session, so there is nothing for the second to destroy")
	}

	// Another runtime directory entirely, and the first daemon's database.
	other := filepath.Join(socketDir(t), "run")
	check := exec.CommandContext(ctx, bin, "--check", "--db", db,
		"--socket", filepath.Join(other, "umbral", "umbral.sock"))
	check.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+other)
	out, err := check.CombinedOutput()

	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != exitTempFail {
		t.Errorf("a second runtime on the same database ended with %v, want exit %d; output:\n%s", err, exitTempFail, out)
	}
	if got := aliveSessions(t, db); got == 0 {
		t.Error("the second daemon recovered over the first one's live sessions: none is alive any more")
	}
}

func aliveSessions(t *testing.T, path string) int {
	t.Helper()
	db, err := store.Open(t.Context(), store.Options{Path: path})
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.DB().QueryRowContext(t.Context(), "SELECT count(*) FROM sessions WHERE state = 'alive'").Scan(&n); err != nil {
		t.Fatalf("count alive sessions: %v", err)
	}
	return n
}
