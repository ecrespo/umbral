//go:build live

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/client"
	"github.com/ecrespo/umbral/internal/mcp/adapters/sdk/sdktest"
)

// The tests in this file drive a real daemon against a real Ollama (`go test -tags live`).
// They are the scripted-Ollama tests beside them with the script replaced by a model, so what
// they prove is the part a script cannot: that a real model's tool calls, as Ollama encodes
// them, travel the whole path. UMBRAL_OLLAMA_URL overrides the endpoint and UMBRAL_LIVE_MODEL
// the model, gpt-oss:20b by default. A model's answer is not deterministic, so each assertion
// is on what the daemon did, and on the answer only where the tool's output forces it.

// liveTurn is what one turn produced, as the notifications reported it.
type liveTurn struct {
	text       strings.Builder
	tools      []string // tool:status, in order
	approvals  []string // the tools that asked
	stopReason string
	inTokens   int64
}

// liveDaemon starts a daemon whose code and fast classes are the live model, connects a
// streaming TUI client, and waits for the model to be discovered and healthy.
func liveDaemon(t *testing.T) (*client.Stream, context.Context, string) {
	t.Helper()
	url := os.Getenv("UMBRAL_OLLAMA_URL")
	if url == "" {
		url = "http://127.0.0.1:11434"
	}
	model := os.Getenv("UMBRAL_LIVE_MODEL")
	if model == "" {
		model = "gpt-oss:20b"
	}
	requireModel(t, url, model)
	bin := buildDaemon(t)
	rt, daemonDir := isolatedRuntime(t)
	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "umbral")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(configDir, "models.toml"), fmt.Sprintf(`
[classes]
code = ["ollama/%[1]s"]
fast = ["ollama/%[1]s"]

[[providers]]
id = "ollama"
type = "ollama"
base_url = %[2]q
[providers.options]
num_ctx = 16384
keep_alive = "10m"
`, model, url))
	startDaemon(t, bin, rt)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	t.Cleanup(cancel)
	c, err := client.Connect(ctx, client.Options{
		SocketPath: filepath.Join(daemonDir, "umbral.sock"), NoAutostart: true, ClientKind: client.ClientKindTUI,
	})
	if err != nil {
		t.Fatal(err)
	}
	stream := client.NewStream(c)
	t.Cleanup(func() { _ = stream.Close() })

	want := "ollama/" + model
	for {
		var list struct{ Items []struct{ ID, Health string } }
		if err := stream.Call(ctx, "model.list", nil, &list); err != nil {
			t.Fatal(err)
		}
		for _, m := range list.Items {
			if m.ID == want && m.Health == "ok" {
				return stream, ctx, want
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s was never discovered: %+v", want, list)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// requireModel skips the test when Ollama is unreachable or has not pulled the model, rather
// than letting discovery poll until the deadline.
func requireModel(t *testing.T, url, model string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url+"/api/tags", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Skipf("Ollama is not reachable at %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var tags struct{ Models []struct{ Name string } }
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		t.Skipf("Ollama at %s answered /api/tags with something else: %v", url, err)
	}
	for _, m := range tags.Models {
		if m.Name == model {
			return
		}
	}
	t.Skipf("%s is not pulled at %s", model, url)
}

// liveThread creates a thread in cwd and sends text to it.
func liveThread(t *testing.T, stream *client.Stream, ctx context.Context, cwd, text string) string {
	t.Helper()
	var thread struct{ ID string }
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": cwd}, &thread); err != nil {
		t.Fatal(err)
	}
	if err := stream.Call(ctx, "thread.send", map[string]any{"thread_id": thread.ID, "text": text}, nil); err != nil {
		t.Fatal(err)
	}
	return thread.ID
}

// awaitTurn reads notifications until the turn finishes. Every approval is handed to decide,
// which answers it; a nil decide approves once.
func awaitTurn(t *testing.T, stream *client.Stream, ctx context.Context, decide func(tool string) string) *liveTurn {
	t.Helper()
	turn := &liveTurn{}
	for {
		select {
		case n, ok := <-stream.Notifications():
			if !ok {
				t.Fatalf("the stream ended: %v", stream.Err())
			}
			switch n.Method {
			case "thread.delta":
				var d struct{ Kind, Text string }
				_ = n.Decode(&d)
				if d.Kind == "text" {
					turn.text.WriteString(d.Text)
				}
			case "thread.tool_call":
				var tc struct{ Tool, Status string }
				_ = n.Decode(&tc)
				turn.tools = append(turn.tools, tc.Tool+":"+tc.Status)
			case "approval.requested":
				var a struct{ ID, Tool string }
				_ = n.Decode(&a)
				turn.approvals = append(turn.approvals, a.Tool)
				decision := "approve"
				if decide != nil {
					decision = decide(a.Tool)
				}
				if err := stream.Call(ctx, "approval.respond", map[string]any{"approval_id": a.ID, "decision": decision, "scope": "once"}, nil); err != nil {
					t.Fatal(err)
				}
			case "thread.turn_finished":
				var end struct {
					StopReason string `json:"stop_reason"`
					Usage      struct {
						InTokens int64 `json:"in_tokens"`
					} `json:"usage"`
				}
				_ = n.Decode(&end)
				turn.stopReason, turn.inTokens = end.StopReason, end.Usage.InTokens
				t.Logf("tools %v, approvals %v, stop %s, in %d tokens, answer %q",
					turn.tools, turn.approvals, turn.stopReason, turn.inTokens, turn.text.String())
				return turn
			}
		case <-ctx.Done():
			t.Fatal("the turn did not finish")
		}
	}
}

// TestLiveReadFile_REQ_AGT_001: the model reads a file through read_file and answers with a
// word only that file holds.
func TestLiveReadFile_REQ_AGT_001(t *testing.T) {
	stream, ctx, _ := liveDaemon(t)
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, "notes.txt"), "The codeword is zebra-4721.\n")

	liveThread(t, stream, ctx, cwd, "Use the read_file tool to read notes.txt, then reply with the codeword it contains and nothing else.")
	turn := awaitTurn(t, stream, ctx, nil)

	if turn.stopReason != "end_turn" || turn.inTokens == 0 {
		t.Fatalf("turn ended %s with %d input tokens", turn.stopReason, turn.inTokens)
	}
	if !contains(turn.tools, "read_file:ok") {
		t.Fatalf("read_file never ran: %v", turn.tools)
	}
	if !strings.Contains(turn.text.String(), "zebra-4721") {
		t.Fatalf("the answer %q lacks the codeword", turn.text.String())
	}
}

