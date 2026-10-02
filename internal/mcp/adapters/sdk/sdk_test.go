package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	llmdomain "github.com/ecrespo/umbral/internal/llmgw/domain"
	"github.com/ecrespo/umbral/internal/mcp/adapters/sdk/sdktest"
	"github.com/ecrespo/umbral/internal/mcp/domain"
)

func TestMain(m *testing.M) {
	sdktest.Serve()
	os.Exit(m.Run())
}

// child is the env_ref that makes the test binary a server: the daemon's environment is not
// inherited, so the marker has to be passed the way a server's own variables are.
var child = map[string]string{sdktest.ChildEnv: "1"}

// stdioServer is the test binary itself, serving MCP on stdio.
func stdioServer(t *testing.T) domain.Server {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return domain.Server{Name: "t", Transport: domain.TransportStdio, Command: self}
}

func connect(t *testing.T, c Connector, s domain.Server, env map[string]string) *session {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	got, err := c.Connect(ctx, s, env)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = got.Close() })
	return got.(*session)
}

// TestAStdioServerServesItsTools_REQ_MCP_001: over stdio, the server's tools are listed with
// their schemas, a call returns its text, and a failure the tool reports is a result.
func TestAStdioServerServesItsTools_REQ_MCP_001(t *testing.T) {
	s := connect(t, New(Config{}), stdioServer(t), child)
	tools, err := s.Tools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
		if tool.Name == "echo" && tool.InputSchema["type"] != "object" {
			t.Fatalf("echo's schema: %v", tool.InputSchema)
		}
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "echo,env,exit,fail,sleep" {
		t.Fatalf("tools %v", names)
	}
	res, err := s.Call(t.Context(), "echo", json.RawMessage(`{"text":"hello"}`))
	if err != nil || res.Text != "hello" || res.IsError {
		t.Fatalf("echo = %+v %v", res, err)
	}
	if res, err := s.Call(t.Context(), "fail", json.RawMessage(`{}`)); err != nil || !res.IsError {
		t.Fatalf("fail = %+v %v", res, err)
	}
}

// TestAStdioServerGetsOnlyItsEnvironment_REQ_MCP_001: the process inherits PATH and the like
// and its resolved env_refs, and nothing else of the daemon's environment.
func TestAStdioServerGetsOnlyItsEnvironment_REQ_MCP_001(t *testing.T) {
	c := New(Config{Environ: func() []string {
		return []string{"PATH=" + os.Getenv("PATH"), "OPENAI_API_KEY=sk-daemon", sdktest.ChildEnv + "=1"}
	}})
	// The child marker is not inherited; it reaches the child as an env_ref would.
	s := connect(t, c, stdioServer(t), map[string]string{"GH_TOKEN": "ghp_x", sdktest.ChildEnv: "1"})
	for name, want := range map[string]string{"GH_TOKEN": "ghp_x", "OPENAI_API_KEY": "", "PATH": os.Getenv("PATH")} {
		res, err := s.Call(t.Context(), "env", json.RawMessage(`{"name":"`+name+`"}`))
		if err != nil || res.Text != want {
			t.Errorf("%s = %q %v, want %q", name, res.Text, err, want)
		}
	}
}

// TestACrashedServerEndsItsSession_REQ_MCP_003: when the process dies, Done closes and a call
// fails.
func TestACrashedServerEndsItsSession_REQ_MCP_003(t *testing.T) {
	s := connect(t, New(Config{}), stdioServer(t), child)
	_, _ = s.Call(t.Context(), "exit", json.RawMessage(`{}`))
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the session outlived its process")
	}
	if _, err := s.Call(t.Context(), "echo", json.RawMessage(`{"text":"x"}`)); err == nil {
		t.Fatal("a call to a dead server succeeded")
	}
}

type recorder struct {
	mu   sync.Mutex
	recs []llmdomain.EgressRecord
}

func (r *recorder) Record(_ context.Context, rec llmdomain.EgressRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recs = append(r.recs, rec)
	return nil
}

