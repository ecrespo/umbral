package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	remotedomain "github.com/ecrespo/umbral/internal/remote/domain"
)

// The relay (REQ-RMT-001…003, API §5.44). A client reaches a remote host's daemon through this
// one: each connection that addresses a host gets a connection of its own to that host's daemon,
// over the socket the remote module forwards, opened on first use with the host's token and the
// client's own kind. Requests go up with their ids' prefix taken off and their answers and the
// host's notifications come down prefixed. Nothing of the host's is decided or kept here — its
// agents, approvals and egress stay on that daemon (REQ-RMT-003); this side only checks what it
// checks for any request, the client's kind and whether it runs in an agent's terminal, before
// relaying.

// Relay timeouts: reaching the forwarded socket and the host's handshake.
const (
	relayDialTimeout  = 5 * time.Second
	relayHelloTimeout = 10 * time.Second
	// relayMaxLine bounds a line read from a host; the host's own frame limit is at most 64 MiB
	// (API §1), and what is written on is held to this connection's.
	relayMaxLine = 64 << 20
)

// relayReasonDisconnected is the `session.unsubscribed` reason when a host's link drops: the
// client subscribes again once `remote.state_changed` says the host is back (REQ-RMT-002).
const relayReasonDisconnected = "remote_disconnected"

// The two methods whose relayed answers change which of a host's sessions a link carries.
const (
	methodSessionSubscribe   = "session.subscribe"
	methodSessionUnsubscribe = "session.unsubscribe"
)

// namespaceSystem is the `system.*` methods' and notifications' namespace.
const namespaceSystem = "system"

// noRelay reports whether a method is always this daemon's: the handshake, the protocol
// document and the hosts themselves.
func noRelay(method string) bool {
	prefix, _, _ := strings.Cut(method, ".")
	return prefix == namespaceSystem || prefix == "api" || prefix == "remote"
}

// relayLink is one connection's connection to one host's daemon.
type relayLink struct {
	host  string
	conn  *conn
	nc    net.Conn
	write sync.Mutex

	mu      sync.Mutex
	nextID  uint64
	pending map[uint64]func(result json.RawMessage, werr *wireError)
	// subs are the host's sessions this connection subscribed to, by the host's ids.
	subs   map[string]struct{}
	closed bool
	done   chan struct{}
}

