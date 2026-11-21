package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ecrespo/umbral/internal/acp/adapters/sdk/acptest"
	"github.com/ecrespo/umbral/internal/client"
)

// relayDaemon starts a daemon whose `fake` agent reaches its model through a relay to the
// scripted Ollama, the `ollama` provider of models.toml, and returns that Ollama's address.
func relayDaemon(t *testing.T, settings string, extraEnv ...string) (*client.Stream, context.Context, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	srv := (&scriptedOllama{}).server(t)
	stream, ctx, _ := startAgentDaemon(t, srv.URL, daemonOpts{extraEnv: extraEnv, settings: settings + fmt.Sprintf(`
[acp]
silence_timeout = 30

[[acp.agents]]
name = "fake"
manifest = "opencode"
command = %q
args = [%q]
relay = "ollama"
[acp.agents.config]
model = "ollama/m"
[acp.agents.config.provider.ollama.options]
baseURL = %q
`, exe, acptest.Arg, srv.URL)})
	return stream, ctx, srv.URL
}

// egressRows counts the thread's egress_log rows.
func egressRows(t *testing.T, ctx context.Context, threadID string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(os.Getenv("XDG_DATA_HOME"), "umbral", "umbral.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM egress_log WHERE thread_id = ?`, threadID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestAHostedAgentReachesItsModelThroughTheRelayOnly_REQ_ACP_007: an agent behind its relay
// reaches its model through it, with the relay's token and never the provider's key; the kernel
// refuses it the model's own port; its thread reads `confined` from its creation on; and a model
// on the loopback leaves no egress row, as Umbral's own calls to one leave none.
func TestAHostedAgentReachesItsModelThroughTheRelayOnly_REQ_ACP_007(t *testing.T) {
	needSandbox(t)
	if !platformSandbox("x").Status().Network {
		if os.Getenv("UMBRAL_REQUIRE_SANDBOX") == "1" {
			t.Fatal("UMBRAL_REQUIRE_SANDBOX is set and this system cannot confine a network")
		}
		t.Skip("this kernel cannot confine a network")
	}
	stream, ctx, model := relayDaemon(t, "")
	var thread acpThread
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": t.TempDir(), "agent": "fake"}, &thread); err != nil {
		t.Fatal(err)
	}
	if thread.AgentUnavailable != nil {
		t.Fatalf("the agent did not start: %+v", thread.AgentUnavailable)
	}
	if thread.EgressVisibility != "confined" {
		t.Errorf("thread.create says %q, want confined", thread.EgressVisibility)
	}
	if got := agentDoes(t, ctx, stream, thread.ID, "model /api/tags"); got != "status 200" {
		t.Errorf("the agent's model call through its relay answered %q", got)
	}
	u, err := url.Parse(model)
	if err != nil {
		t.Fatal(err)
	}
	if got := agentDoes(t, ctx, stream, thread.ID, "connect "+u.Host); !refused(got) {
		t.Errorf("a connection to the model's own port answered %q, want the kernel's refusal", got)
	}
	r := agentReport(t, ctx, stream, thread.ID)
	if !slices.Contains(r.Args, "--port") {
		t.Errorf("the agent was not given its own port: %q", r.Args)
	}
	if strings.Contains(r.Config, model) {
		t.Errorf("the agent's configuration still names the model's own endpoint:\n%s", r.Config)
	}
	var got acpThread
	if err := stream.Call(ctx, "thread.get", map[string]any{"thread_id": thread.ID}, &got); err != nil {
		t.Fatal(err)
	}
	if got.EgressVisibility != "confined" {
		t.Errorf("thread.get says %q, want confined", got.EgressVisibility)
	}
	if n := egressRows(t, ctx, thread.ID); n != 0 {
		t.Errorf("a model on the loopback left %d egress rows, want none", n)
	}
}

// TestARelayWithoutTheSandboxStillServesItsAgent_REQ_ACP_007: with the sandbox off the agent
// still reaches its model through its relay, but nothing confines its network, and its thread
// does not say otherwise.
func TestARelayWithoutTheSandboxStillServesItsAgent_REQ_ACP_007(t *testing.T) {
	stream, ctx, _ := relayDaemon(t, "[sandbox]\nmode = \"off\"\n")
	var thread acpThread
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": t.TempDir(), "agent": "fake"}, &thread); err != nil {
		t.Fatal(err)
	}
	if thread.EgressVisibility != "unobserved" {
		t.Errorf("thread.create says %q, want unobserved", thread.EgressVisibility)
	}
	if got := agentDoes(t, ctx, stream, thread.ID, "model /api/tags"); got != "status 200" {
		t.Errorf("the agent's model call through its relay answered %q", got)
	}
}

