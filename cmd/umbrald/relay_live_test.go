//go:build live

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/client"
)

// TestLiveHostedAgentBehindItsRelay_REQ_ACP_007 is T-HRD-20c's Done line: a real OpenCode,
// launched behind its relay, completes a turn against a local Ollama through it; its thread says
// `confined` where the kernel confines a network; and a model on the loopback leaves no egress row.
func TestLiveHostedAgentBehindItsRelay_REQ_ACP_007(t *testing.T) {
	url := os.Getenv("UMBRAL_OLLAMA_URL")
	if url == "" {
		url = "http://127.0.0.1:11434"
	}
	model := os.Getenv("UMBRAL_LIVE_MODEL")
	if model == "" {
		model = "gpt-oss:20b"
	}
	requireModel(t, url, model)
	opencode := opencodeBinary(t)
	requireIdleModel(t, url, model)

	bin := buildDaemon(t)
	rt, daemonDir := isolatedRuntime(t)
	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "umbral")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(configDir, "models.toml"), fmt.Sprintf(`
[router]
offline = true

[classes]
code = ["ollama/%[1]s"]

[[providers]]
id = "ollama"
type = "ollama"
base_url = %[2]q
`, model, url))
	writeFile(t, filepath.Join(configDir, "config.toml"), fmt.Sprintf(`
[acp]
silence_timeout = 900

[[acp.agents]]
name = "opencode"
manifest = "opencode"
command = %[1]q
args = ["acp"]
relay = "ollama"
[acp.agents.config]
model = "ollama/%[2]s"
[acp.agents.config.provider.ollama]
npm = "@ai-sdk/openai-compatible"
name = "Ollama"
[acp.agents.config.provider.ollama.options]
baseURL = "%[3]s/v1"
[acp.agents.config.provider.ollama.models.%[4]q]
name = %[2]q
`, opencode, model, url, model))
	startStoppableDaemon(t, bin, rt)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	defer cancel()
	c, err := client.Connect(ctx, client.Options{
		SocketPath: filepath.Join(daemonDir, "umbral.sock"), NoAutostart: true, ClientKind: client.ClientKindTUI,
	})
	if err != nil {
		t.Fatal(err)
	}
	stream := client.NewStream(c)
	defer func() { _ = stream.Close() }()

	var th usf2Thread
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": t.TempDir(), "agent": "opencode"}, &th); err != nil {
		t.Fatal(err)
	}
	if th.Unavailable != nil {
		t.Fatalf("OpenCode did not start: %+v", th.Unavailable)
	}
	want := "unobserved"
	if st := platformSandbox("x").Status(); st.Enforcing() && st.Network {
		want = "confined"
	}
	if th.Egress != want {
		t.Errorf("the thread says %q, want %q", th.Egress, want)
	}
	if err := stream.Call(ctx, "thread.send", map[string]any{"thread_id": th.ID, "text": "Reply with the single word: pong. Use no tools."}, nil); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case n, ok := <-stream.Notifications():
			if !ok {
				t.Fatalf("the stream ended: %v", stream.Err())
			}
			if n.Method == "approval.requested" {
				var a struct {
					ID string `json:"id"`
				}
				_ = n.Decode(&a)
				_ = stream.Call(ctx, "approval.respond", map[string]any{"approval_id": a.ID, "decision": "deny", "scope": "once"}, nil)
				continue
			}
			if n.Method != "thread.turn_finished" {
				continue
			}
			var end struct {
				ThreadID   string `json:"thread_id"`
				StopReason string `json:"stop_reason"`
			}
			_ = n.Decode(&end)
			if end.ThreadID != th.ID {
				continue
			}
			if end.StopReason != "end_turn" {
				t.Errorf("the turn ended %s, want end_turn", end.StopReason)
			}
			t.Logf("answer: %q", lastAnswer(t, ctx, stream, th.ID))
			if n := egressRows(t, ctx, th.ID); n != 0 {
				t.Errorf("a model on the loopback left %d egress rows, want none", n)
			}
			return
		case <-ctx.Done():
			t.Fatal("the turn did not finish")
		}
	}
}
