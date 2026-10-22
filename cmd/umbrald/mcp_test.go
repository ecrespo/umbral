package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/client"
	"github.com/ecrespo/umbral/internal/mcp/adapters/sdk/sdktest"
)

// TestMain lets this test binary double as an MCP server: started with sdktest.ChildEnv set,
// it serves the test server on stdio instead of running tests.
func TestMain(m *testing.M) {
	sdktest.Serve()
	os.Exit(m.Run())
}

// awaitNotification reads notifications until one of method arrives, and returns its params
// decoded into v. Every approval.requested met on the way is approved once.
func awaitNotification(t *testing.T, stream *client.Stream, method string, v any) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case n, ok := <-stream.Notifications():
			if !ok {
				t.Fatalf("the stream ended: %v", stream.Err())
			}
			if n.Method == method {
				if v != nil {
					_ = n.Decode(v)
				}
				return
			}
		case <-deadline:
			t.Fatalf("no %s", method)
		}
	}
}

// TestMcpAddMidThread_REQ_MCP_002: through a real daemon, a server added with mcp.server.add
// while a thread exists is offered to that thread from its next turn, prefixed (REQ-MCP-001),
// without restarting anything; its tool asks by default (REQ-MCP-004), and once approved the
// server's answer reaches the model.
func TestMcpAddMidThread_REQ_MCP_002(t *testing.T) {
	ollama := &scriptedOllama{replies: [][]string{
		{`{"model":"m","message":{"role":"assistant","content":"Hello."},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":2}`},
		{`{"model":"m","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"mcp_t_echo","arguments":{"text":"from the mcp server"}}}]},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`},
		{`{"model":"m","message":{"role":"assistant","content":"Done."},"done":true,"done_reason":"stop","prompt_eval_count":20,"eval_count":2}`},
	}}
	srv := ollama.server(t)
	stream, ctx := agentDaemon(t, srv.URL)

	var thread struct{ ID string }
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": t.TempDir()}, &thread); err != nil {
		t.Fatal(err)
	}
	if err := stream.Call(ctx, "thread.send", map[string]any{"thread_id": thread.ID, "text": "hi"}, nil); err != nil {
		t.Fatal(err)
	}
	awaitNotification(t, stream, "thread.turn_finished", nil)

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var added struct{ Name, State, Trust string }
	if err := stream.Call(ctx, "mcp.server.add", map[string]any{
		"name": "t", "transport": "stdio", "command": "/bin/sh",
		"args": []string{"-c", fmt.Sprintf("%s=1 exec %q", sdktest.ChildEnv, self)},
	}, &added); err != nil {
		t.Fatal(err)
	}
	if added.State != "connecting" || added.Trust != "untrusted" {
		t.Fatalf("mcp.server.add = %+v", added)
	}
	var state struct{ Name, State string }
	for state.State != "connected" {
		awaitNotification(t, stream, "mcp.server_state", &state)
		if state.State == "unavailable" {
			t.Fatalf("the server did not connect: %+v", state)
		}
	}

	if err := stream.Call(ctx, "thread.send", map[string]any{"thread_id": thread.ID, "text": "echo something"}, nil); err != nil {
		t.Fatal(err)
	}
	var approval struct{ ID, Tool, Risk string }
	awaitNotification(t, stream, "approval.requested", &approval)
	if approval.Tool != "mcp_t_echo" || approval.Risk != "Network" {
		t.Fatalf("approval.requested %+v, want mcp_t_echo asking as Network", approval)
	}
	if err := stream.Call(ctx, "approval.respond", map[string]any{"approval_id": approval.ID, "decision": "approve", "scope": "once"}, nil); err != nil {
		t.Fatal(err)
	}
	var end struct {
		StopReason string `json:"stop_reason"`
	}
	awaitNotification(t, stream, "thread.turn_finished", &end)
	if end.StopReason != "end_turn" {
		t.Fatalf("the second turn ended %s", end.StopReason)
	}

	ollama.mu.Lock()
	bodies := append([]string(nil), ollama.bodies...)
	ollama.mu.Unlock()
	if len(bodies) != 3 {
		t.Fatalf("%d model calls, want 3", len(bodies))
	}
	if strings.Contains(bodies[0], "mcp_t_") {
		t.Fatal("the first turn, before the server was added, was offered its tools")
	}
	if !strings.Contains(bodies[1], `"mcp_t_echo"`) {
		t.Fatal("the turn after mcp.server.add was not offered mcp_t_echo")
	}
	if !strings.Contains(bodies[2], "from the mcp server") {
		t.Fatal("the server's answer never reached the model")
	}

	var list struct {
		Items []struct {
			Name, State string
			Tools       []string
		}
	}
	if err := stream.Call(ctx, "mcp.server.list", map[string]any{}, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].State != "connected" || len(list.Items[0].Tools) != 5 {
		t.Fatalf("mcp.server.list = %+v", list)
	}
	var status struct {
		MCP []struct{ Name, State string } `json:"mcp"`
	}
	if err := stream.Call(ctx, "system.status", map[string]any{}, &status); err != nil || len(status.MCP) != 1 || status.MCP[0].State != "connected" {
		t.Fatalf("system.status mcp = %+v %v", status.MCP, err)
	}
	if err := stream.Call(ctx, "mcp.server.remove", map[string]any{"name": "t"}, nil); err != nil {
		t.Fatal(err)
	}
}
