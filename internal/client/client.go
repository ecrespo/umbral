// Package client dials the daemon's Unix socket and speaks the JSON-RPC 2.0 protocol of
// `specs/api/umbral-daemon-api-v1.md`. It is what `umb` and, from T-F0-12, the TUI use;
// neither of them knows the wire format.
//
// It cannot import internal/api, which owns the server side of that protocol: the §5.2
// row for `client` in the Technical Design says `config` only, never `api`, because the
// client and the server of one protocol must not depend on each other. The overlap is
// small and deliberate — the method names, the error codes and the handshake are the
// contract, and a contract with two implementations is the point of having written it
// down.
package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ecrespo/umbral/internal/config"
)

// ProtocolVersion is the only version this client speaks. The daemon requires the field
// and rejects a handshake without it (API Spec §2), so it is never omitted.
const ProtocolVersion = 1

// Version is what this client reports in its handshake. `cmd/umb` overwrites it with the
// value the linker stamped into its own `main.version`, because `-X` can only reach the
// main package and the daemon logs what its clients claim to be.
var Version = "0.0.0-dev"

// ClientKindCLI is what `umb` announces. The daemon restricts a `cli` connection to
// `system.*`, `block.*`, `thread.create|send|cancel` and `model.list` (API Spec §2), so a
// method outside that set comes back as METHOD_NOT_FOUND rather than being served.
const ClientKindCLI = "cli"

// maxMessageBytes is the 4 MiB frame limit of API Spec §1 and §8. The reader enforces it
// so a daemon that went wrong cannot make the client allocate without bound.
const maxMessageBytes = 4 << 20

// DialTimeout caps a whole connection attempt, the handshake included. It is short because
// the socket is local: anything slower than this is a daemon that is not answering, and
// the caller has an autostart path for that case.
//
// It covers the handshake and not only the connect, because a daemon that accepts and then
// never replies would otherwise block forever — `cmd/umb` passes a context with no deadline
// of its own, so there would be nothing to stop it.
const DialTimeout = 2 * time.Second

// Client is one connection to the daemon. It is safe for concurrent use: calls are
// serialised, because the transport is a single NDJSON stream with no interleaving.
//
// Notifications are not delivered here. `umb` is request/response only, and a client that
// wants the stream (T-F0-12) needs a different shape — a reader goroutine and a channel —
// which is not built until something needs it.
type Client struct {
	conn net.Conn

	mu     sync.Mutex
	enc    *json.Encoder
	rd     *bufio.Reader
	nextID int64

	// ConnectionID is what the daemon called this connection in its handshake reply. It
	// is the same value the daemon puts in `trace_id` until tracing lands (T-F1-18), so
	// it is worth keeping for an error report.
	ConnectionID string

	// Capabilities are the namespaces the daemon serves, derived by it from its method
	// table (API Spec §2). A client that branches on a feature should ask this rather
	// than the daemon version.
	Capabilities []string

	// DaemonVersion is what the daemon reported in the handshake.
	DaemonVersion string
}

// Error is a JSON-RPC error the daemon returned. `DomainCode` is the stable name from the
// API Spec error table; `Code` is the numeric JSON-RPC code beside it. Callers should
// match on the domain code, because that is what the spec versions.
type Error struct {
	Code       int          `json:"code"`
	Message    string       `json:"message"`
	DomainCode string       `json:"domain_code"`
	Details    []ErrorField `json:"details,omitempty"`
	TraceID    string       `json:"trace_id,omitempty"`
}

// ErrorField is one entry of an error's `details` array.
type ErrorField struct {
	Field string `json:"field"`
	Issue string `json:"issue"`
}

func (e *Error) Error() string {
	var b strings.Builder
	if e.DomainCode != "" {
		b.WriteString(e.DomainCode)
		b.WriteString(": ")
	}
	b.WriteString(e.Message)
	for _, d := range e.Details {
		fmt.Fprintf(&b, " (%s: %s)", d.Field, d.Issue)
	}
	return b.String()
}

// DomainCode reports the daemon's domain code for err, or "" when err did not come from
// the daemon. It saves every caller an errors.As dance.
func DomainCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.DomainCode
	}
	return ""
}

