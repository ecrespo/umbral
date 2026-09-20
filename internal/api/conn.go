package api

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
)

// method is one dispatchable JSON-RPC method.
type method struct {
	// handle runs the method. It returns a domain error; the wire code is api's job.
	handle func(ctx context.Context, c *conn, params json.RawMessage) (any, error)
	// kinds restricts the method to certain client kinds (API Spec §2). Empty means
	// every kind may call it.
	kinds []ClientKind
	// beforeHello marks the handshake itself, the only method reachable unauthenticated.
	beforeHello bool
}

// allows reports whether a client of this kind may call the method. A method the caller
// is not allowed to use answers METHOD_NOT_FOUND rather than PERMISSION_DENIED, as the
// spec's error table says: the CLI has no business learning that session.create exists.
func (m method) allows(kind ClientKind) bool {
	if len(m.kinds) == 0 {
		return true
	}
	for _, k := range m.kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// conn is one client connection and its handshake state.
type conn struct {
	server  *Server
	netConn net.Conn
	reader  *bufio.Scanner
	logger  *slog.Logger

	// writeMu serialises writes: replies come from the read loop, notifications will
	// come from the bus goroutine in T-F0-06.
	writeMu sync.Mutex
	encoder *json.Encoder

	// authenticated and clientKind are written by system.hello and read afterwards.
	// Both stay on the read goroutine, so they need no lock.
	authenticated bool
	clientKind    ClientKind
	connectionID  string

	// subsMu guards subs, which the read goroutine writes and the dispatch goroutine
	// reads on every output chunk.
	subsMu sync.Mutex
	subs   map[string]*subscription

	closed    chan struct{}
	closeOnce sync.Once
}

// closedCh is closed when the connection goes away, which is how each subscription's
// writer learns to stop.
func (c *conn) closedCh() <-chan struct{} { return c.closed }

// subscribe starts streaming a session to this connection, replacing any existing
// subscription to the same session.
func (c *conn) subscribe(sessionID string, startSeq uint64) {
	c.subsMu.Lock()
	defer c.subsMu.Unlock()

	if existing, ok := c.subs[sessionID]; ok {
		existing.close()
	}
	c.subs[sessionID] = newSubscription(c, sessionID, startSeq)
}

// unsubscribe stops streaming a session. It reports whether there was one.
func (c *conn) unsubscribe(sessionID string) bool {
	c.subsMu.Lock()
	defer c.subsMu.Unlock()

	sub, ok := c.subs[sessionID]
	if !ok {
		return false
	}
	sub.close()
	delete(c.subs, sessionID)
	return true
}

// rebaseSubscription raises the floor of an existing subscription to the seq a snapshot is
// current as of, without replacing it. Replacing it would discard everything queued while
// the snapshot was being taken, which is exactly what subscribing first is meant to keep.
func (c *conn) rebaseSubscription(sessionID string, startSeq uint64) {
	c.subsMu.Lock()
	sub := c.subs[sessionID]
	c.subsMu.Unlock()

	if sub != nil {
		sub.rebase(startSeq)
	}
}

// deliver hands one output chunk to this connection's subscription, if it has one.
// deliver hands one chunk to this connection's subscription, if it has one.
//
// Two sequence numbers travel together here and they are not interchangeable: `seq` is the
// session's own, which the client uses to place bytes on a screen (§5.11), and `envelope` is
// the daemon-run counter §6 puts on every notification. A batch coalesces several chunks, so
// the notification it eventually emits carries the highest envelope number it contains.
func (c *conn) deliver(sessionID string, seq, envelope uint64, data []byte) {
	c.subsMu.Lock()
	sub := c.subs[sessionID]
	c.subsMu.Unlock()

	if sub != nil {
		sub.enqueue(seq, envelope, data)
	}
}

// closeSubscriptions stops every stream this connection holds.
func (c *conn) closeSubscriptions() {
	c.subsMu.Lock()
	subs := c.subs
	c.subs = make(map[string]*subscription)
	c.subsMu.Unlock()

	for _, sub := range subs {
		sub.close()
	}
}

func (s *Server) newConn(netConn net.Conn) *conn {
	scanner := bufio.NewScanner(netConn)
	// NDJSON with the 4 MiB ceiling of API Spec §1. Without this the scanner stops at
	// 64 KiB and a large paste would look like a protocol error.
	scanner.Buffer(make([]byte, 0, 64<<10), MaxMessageBytes)

	return &conn{
		server:  s,
		subs:    make(map[string]*subscription),
		closed:  make(chan struct{}),
		netConn: netConn,
		reader:  scanner,
		logger:  s.cfg.Logger,
		encoder: json.NewEncoder(netConn),
	}
}

// serve reads NDJSON messages until the peer disconnects or the daemon shuts down.
func (c *conn) serve(ctx context.Context) {
	defer func() { _ = c.close() }()

	for c.reader.Scan() {
		if ctx.Err() != nil {
			return
		}
		line := c.reader.Bytes()
		if len(line) == 0 {
			continue
		}
		if closeConn := c.handleLine(ctx, line); closeConn {
			return
		}
	}

	if err := c.reader.Err(); err != nil && !errors.Is(err, net.ErrClosed) {
		if errors.Is(err, bufio.ErrTooLong) {
			// The peer is out of contract; tell it why before hanging up.
			c.writeError(nil, ValidationError(
				fmt.Sprintf("message exceeds the %d byte limit", MaxMessageBytes)))
			return
		}
		c.logger.Debug("connection read ended", slog.Any("error", err))
	}
}

// handleLine processes one message and reports whether the connection must be closed.
func (c *conn) handleLine(ctx context.Context, line []byte) (closeConn bool) {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		c.writeRaw(response{
			JSONRPC: jsonrpcVersion,
			ID:      nullID,
			Error: &wireError{
				Code:    codeParseError,
				Message: "invalid JSON",
				Data:    &errorData{DomainCode: "PARSE_ERROR"},
			},
		})
		return false
	}

	if req.JSONRPC != jsonrpcVersion || req.Method == "" {
		c.writeRaw(response{
			JSONRPC: jsonrpcVersion,
			ID:      invalidRequestID(req.ID),
			Error: &wireError{
				Code:    codeInvalidRequest,
				Message: "not a valid JSON-RPC 2.0 request",
				Data:    &errorData{DomainCode: "INVALID_REQUEST"},
			},
		})
		return false
	}

	m, known := c.server.methods[req.Method]

	// API Spec §2 step 3: anything before a successful system.hello is UNAUTHORIZED and
	// the connection is closed. That check comes before "does this method exist", so an
	// unauthenticated peer cannot probe the method table.
	if !c.authenticated && (!known || !m.beforeHello) {
		c.reply(req, nil, fmt.Errorf("%w: the first call must be system.hello", ErrUnauthorized))
		return true
	}
	if !known || (c.authenticated && !m.allows(c.clientKind)) {
		c.reply(req, nil, fmt.Errorf("%w: %s", ErrMethodNotFound, req.Method))
		return false
	}

	result, err := m.handle(ctx, c, req.Params)
	// A failed handshake closes the connection, same rule as calling too early.
	if err != nil && errors.Is(err, ErrUnauthorized) {
		c.reply(req, nil, err)
		return true
	}
	c.reply(req, result, err)
	return false
}

