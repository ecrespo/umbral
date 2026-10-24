//go:build live

package main

import (
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/acp/adapters/sdk/acptest"
)

// F2a's three budgets (PRD F2 §7), measured for T-F2-12's report against a real daemon. They
// need no model — the daemon's model is the scripted one, the agent acptest's — but they are
// measurements rather than gates: the 1 GiB repository alone takes about 20 s to build, so they
// run with `-tags live` beside US-F2-001.

// p95 is the 95th percentile of ds, nearest rank.
func p95(ds []time.Duration) time.Duration {
	s := slices.Clone(ds)
	slices.Sort(s)
	i := (len(s)*95+99)/100 - 1
	return s[max(i, 0)]
}

// TestLiveWorktreeCreateOn1GiB_NFR_F2_01: `worktree.create` on a 1 GiB repository — 4096
// committed files of 256 KiB of random bytes — 20 times, each discarded before the next; the
// p95 is under 2 s.
func TestLiveWorktreeCreateOn1GiB_NFR_F2_01(t *testing.T) {
	repo := filepath.Join(socketDir(t), "big")
	built := time.Now()
	for _, args := range [][]string{{"init", "-q", "-b", "main", repo}, {"-C", repo, "config", "core.compression", "0"}} {
		if out, err := exec.CommandContext(t.Context(), "git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	buf := make([]byte, 256<<10)
	for d := range 64 {
		dir := filepath.Join(repo, fmt.Sprintf("d%02d", d))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for f := range 64 {
			_, _ = rand.Read(buf)
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%02d.bin", f)), buf, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=nfr", "-c", "user.email=nfr@example.invalid", "commit", "-q", "-m", "1 GiB"}} {
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	size, _ := exec.CommandContext(t.Context(), "du", "-sh", filepath.Join(repo, ".git")).Output()
	t.Logf("repository built in %s; .git is %s", time.Since(built).Round(time.Second), strings.Fields(string(size))[0])

	stream, ctx, _ := startAgentDaemon(t, (&scriptedOllama{}).server(t).URL, daemonOpts{})
	took := make([]time.Duration, 0, 20)
	for i := range 20 {
		var th struct{ ID string }
		if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": repo}, &th); err != nil {
			t.Fatal(err)
		}
		var wt struct{ ID string }
		start := time.Now()
		if err := stream.Call(ctx, "worktree.create", map[string]any{"thread_id": th.ID}, &wt); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		took = append(took, time.Since(start))
		if err := stream.Call(ctx, "worktree.discard", map[string]any{"worktree_id": wt.ID, "confirm": true}, nil); err != nil {
			t.Fatalf("discard %d: %v", i, err)
		}
	}
	t.Logf("worktree.create on 1 GiB: p95 %s, min %s, max %s over %d", p95(took), slices.Min(took), slices.Max(took), len(took))
	if p95(took) >= 2*time.Second {
		t.Errorf("p95 %s, want under NFR-F2-01's 2 s", p95(took))
	}
}

// daemonRSS is the resident set, in KiB, of the daemon this test started: the umbrald whose
// XDG_RUNTIME_DIR is the test's.
func daemonRSS(t *testing.T) int64 {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Skipf("no /proc: %v", err)
	}
	want := "XDG_RUNTIME_DIR=" + os.Getenv("XDG_RUNTIME_DIR")
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		if exe, err := os.Readlink(filepath.Join("/proc", e.Name(), "exe")); err != nil || filepath.Base(exe) != "umbrald" {
			continue
		}
		env, err := os.ReadFile(filepath.Join("/proc", e.Name(), "environ"))
		if err != nil || !slices.Contains(strings.Split(string(env), "\x00"), want) {
			continue
		}
		status, err := os.ReadFile(filepath.Join("/proc", e.Name(), "status"))
		if err != nil {
			t.Fatal(err)
		}
		for line := range strings.Lines(string(status)) {
			if v, ok := strings.CutPrefix(line, "VmRSS:"); ok {
				kib, _ := strconv.ParseInt(strings.Fields(v)[0], 10, 64)
				return kib
			}
		}
	}
	t.Fatal("the test's daemon is not running")
	return 0
}

// TestLiveIdleThreadOverhead_NFR_F2_02: ten more threads, each with a worktree and one turn
// behind it, left idle, cost the daemon under 15 MiB of resident memory each.
func TestLiveIdleThreadOverhead_NFR_F2_02(t *testing.T) {
	const threads = 10
	reply := []string{`{"model":"m","message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":1}`}
	ollama := &scriptedOllama{}
	for range threads + 1 {
		ollama.replies = append(ollama.replies, reply)
	}
	repo := usf2Repo(t)
	stream, ctx, _ := startAgentDaemon(t, ollama.server(t).URL, daemonOpts{})

	turn := func() {
		var th struct{ ID string }
		if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": repo, "worktree": true}, &th); err != nil {
			t.Fatal(err)
		}
		sendAndFinish(t, ctx, stream, th.ID, "say ok")
	}
	// One thread first, so what the first turn loads once is in the baseline.
	turn()
	time.Sleep(5 * time.Second)
	before := daemonRSS(t)
	for range threads {
		turn()
	}
	time.Sleep(5 * time.Second)
	after := daemonRSS(t)
	per := float64(after-before) / threads / 1024
	t.Logf("daemon VmRSS %d KiB with 1 idle thread, %d KiB with %d: %.2f MiB per idle thread", before, after, threads+1, per)
	if per >= 15 {
		t.Errorf("%.2f MiB per idle thread, want under NFR-F2-02's 15 MiB", per)
	}
}

// TestLiveACPMediationLatency_NFR_F2_03: the agent's `fs/read_text_file` inside the write root,
// which the policy allows, answered by the daemon — decided, recorded, read and its row
// finished — in under 20 ms at the p95, as the agent measures it, over 500 requests.
func TestLiveACPMediationLatency_NFR_F2_03(t *testing.T) {
	stream, ctx := acpDaemon(t)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "x"), "x\n")
	var th acpThread
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": dir, "agent": "fake"}, &th); err != nil {
		t.Fatal(err)
	}
	sendAndFinish(t, ctx, stream, th.ID, fmt.Sprintf("%s 500 %s", acptest.Time, filepath.Join(dir, "x")))
	answer := lastAnswer(t, ctx, stream, th.ID)
	var p50, p95us, maxus int64
	if _, err := fmt.Sscanf(answer, "p50=%dus p95=%dus max=%dus", &p50, &p95us, &maxus); err != nil {
		t.Fatalf("the agent answered %q: %v", answer, err)
	}
	t.Logf("ACP mediation, fs/read_text_file allowed: p50 %d µs, p95 %d µs, max %d µs over 500", p50, p95us, maxus)
	if p95us >= 20_000 {
		t.Errorf("p95 %d µs, want under NFR-F2-03's 20 ms", p95us)
	}
}
