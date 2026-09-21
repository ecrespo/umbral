package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/client"
)

// buildDaemon compiles umbrald into a temporary directory and returns its path.
func buildDaemon(t *testing.T) string {
	t.Helper()

	bin := filepath.Join(t.TempDir(), "umbrald")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "./cmd/umbrald")
	build.Dir = "../.."
	build.Env = append(os.Environ(), "PKG_CONFIG_PATH="+os.Getenv("PKG_CONFIG_PATH"))
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build umbrald: %v\n%s", err, out)
	}
	return bin
}

// TestNoGapBetweenSnapshotAndStream_REQ_API_002 walks the bootstrap API Spec §5.3 documents,
// against a real daemon, while a second client is changing the tree underneath.
//
// The property is the one a client cannot check for itself: a tree rebuilt from a snapshot
// plus the events above its `seq` equals the daemon's. It is tested here rather than beside
// the module because it is a statement about the *protocol* — the counter, the read order
// and the discard rule together — and nothing below `cmd` is allowed to wire the daemon.
//
// The load matters. With one quiet client the snapshot and the stream cannot disagree; the
// failure this guards against is an event that lands between the counter being read and the
// tree being read, which needs something making changes while the snapshot is taken.
func TestNoGapBetweenSnapshotAndStream_REQ_API_002(t *testing.T) {
	bin := buildDaemon(t)

	dir := t.TempDir()
	runtime := filepath.Join(dir, "run")
	if err := os.MkdirAll(runtime, 0o700); err != nil {
		t.Fatal(err)
	}
	// Every directory the daemon writes to, not only the socket's.
	//
	// Overriding `XDG_RUNTIME_DIR` alone moved the socket and left the *database* where the
	// developer's own is: an autostarted daemon inherits `XDG_DATA_HOME`, so this test was
	// creating its workspaces, tabs, panes and sessions in `~/.local/share/umbral/umbral.db`
	// and holding it open with a WAL. A test may not write to the data of the machine it
	// runs on, and a CI runner hid it because its home is thrown away.
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	// The daemon is started here rather than by the client's autostart, so that this test
	// owns the process and can stop it. `umbrald` outlives its clients by design
	// (REQ-TERM-003), so closing both connections leaves it running: before this, every
	// run of `task ci` left one more daemon on the machine, forever, each holding that
	// database open.
	startDaemon(t, bin, runtime)

	// The observer bootstraps; the writer changes the tree. Two connections, because a
	// client never sees its own calls as notifications and the interesting case is the
	// events someone else caused.
	observerConn, err := client.Connect(ctx, client.Options{DaemonPath: bin, ClientKind: client.ClientKindTUI})
	if err != nil {
		t.Fatalf("connect observer: %v", err)
	}
	observer := client.NewStream(observerConn)
	defer func() { _ = observer.Close() }()

	writer, err := client.Connect(ctx, client.Options{DaemonPath: bin, ClientKind: client.ClientKindTUI})
	if err != nil {
		t.Fatalf("connect writer: %v", err)
	}
	defer func() { _ = writer.Close() }()

	// Step 1 of §5.3: the observer is already listening before it asks for anything, so
	// nothing that happens from here on can slip between the two.
	var (
		mu       sync.Mutex
		buffered []client.Notification
		seqs     []uint64
	)
	collected := make(chan struct{})
	go func() {
		defer close(collected)
		for n := range observer.Notifications() {
			mu.Lock()
			buffered = append(buffered, n)
			seqs = append(seqs, n.Seq)
			mu.Unlock()
		}
	}()

	// Some tree to start from, and then a steady stream of changes while the snapshot is
	// taken.
	first := createWorkspace(ctx, t, writer, dir)
	churn, stopChurn := context.WithCancel(ctx)
	churned := make(chan int, 1)
	go func() {
		n := 0
		for churn.Err() == nil {
			if err := writer.Call(churn, "tab.create", map[string]any{
				"workspace_id": first, "label": "churn",
			}, nil); err != nil {
				break
			}
			n++
		}
		churned <- n
	}()

	// Step 2 and 3: snapshot, then apply only what is above its seq.
	time.Sleep(150 * time.Millisecond)
	var snapshot struct {
		Seq        uint64 `json:"seq"`
		Workspaces []struct {
			ID string `json:"id"`
		} `json:"workspaces"`
		Tabs []struct {
			ID string `json:"id"`
		} `json:"tabs"`
	}
	if err := observer.Call(ctx, "session.snapshot", nil, &snapshot); err != nil {
		t.Fatalf("session.snapshot: %v", err)
	}
	stopChurn()
	created := <-churned
	if created == 0 {
		t.Fatal("the writer created no tabs, so nothing was happening during the snapshot")
	}
	t.Logf("snapshot at seq %d, %d tabs in it, %d tabs created during the run",
		snapshot.Seq, len(snapshot.Tabs), created)

	// Let the last notifications land.
	time.Sleep(300 * time.Millisecond)

	// Rebuild the way §5.3 says: the snapshot's records, plus every `tab.created` above its
	// seq. Anything at or below is already contained and is discarded.
	tabs := map[string]bool{}
	for _, tab := range snapshot.Tabs {
		tabs[tab.ID] = true
	}
	// One lock for both copies: `seqs` is indexed against `replay` below, and taking them
	// separately would let a notification land in between and turn a failure into a panic.
	mu.Lock()
	replay := append([]client.Notification(nil), buffered...)
	replaySeqs := append([]uint64(nil), seqs...)
	mu.Unlock()
	for _, n := range replay {
		if n.Method != "tab.created" || n.Seq <= snapshot.Seq {
			continue
		}
		var tab struct {
			ID string `json:"id"`
		}
		if err := n.Decode(&tab); err != nil {
			t.Fatalf("decode tab.created: %v", err)
		}
		tabs[tab.ID] = true
	}

	// The daemon's own answer, taken after everything settled.
	var settled struct {
		Tabs []struct {
			ID string `json:"id"`
		} `json:"tabs"`
	}
	if err := observer.Call(ctx, "session.snapshot", nil, &settled); err != nil {
		t.Fatalf("second session.snapshot: %v", err)
	}

	for _, tab := range settled.Tabs {
		if !tabs[tab.ID] {
			t.Errorf("tab %q exists in the daemon and the client never learned of it: it was "+
				"neither in the snapshot nor above its seq", tab.ID)
		}
	}
	if len(tabs) < len(settled.Tabs) {
		t.Errorf("the client reconstructed %d tabs, the daemon has %d", len(tabs), len(settled.Tabs))
	}

	// Every notification carried a number.
	//
	// Their order is deliberately not asserted. This observer never subscribes to a session,
	// so everything it receives took the direct write path and does arrive in assignment
	// order — but that is a property of this connection, not of the protocol: decision 5 of
	// the `2026-09-notification-sequencing` delta says the counter is monotonic in assignment
	// only, because output waits in the queue of §8 and control notifications do not.
	// Asserting ordering here would pass for a reason the daemon does not promise, and would
	// make the test a trap for anyone who later gave the observer a subscription.
	if len(replaySeqs) == 0 {
		t.Fatal("the observer received no notifications at all")
	}
	for i, seq := range replaySeqs {
		if seq == 0 {
			t.Errorf("notification %d (%s) carried seq 0", i, replay[i].Method)
		}
	}
}