// TestAnHTTPServerServesItsTools_REQ_MCP_001: over streamable HTTP the same tools answer, and a
// loopback server is not egress.
func TestAnHTTPServerServesItsTools_REQ_MCP_001(t *testing.T) {
	srv := httptest.NewServer(sdktest.Handler())
	t.Cleanup(srv.Close)
	rec := &recorder{}
	s := connect(t, New(Config{Egress: rec}), domain.Server{Name: "h", Transport: domain.TransportHTTP, URL: srv.URL}, nil)
	res, err := s.Call(t.Context(), "echo", json.RawMessage(`{"text":"over http"}`))
	if err != nil || res.Text != "over http" {
		t.Fatalf("echo = %+v %v", res, err)
	}
	if len(rec.recs) != 0 {
		t.Fatalf("a loopback server was recorded as egress: %+v", rec.recs)
	}
}

// TestEveryRemoteRequestIsRecorded_REQ_MCP_001: Art. 4 — each request to a server off this
// machine is in egress_log before it is sent, under the thread it was made for, and one that
// cannot be recorded is not sent.
func TestEveryRemoteRequestIsRecorded_REQ_MCP_001(t *testing.T) {
	srv := httptest.NewServer(sdktest.Handler())
	t.Cleanup(srv.Close)
	rec := &recorder{}
	tr := egressTransport{log: rec, provider: "mcp:h", next: srv.Client().Transport}
	req := httptest.NewRequestWithContext(llmdomain.WithThread(t.Context(), "thr_1"), "POST", srv.URL, strings.NewReader(`{"jsonrpc":"2.0"}`))
	req.RequestURI = ""
	if resp, err := tr.RoundTrip(req); err == nil {
		_ = resp.Body.Close()
	}
	if len(rec.recs) != 1 || rec.recs[0].Provider != "mcp:h" || rec.recs[0].ThreadID != "thr_1" || rec.recs[0].Bytes != 17 {
		t.Fatalf("records %+v", rec.recs)
	}
	failing := egressTransport{log: failLog{}, provider: "mcp:h", next: srv.Client().Transport}
	req2 := httptest.NewRequestWithContext(t.Context(), "POST", srv.URL, strings.NewReader("x"))
	req2.RequestURI = ""
	resp2, err := failing.RoundTrip(req2)
	if err == nil {
		_ = resp2.Body.Close()
		t.Fatal("a request whose record failed was sent")
	}
}

type failLog struct{}

func (failLog) Record(context.Context, llmdomain.EgressRecord) error { return context.Canceled }

// TestARemoteServerGoesThroughTheEgressLog_REQ_MCP_001: the client for a server off this
// machine records every request; a loopback server's does not; and without the egress log a
// remote server is not connected at all.
func TestARemoteServerGoesThroughTheEgressLog_REQ_MCP_001(t *testing.T) {
	c := New(Config{Egress: &recorder{}})
	remote, err := c.httpClient(domain.Server{Name: "r", Transport: domain.TransportHTTP, URL: "https://mcp.example.com/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if tr, ok := remote.Transport.(egressTransport); !ok || tr.provider != "mcp:r" {
		t.Fatalf("a remote server's transport is %T", remote.Transport)
	}
	local, _ := c.httpClient(domain.Server{Name: "l", Transport: domain.TransportHTTP, URL: "http://127.0.0.1:9/mcp"})
	if _, ok := local.Transport.(egressTransport); ok {
		t.Fatal("a loopback server is recorded as egress")
	}
	if _, err := New(Config{}).httpClient(domain.Server{Name: "r", Transport: domain.TransportHTTP, URL: "https://x/mcp"}); err == nil {
		t.Fatal("a remote server was connected without the egress log")
	}
}

// TestARedirectToAnotherHostIsRefused_REQ_MCP_001: Art. 4 — a server's redirect to another host
// is not followed, so a loopback server cannot relay a call off the machine unrecorded.
func TestARedirectToAnotherHostIsRefused_REQ_MCP_001(t *testing.T) {
	target := httptest.NewServer(sdktest.Handler())
	t.Cleanup(target.Close)
	redirector := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusTemporaryRedirect))
	t.Cleanup(redirector.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if s, err := New(Config{}).Connect(ctx, domain.Server{Name: "r", Transport: domain.TransportHTTP, URL: redirector.URL}, nil); err == nil {
		_ = s.Close()
		t.Fatal("a redirect to another host was followed")
	}
}

// TestTruncateKeepsRunesWhole_REQ_MCP_001: a capped result never ends in half a rune.
func TestTruncateKeepsRunesWhole_REQ_MCP_001(t *testing.T) {
	got := truncate("aé", 2)
	if got != "a\n[truncated]" {
		t.Fatalf("truncate = %q", got)
	}
}
