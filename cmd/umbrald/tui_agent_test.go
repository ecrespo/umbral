package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ecrespo/umbral/internal/tui"
	daemonadapter "github.com/ecrespo/umbral/internal/tui/adapters/daemon"
	"github.com/ecrespo/umbral/internal/tui/adapters/ghosttyvt"
	"github.com/ecrespo/umbral/internal/tui/ports"
)

// teaRunner is Bubble Tea's loop without a terminal: commands run in goroutines, their
// messages come back to one goroutine — the test's — which is the only one that touches the
// model, as in the real program.
type teaRunner struct {
	t    *testing.T
	m    *tui.Model
	msgs chan tea.Msg
}

func (r *teaRunner) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				r.run(c)
			}
			return
		}
		if msg != nil {
			r.msgs <- msg
		}
	}()
}

func (r *teaRunner) send(msg tea.Msg) {
	_, cmd := r.m.Update(msg)
	r.run(cmd)
}

// until feeds messages to the model until cond holds.
func (r *teaRunner) until(what string, cond func() bool) {
	r.t.Helper()
	deadline := time.After(30 * time.Second)
	for !cond() {
		select {
		case msg := <-r.msgs:
			r.send(msg)
		case <-deadline:
			r.t.Fatalf("never: %s; status %q\n%s", what, r.m.Status(), r.m.View().Content)
		}
	}
}

// TestTheAgentPanelAgainstARealDaemon_REQ_TUI_001: the TUI's own model and adapter, against
// a real daemon: ctrl+space and a message start a thread in the pane's directory, the turn's
// write_file pauses on an approval — with the `@file:` the message named attached to what
// the model read, and approval.list listing it — the panel shows it with its diff, ctrl+y
// approves it, the file is written and the answer drawn, and a cancel afterwards stops
// nothing.
func TestTheAgentPanelAgainstARealDaemon_REQ_TUI_001(t *testing.T) {
	ollama := &scriptedOllama{replies: [][]string{
		{writeFileCall("from-the-tui.txt")},
		{`{"model":"m","message":{"role":"assistant","content":"Written from the panel."},"done":true,"done_reason":"stop","prompt_eval_count":20,"eval_count":2}`},
	}}
	srv := ollama.server(t)
	// The TUI's session starts in the daemon's default directory, $HOME, and the thread works
	// there: a temporary one, given to the daemon alone, so the agent never writes into the
	// developer's and `go build` keeps its cache.
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "notes.txt"), "secret-sauce-77\n")
	_, ctx := agentDaemon(t, srv.URL, "HOME="+home)

	adapter := daemonadapter.New(secondStream(t, ctx))
	r := &teaRunner{t: t, m: tui.New(adapter, ghosttyvt.New), msgs: make(chan tea.Msg, 256)}
	r.run(r.m.Init())
	r.send(tea.WindowSizeMsg{Width: 140, Height: 40})
	r.until("a session is shown", func() bool { return !strings.Contains(r.m.View().Content, "starting a session") })

	r.send(tea.KeyPressMsg{Code: tea.KeySpace, Mod: tea.ModCtrl})
	for _, ch := range "write the file per @file:notes.txt" {
		if ch == ' ' {
			r.send(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
			continue
		}
		r.send(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}
	r.send(tea.KeyPressMsg{Code: tea.KeyEnter})

	r.until("the approval is pending", func() bool { return len(r.m.PendingApprovals()) == 1 })
	apr := r.m.PendingApprovals()[0]
	if apr.Tool != "write_file" || !strings.Contains(apr.Diff, "+x") {
		t.Fatalf("approval %+v", apr)
	}
	target := filepath.Join(home, "from-the-tui.txt")
	if _, err := os.Stat(target); err == nil {
		t.Fatal("the file exists before the approval")
	}
	ollama.mu.Lock()
	first := ollama.bodies[0]
	ollama.mu.Unlock()
	if !strings.Contains(first, "secret-sauce-77") {
		t.Fatal("the @file: attachment never reached the model")
	}
	// approval.list, which the TUI reads at start, lists it too.
	listed, err := adapter.Approvals(ctx)
	if err != nil || len(listed) != 1 || listed[0].ID != apr.ID {
		t.Fatalf("approval.list = %+v, %v", listed, err)
	}
	r.send(tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	if view := r.m.View().Content; !strings.Contains(view, "+x") {
		t.Fatalf("the diff is not shown:\n%s", view)
	}
	r.send(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})

	r.until("the turn ends", func() bool { return !r.m.AgentRunning() && len(r.m.PendingApprovals()) == 0 })
	if view := r.m.View().Content; !strings.Contains(view, "Written from the panel.") || !strings.Contains(view, "end_turn") {
		t.Fatalf("the panel after the turn:\n%s", view)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "x\n" {
		t.Fatalf("%s = %q, %v", target, data, err)
	}
	// The approval is decided: answering it again is ErrApprovalGone, not a failure to retry.
	if err := adapter.Respond(ctx, apr.ID, "approve", "once"); !errors.Is(err, ports.ErrApprovalGone) {
		t.Fatalf("a second answer: %v", err)
	}
	// With the turn over, a cancel stops nothing, and the adapter says so.
	if stopped, err := adapter.Cancel(ctx, apr.ThreadID); err != nil || stopped {
		t.Fatalf("a cancel with no turn running: stopped %v, %v", stopped, err)
	}
}

// TestTheThreadListAgainstARealDaemon_REQ_ORC_003: the TUI adapter against a real daemon. A
// thread it creates in a workspace belongs to it and one created with none belongs to none,
// and the listing reads the focused workspace and each thread's fields by their wire names.
func TestTheThreadListAgainstARealDaemon_REQ_ORC_003(t *testing.T) {
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
	adapter := daemonadapter.New(secondStream(t, ctx))
	inWs, err := adapter.CreateThread(ctx, home, ws.Workspace.ID)
	if err != nil {
		t.Fatalf("thread.create in %s: %v", ws.Workspace.ID, err)
	}
	inNone, err := adapter.CreateThread(ctx, home, "")
	if err != nil {
		t.Fatalf("thread.create in none: %v", err)
	}

	list, err := adapter.Threads(ctx)
	if err != nil {
		t.Fatalf("Threads: %v", err)
	}
	if list.WorkspaceID != ws.Workspace.ID || list.Seq == 0 {
		t.Fatalf("focused %q at seq %d, want %q after the workspace's events", list.WorkspaceID, list.Seq, ws.Workspace.ID)
	}
	found := map[string]ports.Thread{}
	for _, th := range list.Threads {
		found[th.ID] = th
	}
	if got := found[inWs]; got.WorkspaceID != ws.Workspace.ID || got.Attention == "" || got.State != "idle" {
		t.Errorf("the workspace's thread read %+v", got)
	}
	if got, ok := found[inNone]; !ok || got.WorkspaceID != "" || got.Attention == "" {
		t.Errorf("the thread of no workspace read %+v (listed %v)", got, ok)
	}
}
