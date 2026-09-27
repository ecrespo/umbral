package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/client"
)

// scriptedOllama is an Ollama that answers /api/chat from a script, one reply per call.
type scriptedOllama struct {
	mu      sync.Mutex
	replies [][]string
	bodies  []string
}

func (o *scriptedOllama) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"models":[{"name":"m","details":{"context_length":32768},"capabilities":["completion","tools"]}]}`)
	})
	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		o.mu.Lock()
		o.bodies = append(o.bodies, string(body))
		var reply []string
		if len(o.replies) > 0 {
			reply, o.replies = o.replies[0], o.replies[1:]
		}
		o.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-ndjson")
		for _, line := range reply {
			_, _ = fmt.Fprintln(w, line)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestAThreadRunsAgainstARealDaemon_REQ_AGT_001 drives the whole path through the socket:
// thread.create and thread.send reach the runtime, the model (an Ollama served locally) asks
// for read_file, the policy allows a ReadOnly call in normal mode, the tool reads the file,
// the model answers from it, and the client sees the deltas, the tool call and the turn's end.
func TestAThreadRunsAgainstARealDaemon_REQ_AGT_001(t *testing.T) {
	ollama := &scriptedOllama{replies: [][]string{
		{`{"model":"m","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"read_file","arguments":{"path":"notes.txt"}}}]},"done":true,"done_reason":"stop","prompt_eval_count":40,"eval_count":8}`},
		{
			`{"model":"m","message":{"role":"assistant","content":"The notes "},"done":false}`,
			`{"model":"m","message":{"role":"assistant","content":"say hello."},"done":true,"done_reason":"stop","prompt_eval_count":60,"eval_count":5}`,
		},
	}}
	srv := ollama.server(t)

	stream, ctx := agentDaemon(t, srv.URL)

	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, "notes.txt"), "hello from the notes\n")
	var thread struct{ ID, State string }
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": cwd}, &thread); err != nil {
		t.Fatal(err)
	}
	var sent struct {
		TurnID string `json:"turn_id"`
	}
	if err := stream.Call(ctx, "thread.send", map[string]any{"thread_id": thread.ID, "text": "what do the notes say?"}, &sent); err != nil {
		t.Fatal(err)
	}

	var text strings.Builder
	var toolStatuses []string
	var finished struct {
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InTokens int64 `json:"in_tokens"`
		} `json:"usage"`
	}
wait:
	for {
		select {
		case n, ok := <-stream.Notifications():
			if !ok {
				t.Fatalf("the stream ended: %v", stream.Err())
			}
			switch n.Method {
			case "thread.delta":
				var d struct{ Text string }
				_ = n.Decode(&d)
				text.WriteString(d.Text)
			case "thread.tool_call":
				var tc struct{ Tool, Status string }
				_ = n.Decode(&tc)
				toolStatuses = append(toolStatuses, tc.Tool+":"+tc.Status)
			case "thread.turn_finished":
				_ = n.Decode(&finished)
				break wait
			}
		case <-ctx.Done():
			t.Fatal("the turn did not finish")
		}
	}
	if finished.StopReason != "end_turn" || finished.Usage.InTokens != 100 {
		t.Fatalf("turn finished %+v", finished)
	}
	if text.String() != "The notes say hello." {
		t.Fatalf("deltas %q", text.String())
	}
	if strings.Join(toolStatuses, ",") != "read_file:pending,read_file:ok" {
		t.Fatalf("tool call statuses %v", toolStatuses)
	}
	ollama.mu.Lock()
	second := ollama.bodies[len(ollama.bodies)-1]
	ollama.mu.Unlock()
	if !strings.Contains(second, "hello from the notes") {
		t.Fatalf("the model never read the tool's result: %s", second)
	}

	var got struct {
		State    string
		Messages []struct{ Role, Content string }
	}
	if err := stream.Call(ctx, "thread.get", map[string]any{"thread_id": thread.ID, "include_messages": true}, &got); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got)
	if got.State != "idle" || len(got.Messages) != 3 || got.Messages[2].Content != "The notes say hello." {
		t.Fatalf("thread.get = %s", raw)
	}
}