// TestLiveWriteFileAsks_REQ_AGT_004: write_file asks in normal mode; nothing is written until
// the approval, and the approved content lands.
func TestLiveWriteFileAsks_REQ_AGT_004(t *testing.T) {
	stream, ctx, _ := liveDaemon(t)
	cwd := t.TempDir()
	out := filepath.Join(cwd, "hello.txt")

	liveThread(t, stream, ctx, cwd, "Use the write_file tool to create hello.txt containing exactly the text: hola umbral")
	turn := awaitTurn(t, stream, ctx, func(tool string) string {
		if tool == "write_file" {
			if _, err := os.Stat(out); err == nil {
				t.Error("hello.txt existed before the approval")
			}
		}
		return "approve"
	})

	if turn.stopReason != "end_turn" || !contains(turn.approvals, "write_file") || !contains(turn.tools, "write_file:ok") {
		t.Fatalf("turn %s, approvals %v, tools %v", turn.stopReason, turn.approvals, turn.tools)
	}
	if got := readFile(t, out); !strings.Contains(got, "hola umbral") {
		t.Fatalf("hello.txt = %q", got)
	}
}

// TestLiveDeniedWriteLeavesNoFile_REQ_AGT_005: a denied write_file is not run, and the turn
// goes on to an answer.
func TestLiveDeniedWriteLeavesNoFile_REQ_AGT_005(t *testing.T) {
	stream, ctx, _ := liveDaemon(t)
	cwd := t.TempDir()

	liveThread(t, stream, ctx, cwd, "Use the write_file tool to create denied.txt containing: x. If the user denies it, say so and stop; do not retry.")
	turn := awaitTurn(t, stream, ctx, func(string) string { return "deny" })

	if !contains(turn.tools, "write_file:denied_by_user") || turn.stopReason != "end_turn" {
		t.Fatalf("turn %s, tools %v", turn.stopReason, turn.tools)
	}
	if _, err := os.Stat(filepath.Join(cwd, "denied.txt")); err == nil {
		t.Fatal("denied.txt was written")
	}
}