// ErrNoSocket reports that the socket is not there or nothing is listening on it. The
// autostart path in Autostart turns this into an attempt to launch the daemon; every
// other error is reported as it is, because starting a second daemon will not fix a
// permission problem or a corrupt token.
var ErrNoSocket = errors.New("client: the daemon is not listening")

// DefaultSocketPath is where a client looks when nothing else is configured. It is the
// same answer the daemon gets, which is why it lives in config.
func DefaultSocketPath() (string, error) { return config.DefaultSocketPath() }

// Dial connects to the socket and completes the handshake, which is the only call the
// daemon accepts first (REQ-SEC-003).
//
// The token is read from the file beside the socket rather than passed in, because that
// is the only place it exists: the daemon writes it there at 0600 on first run.
func Dial(ctx context.Context, socketPath string) (*Client, error) {
	token, err := readToken(filepath.Join(filepath.Dir(socketPath), config.TokenFileName))
	if err != nil {
		return nil, err
	}
	return DialWithToken(ctx, socketPath, token)
}

// DialWithToken is Dial with the token supplied, for a caller that already has it and for
// the tests that need a wrong one.
func DialWithToken(ctx context.Context, socketPath, token string) (*Client, error) {
	var d net.Dialer
	dialCtx, cancel := context.WithTimeout(ctx, DialTimeout)
	defer cancel()

	conn, err := d.DialContext(dialCtx, "unix", socketPath)
	if err != nil {
		// A missing socket and a socket nobody is listening on are the same situation
		// for the caller: the daemon is down. Both are worth an autostart; nothing else
		// is.
		if isNotListening(err) {
			return nil, fmt.Errorf("%w: %s", ErrNoSocket, socketPath)
		}
		return nil, fmt.Errorf("client: dial %s: %w", socketPath, err)
	}

	c := &Client{
		conn: conn,
		enc:  json.NewEncoder(conn),
		rd:   bufio.NewReaderSize(conn, 64<<10),
	}
	// dialCtx, not ctx: the handshake shares the connection attempt's budget. A daemon
	// that accepts and never answers is the case this closes.
	if err := c.hello(dialCtx, token); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// Close releases the connection. Calling it twice is not an error.
//
// It takes the same lock Call does, because the type promises to be safe for concurrent
// use and `conn` is the field both touch: a Close racing a Call would otherwise be a data
// race on the pointer, not merely a call that fails.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// Call invokes one method and decodes the result into out, which may be nil when the
// caller does not care about the body. params may be nil.
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.call(ctx, method, params, out)
}

func (c *Client) hello(ctx context.Context, token string) error {
	var result struct {
		DaemonVersion   string   `json:"daemon_version"`
		ProtocolVersion int      `json:"protocol_version"`
		Capabilities    []string `json:"capabilities"`
		ConnectionID    string   `json:"connection_id"`
	}
	params := map[string]any{
		"token":            token,
		"client_kind":      ClientKindCLI,
		"client_version":   Version,
		"protocol_version": ProtocolVersion,
	}
	// No lock: nothing else can hold this client yet.
	if err := c.call(ctx, "system.hello", params, &result); err != nil {
		return err
	}
	c.DaemonVersion = result.DaemonVersion
	c.Capabilities = result.Capabilities
	c.ConnectionID = result.ConnectionID
	return nil
}

// rpcResponse is one frame from the daemon. `Result` stays raw so the caller's own type
// decodes it, and so that an unknown field in a newer daemon is ignored rather than fatal
// (API Spec §9: additive changes do not bump the protocol version).
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *wireError      `json:"error"`
	Method  string          `json:"method"`
}

type wireError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		DomainCode string       `json:"domain_code"`
		Details    []ErrorField `json:"details"`
		TraceID    string       `json:"trace_id"`
	} `json:"data"`
}