// TestAConfinedThreadsFullAutoTerminalHasNoNetwork_REQ_SBX_007: a terminal a hosted agent opens
// unasked in `full-auto` would otherwise be the network its confined thread was kept from, with
// no row of what it sent: where the thread's network is confined the terminal's shell has none.
// The same connection approved in `normal` is the user's decision, and goes through.
func TestAConfinedThreadsFullAutoTerminalHasNoNetwork_REQ_SBX_007(t *testing.T) {
	needSandbox(t)
	if !platformSandbox("x").Status().Network {
		if os.Getenv("UMBRAL_REQUIRE_SANDBOX") == "1" {
			t.Fatal("UMBRAL_REQUIRE_SANDBOX is set and this system cannot confine a network")
		}
		t.Skip("this kernel cannot confine a network")
	}
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("no curl to open a TCP connection with")
	}
	repo, env := gitRepo(t)
	stream, ctx, model := relayDaemon(t, "", env...)
	thread, _ := hostedThread(t, ctx, stream, repo)
	if thread.EgressVisibility != "confined" {
		t.Fatalf("the thread is %q, want confined", thread.EgressVisibility)
	}
	// curl exits 0 on the model's answer and non-zero when its connection is refused.
	probe := "term " + curl + " -s -o /dev/null " + model + "/api/tags"
	terminal := func() (answer string, asked int) {
		t.Helper()
		if err := stream.Call(ctx, "thread.send", map[string]any{"thread_id": thread.ID, "text": probe}, nil); err != nil {
			t.Fatal(err)
		}
		for {
			select {
			case n, ok := <-stream.Notifications():
				if !ok {
					t.Fatalf("the stream ended: %v", stream.Err())
				}
				switch n.Method {
				case "approval.requested":
					var a struct{ ID string }
					_ = n.Decode(&a)
					asked++
					if err := stream.Call(ctx, "approval.respond", map[string]any{"approval_id": a.ID, "decision": "approve", "scope": "once"}, nil); err != nil {
						t.Fatal(err)
					}
				case "thread.turn_finished":
					return lastAnswer(t, ctx, stream, thread.ID), asked
				}
			case <-ctx.Done():
				t.Fatal("the turn did not finish")
			}
		}
	}
	if err := stream.Call(ctx, "thread.update", map[string]any{"thread_id": thread.ID, "mode": "full-auto"}, &thread); err != nil {
		t.Fatal(err)
	}
	// The command must have run: a terminal refused for another reason proves nothing.
	if answer, asked := terminal(); asked != 0 || !strings.HasPrefix(answer, "exit=") || strings.HasPrefix(answer, "exit=0 ") {
		t.Errorf("full-auto: asked %d, a terminal's connection to the model's port answered %q, want refused", asked, answer)
	}
	if err := stream.Call(ctx, "thread.update", map[string]any{"thread_id": thread.ID, "mode": "normal"}, &thread); err != nil {
		t.Fatal(err)
	}
	if answer, asked := terminal(); asked != 1 || !strings.HasPrefix(answer, "exit=0 ") {
		t.Errorf("normal, approved: asked %d, answered %q, want the connection made", asked, answer)
	}
}