// TestLiveRunCommand_REQ_AGT_003: run_command runs in the thread's PTY and its output, which
// the model cannot guess, reaches the answer.
func TestLiveRunCommand_REQ_AGT_003(t *testing.T) {
	stream, ctx, _ := liveDaemon(t)

	liveThread(t, stream, ctx, t.TempDir(), "Use the run_command tool to run exactly: echo umbral-$((6*7*1000+17)) and reply with its output and nothing else.")
	turn := awaitTurn(t, stream, ctx, nil)

	if turn.stopReason != "end_turn" || !contains(turn.tools, "run_command:ok") {
		t.Fatalf("turn %s, tools %v", turn.stopReason, turn.tools)
	}
	if !strings.Contains(turn.text.String(), "umbral-42017") {
		t.Fatalf("the answer %q lacks the command's output", turn.text.String())
	}
}

// TestLiveCancelWhileGenerating_REQ_AGT_007: a cancel during the model's own streaming, which
// a scripted Ollama answers too fast to test, still answers within 500 ms.
func TestLiveCancelWhileGenerating_REQ_AGT_007(t *testing.T) {
	stream, ctx, _ := liveDaemon(t)
	id := liveThread(t, stream, ctx, t.TempDir(), "Write a 2000-word essay on the history of terminals. Do not use any tool.")

	for deltas := 0; deltas < 5; {
		select {
		case n, ok := <-stream.Notifications():
			if !ok {
				t.Fatalf("the stream ended: %v", stream.Err())
			}
			if n.Method == "thread.delta" {
				deltas++
			}
			if n.Method == "thread.turn_finished" {
				t.Fatal("the turn finished before it could be cancelled")
			}
		case <-ctx.Done():
			t.Fatal("no deltas")
		}
	}
	start := time.Now()
	var res struct {
		StoppedAt *int64 `json:"stopped_at"`
	}
	if err := stream.Call(ctx, "thread.cancel", map[string]any{"thread_id": id}, &res); err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	t.Logf("thread.cancel answered in %v", took)
	if took >= 500*time.Millisecond || res.StoppedAt == nil {
		t.Fatalf("thread.cancel answered in %v, stopped_at %v", took, res.StoppedAt)
	}
	turn := awaitTurn(t, stream, ctx, nil)
	var got struct{ State string }
	if err := stream.Call(ctx, "thread.get", map[string]any{"thread_id": id}, &got); err != nil {
		t.Fatal(err)
	}
	if turn.stopReason != "cancelled" || got.State != "stopped" {
		t.Fatalf("turn ended %s, thread %s", turn.stopReason, got.State)
	}
}

