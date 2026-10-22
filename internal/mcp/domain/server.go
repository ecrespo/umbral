// Package domain is the domain layer of the mcp module, which connects the MCP servers the
// user configured and hands their tools to the tool registry (REQ-MCP-001…004). It imports
// nothing outside the standard library and other modules' domain (Art. 3).
package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Errors the module returns; internal/api maps them onto API §3.
var (
	ErrValidation    = errors.New("mcp: invalid request")
	ErrConfigInvalid = errors.New("mcp: invalid configuration")
	ErrNotFound      = errors.New("mcp: no such server")
	ErrConflict      = errors.New("mcp: a server already has this name")
	// ErrUnavailable is a call to a server that is not connected (REQ-MCP-003).
	ErrUnavailable = errors.New("mcp: the server is unavailable")
	// ErrRefused is a call the daemon would not send: its arguments carry what looks like a
	// secret and the server is off this machine (Art. 4).
	ErrRefused = errors.New("mcp: the call was not sent")
)

// Transport is how a server is reached (`mcp_servers.transport`).
type Transport string

// Transports.
const (
	TransportStdio Transport = "stdio"
	TransportHTTP  Transport = "http"
)

// Trust says whether a server's results are trusted content (REQ-SEC-006).
type Trust string

// Trust levels.
const (
	TrustTrusted   Trust = "trusted"
	TrustUntrusted Trust = "untrusted"
)

// State is a server's connection state (API §4 McpServer).
type State string

// States.
const (
	StateConnecting  State = "connecting"
	StateConnected   State = "connected"
	StateUnavailable State = "unavailable"
	StateDisabled    State = "disabled"
)

// Server is one configured MCP server (`mcp_servers`, API §4 McpServer). Tools are the names
// the server gave, unprefixed, as the API lists them.
type Server struct {
	ID        string
	Name      string
	Transport Transport
	Command   string
	Args      []string
	URL       string
	// EnvRefs maps an environment variable of a stdio server's process to a reference:
	// `keyring:<path>` or `env:<VAR>`, never a value (REQ-SEC-004).
	EnvRefs   map[string]string
	Trust     Trust
	State     State
	LastError string
	Tools     []string
	CreatedAt int64
	UpdatedAt int64
}

// Bounds.
const (
	// MaxNameLen keeps a prefixed tool name within the registry's 64 characters for tools of
	// reasonable length. A name has no `_` or `-`, so `mcp_<server>_` is a prefix of one
	// server's tools only: a rule `mcp_github_*` cannot reach a server `github_enterprise`.
	MaxNameLen = 32
	// CallTimeout is how long a connection or a call may take before the server is treated as
	// not responding (REQ-MCP-003).
	CallTimeout = 10 * time.Second
	// MaxReconnects is how many times a lost server is reconnected before it stays
	// unavailable (REQ-MCP-003).
	MaxReconnects = 5
	// MaxDescription caps the description a server gives a tool: it is the server's text in
	// the model's prompt.
	MaxDescription = 1024
	// MaxResultBytes caps what a call returns to the model, as read_file's 256 KiB does.
	MaxResultBytes = 256 << 10
)

// Backoff is the wait before reconnection attempt n (0-based): base, doubled each time.
func Backoff(base time.Duration, attempt int) time.Duration {
	return base << attempt
}

