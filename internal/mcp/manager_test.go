package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/mcp/domain"
)

var gitTools = []domain.ToolInfo{{Name: "get_issue", Description: "Read an issue", InputSchema: map[string]any{"type": "object"}}}

func stdio(name string) domain.AddParams {
	return domain.AddParams{Name: name, Transport: domain.TransportStdio, Command: "mcp-" + name}
}

// TestAddConnectsAndSyncsTools_REQ_MCP_002: mcp.server.add persists the server, answers it
// `connecting`, then connects it and hands its tools to the registry — no restart of anything.
func TestAddConnectsAndSyncsTools_REQ_MCP_002(t *testing.T) {
	r := newRig(t, &fakeConnector{tools: gitTools}, fakeSecrets{})
	got, err := r.m.Add(t.Context(), stdio("gitlab"))
	if err != nil {
		t.Fatal(err)
	}
	if got.State != domain.StateConnecting || got.Trust != domain.TrustUntrusted || !strings.HasPrefix(got.ID, "mcp_") {
		t.Fatalf("add answered %+v", got)
	}
	r.await(t, "gitlab", domain.StateConnected)
	if !r.sink.has("gitlab") {
		t.Fatal("the registry never got the server's tools")
	}
	list, _ := r.m.List(t.Context())
	if len(list) != 1 || list[0].State != domain.StateConnected || len(list[0].Tools) != 1 || list[0].Tools[0] != "get_issue" {
		t.Fatalf("list = %+v", list)
	}
	if _, err := r.m.Add(t.Context(), stdio("gitlab")); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("a second gitlab: %v", err)
	}
}

// TestRemoveDisconnects_REQ_MCP_002: mcp.server.remove closes the session, takes the tools out
// of the registry and deletes the row; an unknown name is NOT_FOUND.
func TestRemoveDisconnects_REQ_MCP_002(t *testing.T) {
	r := newRig(t, &fakeConnector{tools: gitTools}, fakeSecrets{})
	if _, err := r.m.Add(t.Context(), stdio("gitlab")); err != nil {
		t.Fatal(err)
	}
	r.await(t, "gitlab", domain.StateConnected)
	session := r.conn.last()
	if err := r.m.Remove(t.Context(), "gitlab"); err != nil {
		t.Fatal(err)
	}
	if !session.closed.Load() || r.sink.has("gitlab") {
		t.Fatalf("closed %v, tools still registered %v", session.closed.Load(), r.sink.has("gitlab"))
	}
	if list, _ := r.store.List(t.Context()); len(list) != 0 {
		t.Fatalf("the row is still there: %+v", list)
	}
	if err := r.m.Remove(t.Context(), "gitlab"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("removing it again: %v", err)
	}
}

// TestMcpReconnectBackoff_REQ_MCP_003: a server that crashes is marked unavailable, its
// pending call fails, and it is reconnected with exponential backoff; one that never comes
// back is tried five times and then stays unavailable.
func TestMcpReconnectBackoff_REQ_MCP_003(t *testing.T) {
	conn := &fakeConnector{tools: gitTools}
	r := newRig(t, conn, fakeSecrets{})
	if _, err := r.m.Add(t.Context(), stdio("gitlab")); err != nil {
		t.Fatal(err)
	}
	r.await(t, "gitlab", domain.StateConnected)

	// A call in flight when the process dies gets an error.
	first := conn.last()
	started := make(chan struct{})
	first.call = func(ctx context.Context, _ string, _ json.RawMessage) (domain.CallResult, error) {
		close(started)
		select {
		case <-first.done:
			return domain.CallResult{}, errors.New("connection closed")
		case <-ctx.Done():
			return domain.CallResult{}, ctx.Err()
		}
	}
	errc := make(chan error, 1)
	go func() {
		_, err := r.m.Call(t.Context(), "gitlab", "get_issue", json.RawMessage(`{}`))
		errc <- err
	}()
	<-started
	conn.mu.Lock()
	conn.fail = func(int) error { return errors.New("spawn failed") }
	conn.mu.Unlock()
	first.crash()
	if err := <-errc; err == nil {
		t.Fatal("the pending call did not fail")
	}
	r.await(t, "gitlab", domain.StateUnavailable)

	// Five attempts, spaced 10, 20, 40, 80 and 160 ms, and then no more.
	start := time.Now()
	deadline := time.Now().Add(5 * time.Second)
	for conn.count() < 1+domain.MaxReconnects && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	elapsed := time.Since(start)
	time.Sleep(500 * time.Millisecond)
	if n := conn.count() - 1; n != domain.MaxReconnects {
		t.Fatalf("%d reconnection attempts, want %d", n, domain.MaxReconnects)
	}
	if elapsed < 300*time.Millisecond {
		t.Fatalf("five attempts in %v: the waits do not grow", elapsed)
	}
	if got := r.store.state("gitlab"); got != domain.StateUnavailable {
		t.Fatalf("after the last attempt the server is %s", got)
	}
	if _, err := r.m.Call(t.Context(), "gitlab", "get_issue", json.RawMessage(`{}`)); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("a call to an unavailable server: %v", err)
	}
}