// relayResponse is a line from a host: an answer or a notification.
type relayResponse struct {
	ID     *uint64         `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *wireError      `json:"error"`
}

// openLink returns the connection's link to host when one is up.
func (c *conn) openLink(host string) *relayLink {
	c.relayMu.Lock()
	defer c.relayMu.Unlock()
	l, ok := c.relays[host]
	if !ok {
		return nil
	}
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed {
		delete(c.relays, host)
		return nil
	}
	return l
}

// relayTo returns the connection's link to host, opening it if need be. The dial and the
// handshake run outside relayMu, so a slow host holds up neither a closing connection nor this
// connection's other hosts' links; when two callers open one host's link at once, the first
// stored wins and the other is closed.
func (c *conn) relayTo(ctx context.Context, host string) (*relayLink, error) {
	if l := c.openLink(host); l != nil {
		return l, nil
	}
	link, ok := c.server.cfg.Remotes.Link(host)
	if !ok {
		return nil, fmt.Errorf("%w: %s", remotedomain.ErrUnavailable, host)
	}
	var d net.Dialer
	dialCtx, cancel := context.WithTimeout(ctx, relayDialTimeout)
	nc, err := d.DialContext(dialCtx, "unix", link.Socket)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", remotedomain.ErrUnavailable, host, err)
	}
	l := &relayLink{
		host: host, conn: c, nc: nc, nextID: 1,
		pending: map[uint64]func(json.RawMessage, *wireError){}, subs: map[string]struct{}{}, done: make(chan struct{}),
	}
	reader := bufio.NewReaderSize(nc, 64<<10)
	if err := l.hello(ctx, link.Token, reader); err != nil {
		_ = nc.Close()
		return nil, fmt.Errorf("%w: %s: %w", remotedomain.ErrUnavailable, host, err)
	}

	c.relayMu.Lock()
	select {
	case <-c.closed:
		// The connection closed while the link was being opened: nothing may outlive it.
		c.relayMu.Unlock()
		_ = nc.Close()
		return nil, fmt.Errorf("%w: %s: the connection closed", remotedomain.ErrUnavailable, host)
	default:
	}
	if other, ok := c.relays[host]; ok {
		other.mu.Lock()
		closed := other.closed
		other.mu.Unlock()
		if !closed {
			c.relayMu.Unlock()
			_ = nc.Close()
			return other, nil
		}
	}
	if c.relays == nil {
		c.relays = map[string]*relayLink{}
	}
	c.relays[host] = l
	c.relayMu.Unlock()
	go l.read(reader)
	return l, nil
}

// hello authenticates to the host as this connection's client kind, within the handshake's
// timeout and ctx, whichever ends first.
func (l *relayLink) hello(ctx context.Context, token string, r *bufio.Reader) error {
	line, err := json.Marshal(map[string]any{
		"jsonrpc": jsonrpcVersion, "id": 0, "method": methodHello,
		"params": helloParams{
			Token: token, ClientKind: string(l.conn.clientKind), ClientVersion: "umbrald-relay",
			ProtocolVersion: ProtocolVersion, Capabilities: l.conn.clientCapabilities(),
		},
	})
	if err != nil {
		return err
	}
	deadline := time.Now().Add(relayHelloTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := l.nc.SetDeadline(deadline); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = l.nc.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	if _, err := l.nc.Write(append(line, '\n')); err != nil {
		return err
	}
	answer, err := r.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("the host's handshake: %w", err)
	}
	var resp relayResponse
	if err := json.Unmarshal(answer, &resp); err != nil {
		return fmt.Errorf("the host's handshake: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("the host refused the handshake: %s", resp.Error.Message)
	}
	return l.nc.SetDeadline(time.Time{})
}

// call sends a request up; answer, when not nil, gets the host's reply. A nil answer sends a
// JSON-RPC notification. It returns the id the request went up with, 0 for a notification.
func (l *relayLink) call(method string, params json.RawMessage, answer func(json.RawMessage, *wireError)) (uint64, error) {
	msg := map[string]any{"jsonrpc": jsonrpcVersion, "method": method}
	if len(params) > 0 {
		msg["params"] = params
	}
	var id uint64
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return 0, fmt.Errorf("%w: %s", remotedomain.ErrUnavailable, l.host)
	}
	if answer != nil {
		id = l.nextID
		l.nextID++
		l.pending[id] = answer
		msg["id"] = id
	}
	l.mu.Unlock()
	line, err := json.Marshal(msg)
	if err != nil {
		l.forget(id)
		return 0, err
	}
	l.write.Lock()
	defer l.write.Unlock()
	if _, err := l.nc.Write(append(line, '\n')); err != nil {
		// The caller answers this request with the error; shut must not answer it too.
		l.forget(id)
		l.shut()
		return 0, fmt.Errorf("%w: %s: %w", remotedomain.ErrUnavailable, l.host, err)
	}
	return id, nil
}

// forget drops a call's answer, so nothing answers it: its caller already has, or gave up.
func (l *relayLink) forget(id uint64) {
	if id == 0 {
		return
	}
	l.mu.Lock()
	delete(l.pending, id)
	l.mu.Unlock()
}

// read delivers the host's lines until its connection ends, then ends the link.
func (l *relayLink) read(r *bufio.Reader) {
	defer l.shut()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), relayMaxLine)
	for sc.Scan() {
		var msg relayResponse
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			l.conn.logger.Debug("relay: an unreadable line from a host", slog.String("host", l.host))
			continue
		}
		if msg.Method != "" {
			l.notify(msg)
			continue
		}
		if msg.ID == nil {
			continue
		}
		l.mu.Lock()
		answer := l.pending[*msg.ID]
		delete(l.pending, *msg.ID)
		l.mu.Unlock()
		if answer != nil {
			answer(msg.Result, msg.Error)
		}
	}
}

// notify queues a host's notification for the client, prefixed and under this daemon's envelope
// counter. It goes through the connection's queue like this daemon's own, so §8's limit holds for
// a remote session too. A host's `remote.*` and `system.*` notifications are about that host's
// own links and connection, not this client's, and are dropped: relayed, a host's report about
// its own remote of some name would read as this daemon's host of that name.
func (l *relayLink) notify(msg relayResponse) {
	if !relayableNotification(msg.Method) {
		return
	}
	if msg.Method == "session.unsubscribed" {
		var p sessionUnsubscribedPayload
		if json.Unmarshal(msg.Params, &p) == nil {
			l.mu.Lock()
			delete(l.subs, p.SessionID)
			l.mu.Unlock()
		}
	}
	params, err := prefixIDs(msg.Params, l.host)
	if err != nil {
		return
	}
	l.conn.enqueue(msg.Method, params, l.conn.server.nextSeq())
}

// relayableNotification reports whether a host's notification is relayed to the client.
func relayableNotification(method string) bool {
	prefix, _, _ := strings.Cut(method, ".")
	return prefix != "remote" && prefix != namespaceSystem
}

// shut ends the link once: every call still waiting is answered as unavailable, and every
// session subscribed through it is announced unsubscribed, so the client knows to subscribe again
// when the host is back (REQ-RMT-002). Once the client's connection is closing nothing is written:
// shut then runs on the goroutine closing it, which may be the fan-out's, and must not wait on a
// write to a client that stopped reading (REQ-API-006).
func (l *relayLink) shut() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	pending := l.pending
	l.pending = nil
	subs := l.subs
	l.subs = nil
	l.mu.Unlock()
	_ = l.nc.Close()
	close(l.done)

	select {
	case <-l.conn.closed:
		return
	default:
	}
	unavailable := toWire(fmt.Errorf("%w: %s", remotedomain.ErrUnavailable, l.host), "")
	for _, answer := range pending {
		answer(nil, unavailable)
	}
	for id := range subs {
		l.conn.enqueue("session.unsubscribed", sessionUnsubscribedPayload{
			SessionID: l.host + "/" + id, Reason: relayReasonDisconnected,
		}, l.conn.server.nextSeq())
	}
	if len(subs) > 0 {
		go l.conn.redial(l.host)
	}
}

// redial follows a link that ended while the host's ssh link may still be up: the host's daemon
// restarted, handed its panes over (REQ-HND-001) or closed this relay. The remote module then
// announces nothing, so this connection is told itself: once a new link to the host holds, it gets
// `remote.state_changed` `connected`, which is what its client resubscribes on (REQ-RMT-002). When
// the ssh link is down too, the remote module's own announcements follow it and this gives up.
//
//nolint:contextcheck,nolintlint // contextcheck reads this itself; its life is the connection's, not the request's or the read's that ended the link
func (c *conn) redial(host string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-c.closed:
			cancel()
		case <-ctx.Done():
		}
	}()
	for attempt := 0; ; attempt++ {
		if _, ok := c.server.cfg.Remotes.Link(host); !ok {
			return
		}
		if _, err := c.relayTo(ctx, host); err == nil {
			c.enqueue(notificationRemoteStateChanged, remoteStatePayload{Name: host, State: string(remotedomain.StateConnected)}, c.server.nextSeq())
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(remotedomain.Backoff(attempt)):
		}
	}
}

// closeRelays ends every link of a closing connection. The connection's closed channel is closed
// first, so no link writes to it on the way out.
func (c *conn) closeRelays() {
	c.relayMu.Lock()
	links := c.relays
	c.relays = nil
	c.relayMu.Unlock()
	for _, l := range links {
		l.shut()
	}
}

// relay sends a request to host and writes the host's answer as this request's. It returns at
// once: the answer is written when it comes, from the link's reader, and JSON-RPC matches it by
// its id.
func (c *conn) relay(ctx context.Context, req request, host string, params json.RawMessage) {
	l, err := c.relayTo(ctx, host)
	if err != nil {
		c.reply(req, nil, err)
		return
	}
	if req.isNotification() {
		_, _ = l.call(req.Method, params, nil)
		return
	}
	var sessionID string
	if req.Method == methodSessionSubscribe || req.Method == methodSessionUnsubscribe {
		var p struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(params, &p)
		sessionID = p.SessionID
	}
	_, err = l.call(req.Method, params, func(result json.RawMessage, werr *wireError) {
		if werr != nil {
			c.writeRaw(response{JSONRPC: jsonrpcVersion, ID: req.ID, Error: werr, method: req.Method})
			return
		}
		if sessionID != "" {
			l.mu.Lock()
			if l.subs != nil {
				if req.Method == methodSessionSubscribe {
					l.subs[sessionID] = struct{}{}
				} else {
					delete(l.subs, sessionID)
				}
			}
			l.mu.Unlock()
		}
		out, err := prefixIDs(result, host)
		if err != nil {
			c.reply(req, nil, fmt.Errorf("relay: the host's answer: %w", err))
			return
		}
		c.writeRaw(response{JSONRPC: jsonrpcVersion, ID: req.ID, Result: out, method: req.Method})
	})
	if err != nil {
		c.reply(req, nil, err)
	}
}

// relayCall is a call to a host whose answer this daemon reads itself, for session.snapshot's
// merge. It waits for the answer, ctx or the link's end.
func (c *conn) relayCall(ctx context.Context, host, method string, params json.RawMessage) (json.RawMessage, error) {
	l, err := c.relayTo(ctx, host)
	if err != nil {
		return nil, err
	}
	type answer struct {
		result json.RawMessage
		werr   *wireError
	}
	got := make(chan answer, 1)
	id, err := l.call(method, params, func(r json.RawMessage, w *wireError) { got <- answer{r, w} })
	if err != nil {
		return nil, err
	}
	select {
	case a := <-got:
		if a.werr != nil {
			return nil, errors.New(a.werr.Message)
		}
		return a.result, nil
	case <-ctx.Done():
		l.forget(id)
		return nil, ctx.Err()
	}
}
