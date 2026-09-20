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
	t.Setenv("XDG_RUNTIME_DIR", runtime)

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

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
	mu.Lock()
	replay := append([]client.Notification(nil), buffered...)
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

	// Every notification carried a number, and they only went up on this connection.
	mu.Lock()
	defer mu.Unlock()
	if len(seqs) == 0 {
		t.Fatal("the observer received no notifications at all")
	}
	for i, seq := range seqs {
		if seq == 0 {
			t.Errorf("notification %d (%s) carried seq 0", i, replay[i].Method)
		}
		if i > 0 && seq <= seqs[i-1] {
			t.Errorf("seq went %d then %d on one connection", seqs[i-1], seq)
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