// TestAReconnectedServerServesAgain_REQ_MCP_003: when an attempt succeeds the server is
// connected again and its tools are synced again.
func TestAReconnectedServerServesAgain_REQ_MCP_003(t *testing.T) {
	conn := &fakeConnector{tools: gitTools, fail: func(n int) error {
		if n == 2 {
			return errors.New("not yet")
		}
		return nil
	}}
	r := newRig(t, conn, fakeSecrets{})
	if _, err := r.m.Add(t.Context(), stdio("gitlab")); err != nil {
		t.Fatal(err)
	}
	r.await(t, "gitlab", domain.StateConnected)
	conn.last().crash()
	r.await(t, "gitlab", domain.StateUnavailable)
	r.await(t, "gitlab", domain.StateConnected)
	if res, err := r.m.Call(t.Context(), "gitlab", "get_issue", json.RawMessage(`{"id":1}`)); err != nil || res.Text != `get_issue {"id":1}` {
		t.Fatalf("after reconnecting: %+v %v", res, err)
	}
}

// TestASilentServerIsUnavailable_REQ_MCP_003: a call the server does not answer in time fails,
// and the server is marked unavailable and reconnected.
func TestASilentServerIsUnavailable_REQ_MCP_003(t *testing.T) {
	conn := &fakeConnector{tools: gitTools}
	r := newRig(t, conn, fakeSecrets{})
	if _, err := r.m.Add(t.Context(), stdio("gitlab")); err != nil {
		t.Fatal(err)
	}
	r.await(t, "gitlab", domain.StateConnected)
	silent := conn.last()
	silent.call = func(ctx context.Context, _ string, _ json.RawMessage) (domain.CallResult, error) {
		<-ctx.Done()
		return domain.CallResult{}, ctx.Err()
	}
	start := time.Now()
	if _, err := r.m.Call(t.Context(), "gitlab", "get_issue", json.RawMessage(`{}`)); err == nil {
		t.Fatal("a call nobody answered succeeded")
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("the call waited %v", took)
	}
	e := r.await(t, "gitlab", domain.StateUnavailable)
	if !strings.Contains(e.LastError, "respond") {
		t.Fatalf("last_error %q", e.LastError)
	}
	if !silent.closed.Load() {
		t.Fatal("the silent session was not closed")
	}
	r.await(t, "gitlab", domain.StateConnected)
}

