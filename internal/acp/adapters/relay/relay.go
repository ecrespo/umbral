// Package relay is a hosted thread's model relay (REQ-SBX-009, delta `2026-11-model-relay`):
// an HTTP listener on a loopback port that forwards the agent's model requests to the one
// endpoint its thread is configured for. The agent holds a token of the relay's own, never the
// provider's key, which the relay adds as it forwards; a request to an endpoint off the
// machine is an `egress_log` row before it leaves, and one that cannot be recorded does not
// leave.
package relay

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ecrespo/umbral/internal/acp/domain"
	"github.com/ecrespo/umbral/internal/acp/ports"
)

// DefaultMaxBody is the largest request body a relay forwards when Config.MaxBody is zero. A
// model request carries the conversation, attachments included; the daemon's own frame limit
// is far below it.
const DefaultMaxBody = 32 << 20

// DefaultMaxConns and DefaultMaxInFlight bound what one relay holds in the daemon: the
// connections it serves at once — a local process without the token can open them too — and
// the requests it forwards at once, each with its body read whole.
const (
	DefaultMaxConns    = 32
	DefaultMaxInFlight = 8
)

// Timeouts for a connection: its headers, a request read whole and an idle keep-alive. None
// bounds the answer, which streams for as long as the model writes.
const (
	headerTimeout = 10 * time.Second
	readTimeout   = 2 * time.Minute
	idleTimeout   = 30 * time.Second
	maxHeaders    = 64 << 10
)

// Config is one relay: its thread, its endpoint and how it reaches the key and the log.
type Config struct {
	ThreadID string
	Upstream domain.Upstream
	Keys     ports.Keys
	Egress   ports.EgressLog
	// Port is the loopback port to listen on; 0 lets the kernel choose.
	Port uint16
	// MaxBody bounds a request's body; zero is DefaultMaxBody.
	MaxBody int64
	// MaxConns bounds the connections served at once; zero is DefaultMaxConns.
	MaxConns int
	// MaxInFlight bounds the requests being forwarded at once; zero is DefaultMaxInFlight.
	MaxInFlight int
	// Transport reaches the endpoint; nil is a clone of http.DefaultTransport.
	Transport http.RoundTripper
	Now       func() time.Time
}

// Relay is a running relay.
type Relay struct {
	cfg      Config
	target   *url.URL
	basePath string
	token    string
	ln       net.Listener
	srv      *http.Server
	proxy    *httputil.ReverseProxy
	inFlight chan struct{}
}

// crossing are the only headers of the agent's that reach the endpoint, in the order a
// recorded request lists them: what a model request needs, and OpenRouter's two attribution
// headers. Anything else — a credential, a cookie, a header made up to carry data — stays
// behind.
var crossing = []string{"Content-Type", "Accept", "User-Agent", "OpenAI-Beta", "HTTP-Referer", "X-Title"}

type outCtx struct{}

// outgoing is what ServeHTTP decided the endpoint gets besides the body: the key, and the
// agent's headers that cross.
type outgoing struct {
	key    string
	header http.Header
}

