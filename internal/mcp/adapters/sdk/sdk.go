// Package sdk is the mcp module's Connector over the official SDK
// (github.com/modelcontextprotocol/go-sdk): a stdio server is a child process, an http server
// a streamable-HTTP endpoint. A request to an http server off this machine is recorded in
// egress_log before it is sent, failing closed (Art. 4).
package sdk

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	llmdomain "github.com/ecrespo/umbral/internal/llmgw/domain"
	"github.com/ecrespo/umbral/internal/mcp/domain"
	"github.com/ecrespo/umbral/internal/mcp/ports"
)

// Config configures the connector.
type Config struct {
	// Version is the daemon's, sent as the client's implementation version.
	Version string
	// Egress records each request to an http server off this machine; required for one.
	Egress ports.EgressLog
	// Environ is the daemon's environment, from which a stdio server inherits only the
	// variables in Inherited; nil is os.Environ.
	Environ func() []string
	// TerminateAfter is how long Close waits for a stdio server to exit after its stdin
	// closes before signalling it; zero is two seconds.
	TerminateAfter time.Duration
}

// Inherited are the variables a stdio server inherits from the daemon: what a program needs
// to find itself and its files. Everything else — a provider key the env fallback put in the
// daemon's environment included — reaches it only through its env_refs.
var Inherited = []string{
	"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "LC_CTYPE", "TMPDIR", "TZ",
	"XDG_RUNTIME_DIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME",
}

// Connector implements ports.Connector.
type Connector struct {
	cfg Config
}

var _ ports.Connector = Connector{}

// New builds a connector.
func New(cfg Config) Connector {
	if cfg.Environ == nil {
		cfg.Environ = os.Environ
	}
	if cfg.TerminateAfter <= 0 {
		cfg.TerminateAfter = 2 * time.Second
	}
	return Connector{cfg: cfg}
}

// Connect implements ports.Connector.
func (c Connector) Connect(ctx context.Context, s domain.Server, env map[string]string) (ports.Session, error) {
	var transport mcp.Transport
	switch s.Transport {
	case domain.TransportStdio:
		// Not CommandContext: the process outlives the connection's context, and its end is
		// the transport's Close — stdin closed, then SIGTERM.
		cmd := exec.Command(s.Command, s.Args...) //nolint:gosec,noctx // the command the user configured with mcp.server.add
		cmd.Env = c.environment(env)
		if home, err := os.UserHomeDir(); err == nil {
			cmd.Dir = home
		}
		// A server's stderr is its own log; it is not kept (Art. 7: no tool output in logs).
		cmd.Stderr = io.Discard
		transport = &mcp.CommandTransport{Command: cmd, TerminateDuration: c.cfg.TerminateAfter}
	case domain.TransportHTTP:
		client, err := c.httpClient(s)
		if err != nil {
			return nil, err
		}
		// Reconnection is the manager's (REQ-MCP-003), not the SDK's.
		transport = &mcp.StreamableClientTransport{Endpoint: s.URL, HTTPClient: client, MaxRetries: -1}
	default:
		return nil, fmt.Errorf("unknown transport %q", s.Transport)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "umbral", Version: c.cfg.Version}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, err
	}
	sess := &session{cs: cs, done: make(chan struct{})}
	go func() {
		_ = cs.Wait()
		close(sess.done)
	}()
	return sess, nil
}

// httpClient is an http server's client: one that records every request in egress_log first
// when the server is off this machine (Art. 4).
func (c Connector) httpClient(s domain.Server) (*http.Client, error) {
	// A redirect is followed to the same host only: a loopback server redirecting a call to
	// another machine would send it with no egress row and no redaction (Art. 4).
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Host != via[0].URL.Host {
			return fmt.Errorf("refused a redirect from %s to another host, %s", via[0].URL.Host, req.URL.Host)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}}
	if domain.Remote(s) {
		if c.cfg.Egress == nil {
			return nil, errors.New("an http server off this machine needs the egress log")
		}
		client.Transport = egressTransport{log: c.cfg.Egress, provider: "mcp:" + s.Name, next: http.DefaultTransport}
	}
	return client, nil
}