// reply writes a response unless the request was a notification.
func (c *conn) reply(req request, result any, err error) {
	if req.isNotification() {
		if err != nil {
			c.logger.Debug("notification failed", slog.String("method", req.Method), slog.Any("error", err))
		}
		return
	}
	if err != nil {
		c.writeError(req.ID, err)
		return
	}
	c.writeRaw(response{JSONRPC: jsonrpcVersion, ID: req.ID, Result: result})
}

// nullID is the literal JSON null. JSON-RPC 2.0 §5 requires an explicit null id when the
// request's id could not be determined, which `omitempty` would otherwise drop.
var nullID = json.RawMessage("null")

func (c *conn) writeError(id json.RawMessage, err error) {
	// Until OTel tracing lands in T-F1-18 the daemon has no trace id, so the connection
	// id stands in: it correlates a client's error to the rest of that connection's log
	// lines. Before the handshake there is nothing to correlate to, and inventing an id
	// per error would correlate to nothing at all, so the field stays empty.
	wire := toWire(err, c.connectionID)
	if wire.Code == codeInternalError {
		c.logger.Error("internal error",
			slog.String("connection_id", c.connectionID), slog.Any("error", err))
	}
	if len(id) == 0 {
		id = nullID
	}
	c.writeRaw(response{JSONRPC: jsonrpcVersion, ID: id, Error: wire})
}

// writeRaw serialises one message onto the socket.
func (c *conn) writeRaw(msg any) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.encoder.Encode(msg); err != nil {
		c.logger.Debug("write failed", slog.Any("error", err))
	}
}

// notify sends a daemon-to-client notification (API Spec §6).
func (c *conn) notify(method string, params any, seq uint64) {
	c.writeRaw(notification{
		JSONRPC: jsonrpcVersion, Method: method, Seq: seq, Params: params,
	})
}

func (c *conn) close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.closed)
		c.closeSubscriptions()
		err = c.netConn.Close()
	})
	return err
}

// tokenMatches compares in constant time. A byte-by-byte comparison on a local socket is
// a timing oracle an unprivileged process on the same machine can actually exploit.
func tokenMatches(want, got string) bool {
	return subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}

// invalidRequestID keeps the client's id when it sent one and answers with an explicit
// null when it did not (JSON-RPC 2.0 §5).
func invalidRequestID(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return nullID
	}
	return id
}