func (c *Client) call(ctx context.Context, method string, params, out any) error {
	if c.conn == nil {
		return errors.New("client: the connection is closed")
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := c.conn.SetDeadline(deadline); err != nil {
			return fmt.Errorf("client: set the deadline: %w", err)
		}
		defer func() { _ = c.conn.SetDeadline(time.Time{}) }()
	}

	// A deadline does not cover cancellation, and `umb` cancels on SIGINT. Without this
	// the reader below would sit on a socket nobody is going to write to, and Ctrl-C
	// would do nothing — worse than the default, because signal.NotifyContext has already
	// disarmed the signal's own termination. Unblocking the read means expiring the
	// deadline, which is what makes the failure visible to the caller.
	//
	// The callback closes over the connection rather than reading c.conn: stop() does not
	// wait for a callback that has already started, so a Close racing this one would
	// otherwise be a write to c.conn against this read. Calling SetDeadline on a closed
	// connection simply fails, which is the right outcome here.
	conn := c.conn
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	c.nextID++
	id := c.nextID
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	if err := c.enc.Encode(req); err != nil {
		return fmt.Errorf("client: send %s: %w", method, err)
	}

	// The daemon may emit notifications on this connection even though `umb` never
	// subscribes to anything, so the reply is the next frame carrying this id rather
	// than simply the next frame.
	for {
		resp, err := c.readFrame()
		if err != nil {
			// A cancelled context is the interesting cause here, and reporting the
			// i/o timeout the close produced would hide it.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return fmt.Errorf("client: %s: %w", method, ctxErr)
			}
			return fmt.Errorf("client: read the reply to %s: %w", method, err)
		}
		if resp.ID == nil || *resp.ID != id {
			continue
		}
		if resp.Error != nil {
			return &Error{
				Code:       resp.Error.Code,
				Message:    resp.Error.Message,
				DomainCode: resp.Error.Data.DomainCode,
				Details:    resp.Error.Data.Details,
				TraceID:    resp.Error.Data.TraceID,
			}
		}
		if out == nil || len(resp.Result) == 0 {
			return nil
		}
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return fmt.Errorf("client: decode the result of %s: %w", method, err)
		}
		return nil
	}
}

// readFrame reads one NDJSON message, refusing anything past the 4 MiB frame limit of
// API Spec §2 instead of growing a buffer to match whatever arrived.
func (c *Client) readFrame() (*rpcResponse, error) {
	line, err := readLimitedLine(c.rd, maxMessageBytes)
	if err != nil {
		return nil, err
	}
	var resp rpcResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("the daemon sent something that is not JSON-RPC: %w", err)
	}
	return &resp, nil
}

func readLimitedLine(rd *bufio.Reader, limit int) ([]byte, error) {
	var out []byte
	for {
		chunk, more, err := rd.ReadLine()
		if err != nil {
			return nil, err
		}
		if len(out)+len(chunk) > limit {
			return nil, fmt.Errorf("a frame exceeded the %d byte limit of API Spec §8", limit)
		}
		out = append(out, chunk...)
		if !more {
			return out, nil
		}
	}
}

// readToken reads the per-installation token. A missing token file means the daemon has
// never run here, which is the same situation as a missing socket and deserves the same
// answer, so that `umb status` on a fresh machine autostarts instead of complaining about
// a file the user has never heard of.
func readToken(path string) (string, error) {
	//nolint:gosec // path is derived from the socket path, which comes from a flag or from config
	raw, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		return "", fmt.Errorf("%w: no token at %s", ErrNoSocket, path)
	case err != nil:
		return "", fmt.Errorf("client: read the token at %s: %w", path, err)
	case len(raw) == 0:
		return "", fmt.Errorf("%w: the token at %s is empty", ErrNoSocket, path)
	}
	return strings.TrimSpace(string(raw)), nil
}

// isNotListening reports whether the dial failed because nothing is there, as opposed to
// because something is wrong. ENOENT is a socket file that does not exist and ECONNREFUSED
// is a stale one left by a daemon that died; a permission error is neither, and starting
// another daemon would not fix it.
//
// Both are matched on the errno rather than on the text Go prints for it: the text is not
// part of any contract, and matching it would leave the autostart one error-wrapping
// change away from silently never firing.
func isNotListening(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED)
}