// Start validates cfg and serves the relay on 127.0.0.1.
func Start(cfg Config) (*Relay, error) {
	switch {
	case cfg.ThreadID == "":
		return nil, errors.New("relay: no thread")
	case cfg.Keys == nil:
		return nil, errors.New("relay: no key source")
	case cfg.Egress == nil:
		return nil, errors.New("relay: no egress log")
	}
	target, err := endpoint(cfg.Upstream.BaseURL)
	if err != nil {
		return nil, err
	}
	if cfg.MaxBody <= 0 {
		cfg.MaxBody = DefaultMaxBody
	}
	if cfg.MaxConns <= 0 {
		cfg.MaxConns = DefaultMaxConns
	}
	if cfg.MaxInFlight <= 0 {
		cfg.MaxInFlight = DefaultMaxInFlight
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	transport := cfg.Transport
	if transport == nil {
		transport = http.DefaultTransport.(*http.Transport).Clone()
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, fmt.Errorf("relay: token: %w", err)
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(cfg.Port))))
	if err != nil {
		return nil, fmt.Errorf("relay: %w", err)
	}
	r := &Relay{
		cfg: cfg, target: target, basePath: strings.TrimSuffix(target.Path, "/"),
		token: hex.EncodeToString(raw[:]), ln: limited(ln, cfg.MaxConns),
		inFlight: make(chan struct{}, cfg.MaxInFlight),
	}
	r.proxy = &httputil.ReverseProxy{
		Rewrite:       r.rewrite,
		Transport:     transport,
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "relay: the model endpoint did not answer", http.StatusBadGateway)
		},
	}
	r.srv = &http.Server{
		Handler: r, ReadHeaderTimeout: headerTimeout, ReadTimeout: readTimeout,
		IdleTimeout: idleTimeout, MaxHeaderBytes: maxHeaders,
	}
	go func() { _ = r.srv.Serve(r.ln) }()
	return r, nil
}

// endpoint parses a base URL the relay can forward to: http or https, a host, and nothing a
// request would have to carry besides its path.
func endpoint(base string) (*url.URL, error) {
	u, err := url.Parse(base)
	switch {
	case err != nil:
		return nil, fmt.Errorf("relay: base URL: %w", err)
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, fmt.Errorf("relay: base URL %q is not http or https", base)
	case u.Host == "" || u.Opaque != "":
		return nil, fmt.Errorf("relay: base URL %q names no host", base)
	case u.User != nil, u.RawQuery != "", u.Fragment != "":
		return nil, fmt.Errorf("relay: base URL %q carries more than a host and a path", base)
	}
	return u, nil
}

// Addr is the relay's address, 127.0.0.1:<port>.
func (r *Relay) Addr() string { return r.ln.Addr().String() }

// BaseURL is what the agent is given as its endpoint: the relay with the endpoint's path.
func (r *Relay) BaseURL() string { return "http://" + r.Addr() + r.basePath }

// Token is the bearer token the agent sends instead of a key.
func (r *Relay) Token() string { return r.token }

// Close stops the relay and every connection to it. The listener is closed here as well:
// Serve may not have taken it yet, and Server.Close closes only what Serve took.
func (r *Relay) Close() error {
	err := r.srv.Close()
	if lnErr := r.ln.Close(); lnErr != nil && !errors.Is(lnErr, net.ErrClosed) && err == nil {
		err = lnErr
	}
	return err
}

// ServeHTTP checks a request and forwards it. The order is the guarantee: nothing reaches the
// key, the log or the endpoint without the token, and nothing reaches the endpoint unrecorded.
func (r *Relay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if !r.authorized(req) {
		// A connection without the token is not kept for its next request.
		w.Header().Set("Connection", "close")
		http.Error(w, "relay: unauthorized", http.StatusUnauthorized)
		return
	}
	// An upgrade would turn the connection into a tunnel the relay neither counts nor ends
	// on Close; a model request is a plain GET or POST.
	if req.Header.Get("Upgrade") != "" {
		http.Error(w, "relay: no upgrade", http.StatusBadRequest)
		return
	}
	if req.Method != http.MethodGet && req.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "relay: method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !r.below(req.URL) {
		http.NotFound(w, req)
		return
	}
	select {
	case r.inFlight <- struct{}{}:
		defer func() { <-r.inFlight }()
	default:
		http.Error(w, "relay: too many requests in flight", http.StatusTooManyRequests)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, r.cfg.MaxBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "relay: request too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "relay: the request could not be read", http.StatusBadRequest)
		return
	}
	ctx := req.Context()
	key, err := r.cfg.Keys.Key(ctx, r.cfg.Upstream.Provider)
	if err != nil {
		http.Error(w, "relay: the provider's key is unavailable", http.StatusBadGateway)
		return
	}
	header, sent := forwarded(req, body)
	if !loopback(r.target.Hostname()) {
		sum := sha256.Sum256(sent)
		if err := r.cfg.Egress.Record(ctx, domain.Egress{
			ThreadID: r.cfg.ThreadID, Provider: r.cfg.Upstream.Provider, Host: r.target.Hostname(),
			Bytes: int64(len(sent)), PayloadSHA256: hex.EncodeToString(sum[:]), CreatedAt: r.cfg.Now().UnixMilli(),
		}); err != nil {
			http.Error(w, "relay: the request could not be recorded", http.StatusBadGateway)
			return
		}
	}
	out := req.Clone(context.WithValue(ctx, outCtx{}, outgoing{key: key, header: header}))
	out.Body = io.NopCloser(bytes.NewReader(body))
	out.ContentLength = int64(len(body))
	// What crosses is what was recorded: sent with its length, not chunked, a request
	// carries no trailer.
	out.TransferEncoding = nil
	out.Trailer = nil
	r.proxy.ServeHTTP(w, out)
}