// createWorkspace makes a workspace and returns its id.
func createWorkspace(ctx context.Context, t *testing.T, c *client.Client, cwd string) string {
	t.Helper()

	var out struct {
		Workspace struct {
			ID string `json:"id"`
		} `json:"workspace"`
	}
	if err := c.Call(ctx, "workspace.create", map[string]any{"cwd": cwd, "label": "bootstrap"}, &out); err != nil {
		t.Fatalf("workspace.create: %v", err)
	}
	return out.Workspace.ID
}

// startDaemon runs `umbrald` for the test and stops it when the test ends.
//
// It exists because the daemon is built to survive its clients (REQ-TERM-003): a test that
// autostarts one and closes its connections has not stopped anything, and there is no
// `system.shutdown` to ask politely with. The process is therefore owned here — signalled on
// cleanup, and killed if it does not go — so the suite leaves nothing behind.
func startDaemon(t *testing.T, bin, runtime string) {
	t.Helper()

	// Tied to the test's context, which Go cancels just before the cleanups run, and
	// cancelled with SIGINT rather than SIGKILL so the daemon closes its database instead
	// of leaving a hot WAL behind. `WaitDelay` is the promise that it goes either way.
	cmd := exec.CommandContext(t.Context(), bin)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 10 * time.Second
	cmd.Env = os.Environ()
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start umbrald: %v", err)
	}
	// Reaped here: cancelling the context terminates the process but does not wait for it,
	// and an unwaited child stays as a zombie for the life of the test binary.
	t.Cleanup(func() { _ = cmd.Wait() })

	// The socket appears a moment after the process does, and connecting before it exists
	// would send the client down its autostart path — starting the second daemon this
	// function exists to avoid.
	socket := filepath.Join(runtime, "umbral", "umbral.sock")
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("umbrald did not open %s", socket)
}
