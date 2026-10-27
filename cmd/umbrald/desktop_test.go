package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/client"
	"github.com/ecrespo/umbral/internal/desktop"
	"github.com/ecrespo/umbral/internal/desktop/adapters/vtframe"
)

// recordingEmitter keeps what the bridge told the page.
type recordingEmitter struct {
	mu     sync.Mutex
	events []recorded
	ch     chan recorded
}

type recorded struct {
	name string
	data any
}

func (r *recordingEmitter) Emit(name string, data any) {
	r.mu.Lock()
	r.events = append(r.events, recorded{name, data})
	r.mu.Unlock()
	select {
	case r.ch <- recorded{name, data}:
	default:
	}
}

// frameText is a frame's rows as plain text.
func frameText(f desktop.Frame) string {
	var b strings.Builder
	for _, row := range f.Frame.Rows {
		var line strings.Builder
		for _, run := range row {
			line.WriteString(run.Text)
		}
		b.WriteString(strings.TrimRight(line.String(), " "))
		b.WriteString("\n")
	}
	return b.String()
}

// TestTheDesktopBridgeDrivesADaemon_REQ_DSK_001: the desktop client's core, on a `desktop`
// connection to a real daemon, creates a workspace, attaches to its pane's terminal, types a
// command and draws its output from the daemon's stream; lists threads, approvals, blocks and
// worktrees; passes the daemon's notifications on; refuses a method outside its list without
// sending it, and a size the daemon does not take; and every method it may call is one the published protocol serves to `desktop`
// (delta `2026-10-desktop-client`).
func TestTheDesktopBridgeDrivesADaemon_REQ_DSK_001(t *testing.T) {
	bin := buildDaemon(t)
	rt, daemonDir := isolatedRuntime(t)
	startDaemon(t, bin, rt)
	c, err := client.Connect(t.Context(), client.Options{
		SocketPath: filepath.Join(daemonDir, "umbral.sock"), NoAutostart: true, ClientKind: client.ClientKindDesktop,
	})
	if err != nil {
		t.Fatal(err)
	}
	stream := client.NewUnboundedStream(c)
	emit := &recordingEmitter{ch: make(chan recorded, 4096)}
	b := desktop.New(stream, emit, vtframe.New)
	t.Cleanup(func() { _ = b.Close() })

	// Only the published protocol: every method the page may call is served to `desktop`.
	var schema struct {
		Schema struct {
			Methods []struct {
				Name        string   `json:"name"`
				ClientKinds []string `json:"client_kinds"`
				Served      bool     `json:"served"`
			} `json:"methods"`
		} `json:"schema"`
	}
	if err := stream.Call(t.Context(), "api.schema", map[string]any{}, &schema); err != nil {
		t.Fatal(err)
	}
	served := map[string]bool{}
	for _, m := range schema.Schema.Methods {
		served[m.Name] = m.Served && slices.Contains(m.ClientKinds, client.ClientKindDesktop)
	}
	for _, m := range append(slices.Clone(desktop.Methods), "session.subscribe", "session.unsubscribe", "session.input", "session.resize") {
		if !served[m] {
			t.Errorf("the bridge calls %s, which the protocol does not serve to desktop", m)
		}
	}

	if _, err := b.Call("rules.reset", nil); !errors.Is(err, desktop.ErrNotAllowed) {
		t.Errorf("rules.reset through the bridge: %v, want ErrNotAllowed", err)
	}

	raw, err := b.Call("workspace.create", json.RawMessage(`{"cwd":"`+t.TempDir()+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	var ws struct {
		Workspace struct{ ID string } `json:"workspace"`
		RootPane  struct {
			ID        string `json:"id"`
			SessionID string `json:"session_id"`
		} `json:"root_pane"`
	}
	if err := json.Unmarshal(raw, &ws); err != nil || ws.RootPane.SessionID == "" {
		t.Fatalf("workspace.create answered %s: %v", raw, err)
	}
	if err := b.Attach(ws.RootPane.SessionID, 90, 20); err != nil {
		t.Fatal(err)
	}
	if err := b.Input(ws.RootPane.SessionID, "echo desk-$((40+2))\r"); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(30 * time.Second)
	sawTree := false
	for drawn := false; !drawn || !sawTree; {
		select {
		case ev := <-emit.ch:
			switch ev.name {
			case desktop.EventFrame:
				f := ev.data.(desktop.Frame)
				if f.SessionID == ws.RootPane.SessionID && len(f.Frame.Rows) == 20 && f.Frame.Cols == 90 &&
					strings.Contains(frameText(f), "\ndesk-42\n") {
					drawn = true
				}
			case desktop.EventDaemon:
				if n := ev.data.(desktop.Notification); strings.HasPrefix(n.Method, "workspace.") || strings.HasPrefix(n.Method, "pane.") {
					sawTree = true
				}
			}
		case <-deadline:
			t.Fatalf("no frame showing desk-42, or no tree notification (%v)", sawTree)
		}
	}

	for _, call := range []struct{ method, params string }{
		{"session.snapshot", ""},
		{"approval.list", ""},
		{"worktree.list", ""},
		{"block.list", `{"session_id":"` + ws.RootPane.SessionID + `"}`},
	} {
		if _, err := b.Call(call.method, json.RawMessage(call.params)); err != nil {
			t.Errorf("%s: %v", call.method, err)
		}
	}
	if rows, err := b.Threads(ws.Workspace.ID); err != nil || len(rows) != 0 {
		t.Errorf("a new workspace's threads: %+v %v", rows, err)
	}
	if err := b.Resize(ws.RootPane.SessionID, 70, 15); err != nil {
		t.Fatal(err)
	}
	if err := b.Resize(ws.RootPane.SessionID, 5000, 15); err == nil {
		t.Error("a 5000-column terminal was accepted")
	}

	_ = b.Close()
	for {
		select {
		case ev := <-emit.ch:
			if ev.name == desktop.EventDisconnected {
				return
			}
		case <-time.After(10 * time.Second):
			t.Fatal("closing the connection did not tell the page")
		}
	}
}

// TestTheDesktopThreadsViewAgainstARealDaemon_REQ_ORC_003: the desktop client's threads view,
// read over a `desktop` connection from a real daemon, lists the thread created in the
// workspace and not the one created in none, by the wire names of `session.snapshot` and
// `approval.list`.
func TestTheDesktopThreadsViewAgainstARealDaemon_REQ_ORC_003(t *testing.T) {
	ollama := &scriptedOllama{}
	srv := ollama.server(t)
	home := t.TempDir()
	stream, ctx := agentDaemon(t, srv.URL, "HOME="+home)

	var ws struct {
		Workspace struct {
			ID string `json:"id"`
		} `json:"workspace"`
	}
	if err := stream.Call(ctx, "workspace.create", map[string]any{"cwd": home}, &ws); err != nil {
		t.Fatalf("workspace.create: %v", err)
	}
	var inWs, inNone struct {
		ID string `json:"id"`
	}
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": home, "workspace": ws.Workspace.ID}, &inWs); err != nil {
		t.Fatalf("thread.create in %s: %v", ws.Workspace.ID, err)
	}
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": home}, &inNone); err != nil {
		t.Fatalf("thread.create in none: %v", err)
	}

	c, err := client.Connect(ctx, client.Options{
		SocketPath:  filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "umbral", "umbral.sock"),
		NoAutostart: true, ClientKind: client.ClientKindDesktop,
	})
	if err != nil {
		t.Fatal(err)
	}
	b := desktop.New(client.NewUnboundedStream(c), &recordingEmitter{ch: make(chan recorded, 64)}, vtframe.New)
	t.Cleanup(func() { _ = b.Close() })

	rows, err := b.Threads(ws.Workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != inWs.ID || rows[0].State == "" || rows[0].Attention == "" || rows[0].Mode == "" {
		t.Fatalf("the workspace's threads %+v, want %s alone with its state, attention and mode (not %s)", rows, inWs.ID, inNone.ID)
	}
}