// TestLiveCancelDuringCommand_REQ_AGT_007: the model starts a long command, and the cancel
// stops it within 500 ms.
func TestLiveCancelDuringCommand_REQ_AGT_007(t *testing.T) {
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skipf("pgrep is not installed: %v", err)
	}
	stream, ctx, _ := liveDaemon(t)
	seconds := fmt.Sprintf("60.%d", os.Getpid())
	command := "sleep " + seconds
	id := liveThread(t, stream, ctx, t.TempDir(), "Use the run_command tool to run exactly: "+command)

	for approved := false; !approved; {
		select {
		case n, ok := <-stream.Notifications():
			if !ok {
				t.Fatalf("the stream ended: %v", stream.Err())
			}
			switch n.Method {
			case "approval.requested":
				var a struct{ ID string }
				_ = n.Decode(&a)
				if err := stream.Call(ctx, "approval.respond", map[string]any{"approval_id": a.ID, "decision": "approve", "scope": "once"}, nil); err != nil {
					t.Fatal(err)
				}
				approved = true
			case "thread.turn_finished":
				t.Fatal("the model never ran the command")
			}
		case <-ctx.Done():
			t.Fatal("run_command never asked")
		}
	}
	for !running(t, command) {
		select {
		case <-ctx.Done():
			t.Fatal("the sleep never started")
		case <-time.After(20 * time.Millisecond):
		}
	}
	start := time.Now()
	var res struct {
		StoppedAt *int64 `json:"stopped_at"`
	}
	if err := stream.Call(ctx, "thread.cancel", map[string]any{"thread_id": id}, &res); err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	t.Logf("thread.cancel answered in %v", took)
	if took >= 500*time.Millisecond || res.StoppedAt == nil || running(t, command) {
		t.Fatalf("thread.cancel answered in %v, stopped_at %v, sleep running %v", took, res.StoppedAt, running(t, command))
	}
	if turn := awaitTurn(t, stream, ctx, nil); turn.stopReason != "cancelled" {
		t.Fatalf("turn ended %s", turn.stopReason)
	}
}

// TestLiveUnknownToolIsRepaired_REQ_AGT_006: asked to call a tool that does not exist, the
// call is recorded invalid_args, nothing runs under that name, and the turn goes on to its
// retry and ends. Against gpt-oss:20b the model ran the name as a shell command first and then
// invented `search` and `find`; any invented name exercises the repair. A model that never
// makes an invalid call is a skip, not a pass.
func TestLiveUnknownToolIsRepaired_REQ_AGT_006(t *testing.T) {
	stream, ctx, _ := liveDaemon(t)
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, "README"), "Nothing to see here.\n")

	liveThread(t, stream, ctx, cwd, "Call the tool named frobnicate with the argument {\"level\": 3}, as a tool call and not as a shell command. It exists even if you were not told about it. If that fails, use read_file on README, reply with its first word, and stop.")
	turn := awaitTurn(t, stream, ctx, nil)

	invalid := -1
	for i, s := range turn.tools {
		if strings.HasSuffix(s, ":invalid_args") {
			invalid = i
			break
		}
	}
	if invalid < 0 {
		t.Skipf("the model made no invalid call: %v", turn.tools)
	}
	if turn.stopReason != "end_turn" && turn.stopReason != "tool_error" {
		t.Fatalf("turn ended %s after an invalid call", turn.stopReason)
	}
	if turn.stopReason == "end_turn" && invalid == len(turn.tools)-1 && turn.text.Len() == 0 {
		t.Fatalf("the turn ended end_turn right after the invalid call, with no retry: %v", turn.tools)
	}
}

// TestLiveMcpTool_REQ_MCP_001: the model calls an MCP server's tool under its prefixed name,
// the call asks as Network, and the server's answer reaches the model.
func TestLiveMcpTool_REQ_MCP_001(t *testing.T) {
	stream, ctx, _ := liveDaemon(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Call(ctx, "mcp.server.add", map[string]any{
		"name": "t", "transport": "stdio", "command": "/bin/sh",
		"args": []string{"-c", fmt.Sprintf("%s=1 exec %q", sdktest.ChildEnv, self)},
	}, nil); err != nil {
		t.Fatal(err)
	}
	var state struct{ State string }
	for state.State != "connected" {
		awaitNotification(t, stream, "mcp.server_state", &state)
		if state.State == "unavailable" {
			t.Fatalf("the server did not connect: %+v", state)
		}
	}

	liveThread(t, stream, ctx, t.TempDir(), "Use the mcp_t_echo tool with text \"ping-9137\" and reply with exactly what it returned.")
	turn := awaitTurn(t, stream, ctx, nil)

	if !contains(turn.approvals, "mcp_t_echo") || !contains(turn.tools, "mcp_t_echo:ok") || turn.stopReason != "end_turn" {
		t.Fatalf("turn %s, approvals %v, tools %v", turn.stopReason, turn.approvals, turn.tools)
	}
	if !strings.Contains(turn.text.String(), "ping-9137") {
		t.Fatalf("the answer %q lacks the server's echo", turn.text.String())
	}
}