// authorized reports whether req carries this relay's token as its bearer token.
func (r *Relay) authorized(req *http.Request) bool {
	got, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	return ok && subtle.ConstantTimeCompare([]byte(got), []byte(r.token)) == 1
}

// below reports whether u's path is the endpoint's path or under it, written plainly: only
// letters, digits and `-._~/` as sent, no escape, no empty segment and no dot segment. A
// server that reads `;`, `\` or a second decoding as a separator could otherwise take a path
// out of the endpoint's.
func (r *Relay) below(u *url.URL) bool {
	p := u.EscapedPath()
	if p != r.basePath && !strings.HasPrefix(p, r.basePath+"/") {
		return false
	}
	for _, c := range p {
		if !plain(c) {
			return false
		}
	}
	rest := strings.TrimPrefix(p, r.basePath)
	if rest == "" {
		return true
	}
	for _, seg := range strings.Split(rest[1:], "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

func plain(c rune) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~/", c)
}

// limitListener accepts at most n connections at once: one past it waits in the kernel's
// backlog until a served one closes.
type limitListener struct {
	net.Listener
	slots chan struct{}
}

func limited(ln net.Listener, n int) net.Listener {
	return &limitListener{Listener: ln, slots: make(chan struct{}, n)}
}

func (l *limitListener) Accept() (net.Conn, error) {
	l.slots <- struct{}{}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.slots
		return nil, err
	}
	return &slotConn{Conn: c, release: func() { <-l.slots }}, nil
}

type slotConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *slotConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

// forwarded is what of req crosses besides the key: the headers in crossing, and the request
// as it is recorded — its method, its path and query, those headers in their order, and its
// body — so a byte the agent sends out is a byte its row counts.
func forwarded(req *http.Request, body []byte) (http.Header, []byte) {
	header := http.Header{}
	var b bytes.Buffer
	b.WriteString(req.Method + " " + req.URL.RequestURI() + "\n")
	for _, name := range crossing {
		if v := req.Header.Get(name); v != "" {
			header.Set(name, v)
			b.WriteString(name + ": " + v + "\n")
		}
	}
	b.WriteString("\n")
	b.Write(body)
	return header, b.Bytes()
}

// rewrite points the request at the endpoint, with the headers ServeHTTP let cross and the
// provider's key, and nothing else of the agent's.
func (r *Relay) rewrite(pr *httputil.ProxyRequest) {
	pr.Out.URL.Scheme = r.target.Scheme
	pr.Out.URL.Host = r.target.Host
	pr.Out.Host = ""
	out, _ := pr.In.Context().Value(outCtx{}).(outgoing)
	pr.Out.Header = out.header.Clone()
	if pr.Out.Header == nil {
		pr.Out.Header = http.Header{}
	}
	if out.key != "" {
		pr.Out.Header.Set("Authorization", "Bearer "+out.key)
	}
}

// loopback is Tech Design §4's test for a request that does not leave the machine, the one the
// gateway applies to Umbral's own calls.
func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