var (
	serverName = regexp.MustCompile(`^[a-z0-9]{1,32}$`)
	envName    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	unsafeRune = regexp.MustCompile(`[^a-z0-9_]`)
	toolName   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

// ToolName is the name a server's tool reaches the model and the policy rules with:
// `mcp_<server>_<tool>`, lower case, every other character folded to `_` (REQ-MCP-001). It
// reports false for a name that does not fit the registry's 64 characters, which is refused
// rather than truncated, so two tools never collapse into one.
func ToolName(server, tool string) (string, bool) {
	if tool == "" {
		return "", false
	}
	name := unsafeRune.ReplaceAllString(strings.ToLower("mcp_"+server+"_"+tool), "_")
	if !toolName.MatchString(name) {
		return "", false
	}
	return name, true
}

// RefKind is the store a reference names.
type RefKind string

// Reference kinds.
const (
	RefKeyring RefKind = "keyring"
	RefEnv     RefKind = "env"
)

// Ref is a parsed secret reference.
type Ref struct {
	Kind RefKind
	Name string
}

// ParseRef parses `keyring:<path>` or `env:<VAR>`. Anything else — a value written in the
// clear included — is ErrConfigInvalid (REQ-SEC-004); the error never repeats the value.
func ParseRef(raw string) (Ref, error) {
	kind, name, ok := strings.Cut(raw, ":")
	switch {
	case ok && kind == string(RefKeyring) && strings.TrimSpace(name) != "":
		return Ref{Kind: RefKeyring, Name: name}, nil
	case ok && kind == string(RefEnv) && envName.MatchString(name):
		return Ref{Kind: RefEnv, Name: name}, nil
	default:
		return Ref{}, fmt.Errorf("%w: a reference must be keyring:<path> or env:<VAR>, never a value", ErrConfigInvalid)
	}
}

// AddParams is mcp.server.add's input (API §5.27).
type AddParams struct {
	Name      string
	Transport Transport
	Command   string
	Args      []string
	URL       string
	EnvRefs   map[string]string
	Trust     Trust
}

// Validate checks the request and fills the default trust, `untrusted` (REQ-MCP-004).
func (p AddParams) Validate() (AddParams, error) {
	bad := func(format string, a ...any) (AddParams, error) {
		return AddParams{}, fmt.Errorf("%w: "+format, append([]any{ErrValidation}, a...)...)
	}
	if !serverName.MatchString(p.Name) {
		return bad("name must be 1-%d characters of a-z and 0-9", MaxNameLen)
	}
	switch p.Trust {
	case "":
		p.Trust = TrustUntrusted
	case TrustTrusted, TrustUntrusted:
	default:
		return bad("trust must be trusted or untrusted")
	}
	switch p.Transport {
	case TransportStdio:
		if strings.TrimSpace(p.Command) == "" {
			return bad("a stdio server needs a command")
		}
		if p.URL != "" {
			return bad("a stdio server takes no url")
		}
	case TransportHTTP:
		u, err := url.Parse(p.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return bad("an http server needs an http or https url")
		}
		if u.User != nil {
			return AddParams{}, fmt.Errorf("%w: the url carries credentials; a server's url is stored in clear (REQ-SEC-004)", ErrConfigInvalid)
		}
		if p.Command != "" || len(p.Args) > 0 {
			return bad("an http server takes no command or args")
		}
		if len(p.EnvRefs) > 0 {
			return bad("env_refs set a stdio server's environment; an http server takes none")
		}
	default:
		return bad("transport must be stdio or http")
	}
	for name, ref := range p.EnvRefs {
		if !envName.MatchString(name) {
			return bad("env_refs: %q is not an environment variable name", name)
		}
		if _, err := ParseRef(ref); err != nil {
			return AddParams{}, fmt.Errorf("%w (env_refs.%s)", err, name)
		}
	}
	return p, nil
}

// Remote reports whether a server is reached over the network off this machine (Art. 4): an
// http server whose host is neither localhost nor a loopback address. A stdio server is a
// process on this machine.
func Remote(s Server) bool {
	if s.Transport != TransportHTTP {
		return false
	}
	u, err := url.Parse(s.URL)
	if err != nil {
		return true
	}
	host := u.Hostname()
	if host == "localhost" {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

// ToolInfo is a tool as a server lists it.
type ToolInfo struct {
	Name        string
	Description string
	InputSchema map[string]any
}

// CallResult is what a call returned: its text, and whether the server reported the call as
// failed (`isError`).
type CallResult struct {
	Text    string
	IsError bool
}

// Compact is the call's arguments as one line of JSON, the target the policy rules match.
func Compact(args json.RawMessage) string {
	var b bytes.Buffer
	if err := json.Compact(&b, args); err != nil {
		return string(args)
	}
	return b.String()
}