// TestACancelledCallLeavesTheServerAlone_REQ_MCP_003: a call the turn cancels is not the
// server's fault: it stays connected.
func TestACancelledCallLeavesTheServerAlone_REQ_MCP_003(t *testing.T) {
	conn := &fakeConnector{tools: gitTools}
	r := newRig(t, conn, fakeSecrets{})
	if _, err := r.m.Add(t.Context(), stdio("gitlab")); err != nil {
		t.Fatal(err)
	}
	r.await(t, "gitlab", domain.StateConnected)
	conn.last().call = func(ctx context.Context, _ string, _ json.RawMessage) (domain.CallResult, error) {
		<-ctx.Done()
		return domain.CallResult{}, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := r.m.Call(ctx, "gitlab", "get_issue", json.RawMessage(`{}`)); err == nil {
		t.Fatal("a cancelled call succeeded")
	}
	time.Sleep(300 * time.Millisecond)
	if got := r.store.state("gitlab"); got != domain.StateConnected || conn.count() != 1 {
		t.Fatalf("a cancelled call left the server %s after %d connections; want connected, never dropped", got, conn.count())
	}
}

// TestEnvRefsAreResolved_REQ_MCP_001: a stdio server's env_refs reach its process resolved;
// one that cannot be resolved leaves the server unavailable with the reason and never starts
// it; an env: reference outside the keyring fallback is CONFIG_INVALID at add.
func TestEnvRefsAreResolved_REQ_MCP_001(t *testing.T) {
	conn := &fakeConnector{tools: gitTools}
	r := newRig(t, conn, fakeSecrets{values: map[string]string{"umbral/gl": "glpat-x"}})
	p := stdio("gitlab")
	p.EnvRefs = map[string]string{"GITLAB_TOKEN": "keyring:umbral/gl"}
	if _, err := r.m.Add(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	r.await(t, "gitlab", domain.StateConnected)
	if got := conn.envs[0]["GITLAB_TOKEN"]; got != "glpat-x" {
		t.Fatalf("the process got GITLAB_TOKEN=%q", got)
	}

	missing := stdio("github")
	missing.EnvRefs = map[string]string{"GH_TOKEN": "keyring:umbral/none"}
	if _, err := r.m.Add(t.Context(), missing); err != nil {
		t.Fatal(err)
	}
	e := r.await(t, "github", domain.StateUnavailable)
	if !strings.Contains(e.LastError, "GH_TOKEN") || strings.Contains(e.LastError, "glpat") {
		t.Fatalf("last_error %q", e.LastError)
	}
	if conn.count() != 1 {
		t.Fatalf("a server whose secret is missing was started (%d connects)", conn.count())
	}

	env := stdio("jira")
	env.EnvRefs = map[string]string{"T": "env:JIRA_TOKEN"}
	if _, err := r.m.Add(t.Context(), env); !errors.Is(err, domain.ErrConfigInvalid) {
		t.Fatalf("an env: ref without the fallback: %v", err)
	}
}

// TestARemoteCallCarryingASecretIsRefused_REQ_MCP_001: Art. 4 — arguments for a server off
// this machine go through the redaction rules, and a call they would change is refused rather
// than sent. A loopback server is not off the machine.
func TestARemoteCallCarryingASecretIsRefused_REQ_MCP_001(t *testing.T) {
	conn := &fakeConnector{tools: gitTools}
	r := newRig(t, conn, fakeSecrets{})
	for _, p := range []domain.AddParams{
		{Name: "remote", Transport: domain.TransportHTTP, URL: "https://mcp.example.com/mcp"},
		{Name: "local", Transport: domain.TransportHTTP, URL: "http://127.0.0.1:9000/mcp"},
	} {
		if _, err := r.m.Add(t.Context(), p); err != nil {
			t.Fatal(err)
		}
		r.await(t, p.Name, domain.StateConnected)
	}
	secret := json.RawMessage(`{"token":"ghp_secret"}`)
	if _, err := r.m.Call(t.Context(), "remote", "get_issue", secret); err == nil {
		t.Fatal("a secret was sent to a remote server")
	}
	if _, err := r.m.Call(t.Context(), "local", "get_issue", secret); err != nil {
		t.Fatalf("a loopback server: %v", err)
	}
}

// TestStartConnectsStoredServers_REQ_MCP_001: at start, every configured server is connected.
func TestStartConnectsStoredServers_REQ_MCP_001(t *testing.T) {
	conn := &fakeConnector{tools: gitTools}
	r := newRig(t, conn, fakeSecrets{}, domain.Server{
		ID: "mcp_01", Name: "gitlab", Transport: domain.TransportStdio, Command: "mcp-gitlab",
		Trust: domain.TrustUntrusted, State: domain.StateUnavailable,
	})
	r.await(t, "gitlab", domain.StateConnected)
	if !r.sink.has("gitlab") {
		t.Fatal("a stored server's tools never reached the registry")
	}
}

// TestAFlappingServerIsNotRetriedForever_REQ_MCP_003: a server that connects and drops at once
// spends the five attempts like one that never connects; only a connection that stayed up resets
// the count.
func TestAFlappingServerIsNotRetriedForever_REQ_MCP_003(t *testing.T) {
	conn := &fakeConnector{tools: gitTools}
	r := newRig(t, conn, fakeSecrets{})
	conn.mu.Lock()
	conn.fail = nil
	conn.mu.Unlock()
	if _, err := r.m.Add(t.Context(), stdio("gitlab")); err != nil {
		t.Fatal(err)
	}
	// Every session is killed as soon as it appears, for two seconds.
	crashed := map[*fakeSession]bool{}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if sess := conn.last(); sess != nil && !crashed[sess] {
			crashed[sess] = true
			sess.crash()
		}
	}
	if n := conn.count(); n != 1+domain.MaxReconnects {
		t.Fatalf("%d connections for a server that never stays up, want %d", n, 1+domain.MaxReconnects)
	}
}

// TestADegradedSecretIsReported_REQ_MCP_001: REQ-SEC-012 — a server running on an env:
// reference says so while it is connected.
func TestADegradedSecretIsReported_REQ_MCP_001(t *testing.T) {
	r := newRig(t, &fakeConnector{tools: gitTools}, fakeSecrets{allowEnv: true, values: map[string]string{"JIRA_TOKEN": "j"}})
	p := stdio("jira")
	p.EnvRefs = map[string]string{"T": "env:JIRA_TOKEN"}
	if _, err := r.m.Add(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	e := r.await(t, "jira", domain.StateConnected)
	if !strings.Contains(e.LastError, "env_secret") || !strings.Contains(e.LastError, "T") {
		t.Fatalf("last_error %q, want the degraded env_secret reported", e.LastError)
	}
}

// TestAURLCarryingASecretIsRefused_REQ_MCP_001: REQ-SEC-004 — an http URL the redaction rules
// would change is not stored.
func TestAURLCarryingASecretIsRefused_REQ_MCP_001(t *testing.T) {
	r := newRig(t, &fakeConnector{tools: gitTools}, fakeSecrets{})
	_, err := r.m.Add(t.Context(), domain.AddParams{Name: "web", Transport: domain.TransportHTTP, URL: "https://h/mcp?token=ghp_secret"})
	if !errors.Is(err, domain.ErrConfigInvalid) || strings.Contains(err.Error(), "ghp_secret") {
		t.Fatalf("add = %v, want CONFIG_INVALID without the value", err)
	}
	if list, _ := r.store.List(t.Context()); len(list) != 0 {
		t.Fatal("the URL was stored")
	}
}

// TestAnAddAfterCloseStartsNothing_REQ_MCP_002: a closed manager starts no connection.
func TestAnAddAfterCloseStartsNothing_REQ_MCP_002(t *testing.T) {
	conn := &fakeConnector{tools: gitTools}
	r := newRig(t, conn, fakeSecrets{})
	r.m.Close()
	if _, err := r.m.Add(t.Context(), stdio("gitlab")); err == nil {
		t.Fatal("a closed manager accepted a server")
	}
	time.Sleep(50 * time.Millisecond)
	if conn.count() != 0 {
		t.Fatal("a closed manager connected a server")
	}
}