// environment is a stdio server's: the inherited variables, then its resolved env_refs.
func (c Connector) environment(refs map[string]string) []string {
	keep := map[string]bool{}
	for _, name := range Inherited {
		keep[name] = true
	}
	var out []string
	for _, kv := range c.cfg.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if keep[name] {
			if _, overridden := refs[name]; !overridden {
				out = append(out, kv)
			}
		}
	}
	for name, value := range refs {
		out = append(out, name+"="+value)
	}
	return out
}

// session implements ports.Session over an SDK client session.
type session struct {
	cs   *mcp.ClientSession
	done chan struct{}
}

func (s *session) Done() <-chan struct{} { return s.done }

func (s *session) Close() error { return s.cs.Close() }

// Tools lists every tool, following the server's pages.
func (s *session) Tools(ctx context.Context) ([]domain.ToolInfo, error) {
	var out []domain.ToolInfo
	for t, err := range s.cs.Tools(ctx, nil) {
		if err != nil {
			return nil, err
		}
		schema := map[string]any{}
		if t.InputSchema != nil {
			raw, err := json.Marshal(t.InputSchema)
			if err != nil {
				return nil, fmt.Errorf("tool %s: input schema: %w", t.Name, err)
			}
			if err := json.Unmarshal(raw, &schema); err != nil {
				schema = nil
			}
		}
		out = append(out, domain.ToolInfo{Name: t.Name, Description: t.Description, InputSchema: schema})
	}
	return out, nil
}

// Call runs one tool. A protocol error is an error; a tool that reports its own failure
// (`isError`) is a result the model reads.
func (s *session) Call(ctx context.Context, tool string, args json.RawMessage) (domain.CallResult, error) {
	var arguments map[string]any
	if len(bytes.TrimSpace(args)) > 0 {
		if err := json.Unmarshal(args, &arguments); err != nil {
			return domain.CallResult{}, fmt.Errorf("arguments: %w", err)
		}
	}
	res, err := s.cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: arguments})
	if err != nil {
		return domain.CallResult{}, err
	}
	return domain.CallResult{Text: text(res), IsError: res.IsError}, nil
}

// text is what a result says, as the model reads it: its text blocks, a placeholder for any
// other content, or its structured content when it has no text; at most MaxResultBytes.
func text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		switch v := c.(type) {
		case *mcp.TextContent:
			b.WriteString(v.Text)
		case *mcp.ImageContent:
			fmt.Fprintf(&b, "[image %s, %d bytes]", v.MIMEType, len(v.Data))
		case *mcp.AudioContent:
			fmt.Fprintf(&b, "[audio %s, %d bytes]", v.MIMEType, len(v.Data))
		case *mcp.ResourceLink:
			fmt.Fprintf(&b, "[resource %s]", v.URI)
		case *mcp.EmbeddedResource:
			if v.Resource != nil && v.Resource.Text != "" {
				b.WriteString(v.Resource.Text)
			} else if v.Resource != nil {
				fmt.Fprintf(&b, "[resource %s]", v.Resource.URI)
			}
		default:
			b.WriteString("[content]")
		}
	}
	if b.Len() == 0 && res.StructuredContent != nil {
		if raw, err := json.Marshal(res.StructuredContent); err == nil {
			b.Write(raw)
		}
	}
	return truncate(b.String(), domain.MaxResultBytes)
}

// truncate cuts s to at most max bytes at a rune boundary, and says so.
func truncate(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n[truncated]"
}

// egressTransport records each request in egress_log before sending it, and refuses it when
// the record cannot be written (Art. 4). The row hashes the body and counts its bytes.
type egressTransport struct {
	log      ports.EgressLog
	provider string
	next     http.RoundTripper
}

func (t egressTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		var err error
		if body, err = io.ReadAll(req.Body); err != nil {
			return nil, err
		}
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	payload := body
	if len(payload) == 0 {
		payload = []byte(req.URL.String())
	}
	sum := sha256.Sum256(payload)
	if err := t.log.Record(req.Context(), llmdomain.EgressRecord{
		ThreadID: llmdomain.ThreadOf(req.Context()), Provider: t.provider, Host: req.URL.Hostname(),
		Bytes: int64(len(payload)), PayloadSHA256: hex.EncodeToString(sum[:]), CreatedAt: time.Now().UnixMilli(),
	}); err != nil {
		return nil, fmt.Errorf("the request could not be recorded in egress_log: %w", err)
	}
	return t.next.RoundTrip(req)
}