// agentDaemon starts a daemon whose only model is the scripted Ollama at url, connects a
// streaming TUI client, and waits for the model to be discovered.
func agentDaemon(t *testing.T, url string) (*client.Stream, context.Context) {
	t.Helper()
	bin := buildDaemon(t)
	rt, daemonDir := isolatedRuntime(t)
	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "umbral")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(configDir, "models.toml"), fmt.Sprintf(`
[classes]
code = ["ollama/m"]
fast = ["ollama/m"]

[[providers]]
id = "ollama"
type = "ollama"
base_url = %q
`, url))
	startDaemon(t, bin, rt)

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	t.Cleanup(cancel)
	c, err := client.Connect(ctx, client.Options{
		SocketPath: filepath.Join(daemonDir, "umbral.sock"), NoAutostart: true, ClientKind: client.ClientKindTUI,
	})
	if err != nil {
		t.Fatal(err)
	}
	stream := client.NewStream(c)
	t.Cleanup(func() { _ = stream.Close() })

	// Discovery runs in the background: wait for the model.
	for {
		var list struct{ Items []struct{ ID, Health string } }
		if err := stream.Call(ctx, "model.list", nil, &list); err != nil {
			t.Fatal(err)
		}
		if len(list.Items) == 1 && list.Items[0].Health == "ok" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("the model was never discovered: %+v", list)
		case <-time.After(50 * time.Millisecond):
		}
	}

	return stream, ctx
}

// TestAnApprovalRoundTripsThroughARealDaemon_REQ_AGT_004: in normal mode write_file asks; the
// client sees approval.requested with the diff, approves through approval.respond, and the
// file is written — on the real store, whose transactions pause and resume the thread.
func TestAnApprovalRoundTripsThroughARealDaemon_REQ_AGT_004(t *testing.T) {
	ollama := &scriptedOllama{replies: [][]string{
		{`{"model":"m","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"write_file","arguments":{"path":"out.txt","content":"written by the agent\n"}}}]},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`},
		{`{"model":"m","message":{"role":"assistant","content":"Done."},"done":true,"done_reason":"stop","prompt_eval_count":20,"eval_count":2}`},
	}}
	srv := ollama.server(t)
	stream, ctx := agentDaemon(t, srv.URL)

	cwd := t.TempDir()
	var thread struct{ ID string }
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": cwd}, &thread); err != nil {
		t.Fatal(err)
	}
	if err := stream.Call(ctx, "thread.send", map[string]any{"thread_id": thread.ID, "text": "write the file"}, nil); err != nil {
		t.Fatal(err)
	}
	var approval struct {
		ID, Tool, Reason, Diff, State string
	}
	var finished struct {
		StopReason string `json:"stop_reason"`
	}
wait:
	for {
		select {
		case n, ok := <-stream.Notifications():
			if !ok {
				t.Fatalf("the stream ended: %v", stream.Err())
			}
			switch n.Method {
			case "approval.requested":
				_ = n.Decode(&approval)
				var got struct{ State string }
				if err := stream.Call(ctx, "thread.get", map[string]any{"thread_id": thread.ID}, &got); err != nil || got.State != "awaiting_approval" {
					t.Fatalf("while paused: %+v %v", got, err)
				}
				if _, err := os.Stat(filepath.Join(cwd, "out.txt")); err == nil {
					t.Fatal("the file was written before the approval")
				}
				if err := stream.Call(ctx, "approval.respond", map[string]any{"approval_id": approval.ID, "decision": "approve", "scope": "once"}, nil); err != nil {
					t.Fatal(err)
				}
			case "thread.turn_finished":
				_ = n.Decode(&finished)
				break wait
			}
		case <-ctx.Done():
			t.Fatal("the turn did not finish")
		}
	}
	if approval.Tool != "write_file" || approval.Reason != "policy" || !strings.Contains(approval.Diff, "+written by the agent") {
		t.Fatalf("approval.requested %+v", approval)
	}
	if finished.StopReason != "end_turn" {
		t.Fatalf("turn finished %+v", finished)
	}
	data, err := os.ReadFile(filepath.Join(cwd, "out.txt"))
	if err != nil || string(data) != "written by the agent\n" {
		t.Fatalf("out.txt = %q %v", data, err)
	}
	var list struct {
		Items []struct {
			State         string
			DecisionScope string `json:"decision_scope"`
		}
	}
	if err := stream.Call(ctx, "approval.list", map[string]any{"all": true}, &list); err != nil || len(list.Items) != 1 ||
		list.Items[0].State != "approved" || list.Items[0].DecisionScope != "once" {
		t.Fatalf("approval.list %+v %v", list, err)
	}
}
