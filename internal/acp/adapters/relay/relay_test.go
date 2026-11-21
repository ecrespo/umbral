package relay_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/acp/adapters/relay"
	"github.com/ecrespo/umbral/internal/acp/domain"
)

// recorder is an egress log that keeps what it was given, or refuses.
type recorder struct {
	mu   sync.Mutex
	rows []domain.Egress
	err  error
}

func (r *recorder) Record(_ context.Context, e domain.Egress) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.rows = append(r.rows, e)
	return nil
}

func (r *recorder) all() []domain.Egress {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.Egress(nil), r.rows...)
}

type keys map[string]string

func (k keys) Key(_ context.Context, provider string) (string, error) {
	v, ok := k[provider]
	if !ok {
		return "", errors.New("no such provider")
	}
	return v, nil
}

// upstream is a provider endpoint the test reads back from.
type upstream struct {
	srv  *httptest.Server
	mu   sync.Mutex
	seen []*http.Request
	body []string
	// rowsAtArrival is how many egress rows existed when each request arrived.
	rowsAtArrival []int
	rec           *recorder
	handle        func(w http.ResponseWriter, r *http.Request)
}

func newUpstream(t *testing.T, rec *recorder) *upstream {
	t.Helper()
	u := &upstream{rec: rec}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.seen = append(u.seen, r.Clone(r.Context()))
		u.body = append(u.body, string(b))
		if u.rec != nil {
			u.rowsAtArrival = append(u.rowsAtArrival, len(u.rec.all()))
		}
		h := u.handle
		u.mu.Unlock()
		if h != nil {
			h(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstream) requests() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.seen)
}

// remote dials the test endpoint for a host name that is not loopback, so the relay treats it
// as an endpoint off the machine.
func remote(u *upstream) http.RoundTripper {
	addr := u.srv.Listener.Addr().String()
	return &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}}
}

func start(t *testing.T, cfg relay.Config) *relay.Relay {
	t.Helper()
	r, err := relay.Start(cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// reply is an answer read whole.
type reply struct {
	StatusCode int
	Body       string
}

func post(t *testing.T, url, token, body string, header ...string) reply {
	t.Helper()
	resp := open(t, url, token, body, header...)
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return reply{StatusCode: resp.StatusCode, Body: string(b)}
}

// open sends a request and leaves its answer to the caller, who closes it.
func open(t *testing.T, url, token, body string, header ...string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func remoteConfig(u *upstream, rec *recorder) relay.Config {
	return relay.Config{
		ThreadID:  "th_1",
		Upstream:  domain.Upstream{Provider: "router", BaseURL: "http://models.example.test/api/v1"},
		Keys:      keys{"router": "sk-real-key"},
		Egress:    rec,
		Transport: remote(u),
	}
}

func TestTheRelayListensOnTheLoopbackOnly_REQ_SBX_009(t *testing.T) {
	rec := &recorder{}
	u := newUpstream(t, rec)
	r := start(t, remoteConfig(u, rec))
	host, _, err := net.SplitHostPort(r.Addr())
	if err != nil {
		t.Fatal(err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("the relay listens on %s, want 127.0.0.1", host)
	}
	if !strings.HasPrefix(r.BaseURL(), "http://"+r.Addr()+"/api/v1") {
		t.Fatalf("base URL %q does not name the relay and the endpoint's path", r.BaseURL())
	}
}

func TestTheRelayListensOnThePortItIsGiven_REQ_SBX_009(t *testing.T) {
	rec := &recorder{}
	u := newUpstream(t, rec)
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	cfg := remoteConfig(u, rec)
	cfg.Port = uint16(port)
	r := start(t, cfg)
	if _, got, _ := net.SplitHostPort(r.Addr()); got != itoa(port) {
		t.Fatalf("the relay listens on port %s, want %d", got, port)
	}
}

func itoa(n int) string {
	var b [20]byte
	i := len(b)
	for {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
		if n == 0 {
			return string(b[i:])
		}
	}
}

func TestTheRelayAnswersOnlyItsThreadsToken_REQ_SBX_009(t *testing.T) {
	rec := &recorder{}
	u := newUpstream(t, rec)
	r := start(t, remoteConfig(u, rec))
	other := start(t, remoteConfig(u, rec))
	if r.Token() == "" || r.Token() == other.Token() {
		t.Fatalf("two relays' tokens: %q and %q", r.Token(), other.Token())
	}
	url := r.BaseURL() + "/chat/completions"
	for name, resp := range map[string]reply{
		"none":            post(t, url, "", `{}`),
		"another relay's": post(t, url, other.Token(), `{}`),
		"the real key":    post(t, url, "sk-real-key", `{}`),
		"as x-api-key":    post(t, url, "", `{}`, "X-Api-Key", r.Token()),
		"a prefix":        post(t, url, r.Token()[:len(r.Token())-1], `{}`),
	} {
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("token %s: status %d, want 401", name, resp.StatusCode)
		}
	}
	if n := u.requests(); n != 0 {
		t.Fatalf("the endpoint received %d requests without the token", n)
	}
	if rows := rec.all(); len(rows) != 0 {
		t.Fatalf("a refused request was recorded: %+v", rows)
	}
}

func TestTheRelayForwardsWithTheProvidersKeyAndNotTheToken_REQ_SBX_009(t *testing.T) {
	rec := &recorder{}
	u := newUpstream(t, rec)
	r := start(t, remoteConfig(u, rec))
	resp := post(t, r.BaseURL()+"/chat/completions?stream=true", r.Token(), `{"model":"m"}`,
		"X-Api-Key", "sk-agents-own", "Api-Key", "sk-agents-own", "Cookie", "a=b", "Content-Type", "application/json")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if resp.Body != `{"ok":true}` {
		t.Fatalf("answer %q", resp.Body)
	}
	if u.requests() != 1 {
		t.Fatalf("the endpoint received %d requests, want 1", u.requests())
	}
	seen := u.seen[0]
	if a := seen.Header.Get("Authorization"); a != "Bearer sk-real-key" {
		t.Errorf("Authorization %q, want the provider's key", a)
	}
	for _, h := range []string{"X-Api-Key", "Api-Key", "Cookie"} {
		if v := seen.Header.Get(h); v != "" {
			t.Errorf("%s %q crossed the relay", h, v)
		}
	}
	for name, values := range seen.Header {
		for _, v := range values {
			if strings.Contains(v, r.Token()) {
				t.Errorf("the relay's token crossed in %s", name)
			}
		}
	}
	if seen.Host != "models.example.test" {
		t.Errorf("Host %q, want the endpoint's", seen.Host)
	}
	if seen.URL.Path != "/api/v1/chat/completions" || seen.URL.RawQuery != "stream=true" {
		t.Errorf("forwarded to %s?%s", seen.URL.Path, seen.URL.RawQuery)
	}
	if u.body[0] != `{"model":"m"}` {
		t.Errorf("body %q", u.body[0])
	}
	if ct := seen.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type %q was not forwarded", ct)
	}
	if xf := seen.Header.Get("X-Forwarded-For"); xf != "" {
		t.Errorf("X-Forwarded-For %q was added", xf)
	}
}

func TestTheRelayTakesTheKeyAtEachRequest_REQ_SBX_009(t *testing.T) {
	rec := &recorder{}
	u := newUpstream(t, rec)
	k := keys{"router": "sk-first"}
	cfg := remoteConfig(u, rec)
	cfg.Keys = k
	r := start(t, cfg)
	post(t, r.BaseURL()+"/x", r.Token(), `{}`)
	k["router"] = "sk-second"
	post(t, r.BaseURL()+"/x", r.Token(), `{}`)
	if a, b := u.seen[0].Header.Get("Authorization"), u.seen[1].Header.Get("Authorization"); a != "Bearer sk-first" || b != "Bearer sk-second" {
		t.Fatalf("keys sent %q, %q: a reloaded key is not used", a, b)
	}
}

func TestAProviderWithoutAKeyGetsNoAuthorization_REQ_SBX_009(t *testing.T) {
	u := newUpstream(t, nil)
	r := start(t, relay.Config{
		ThreadID: "th_1",
		Upstream: domain.Upstream{Provider: "ollama", BaseURL: u.srv.URL + "/v1"},
		Keys:     keys{"ollama": ""},
		Egress:   &recorder{},
	})
	post(t, r.BaseURL()+"/chat/completions", r.Token(), `{}`)
	if a := u.seen[0].Header.Get("Authorization"); a != "" {
		t.Fatalf("Authorization %q sent for a provider with no key", a)
	}
}

func TestTheRelayRefusesWhenTheKeyCannotBeRead_REQ_SBX_009(t *testing.T) {
	rec := &recorder{}
	u := newUpstream(t, rec)
	cfg := remoteConfig(u, rec)
	cfg.Keys = keys{}
	r := start(t, cfg)
	resp := post(t, r.BaseURL()+"/x", r.Token(), `{}`)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", resp.StatusCode)
	}
	if u.requests() != 0 || len(rec.all()) != 0 {
		t.Fatalf("forwarded or recorded without a key: %d requests, %d rows", u.requests(), len(rec.all()))
	}
}

func TestTheRelayRecordsARemoteRequestBeforeForwardingIt_REQ_SBX_009(t *testing.T) {
	rec := &recorder{}
	u := newUpstream(t, rec)
	cfg := remoteConfig(u, rec)
	cfg.Now = func() time.Time { return time.UnixMilli(1_700_000_000_000) }
	r := start(t, cfg)
	body := `{"model":"m","messages":[]}`
	post(t, r.BaseURL()+"/chat/completions?x=1", r.Token(), body, "Content-Type", "application/json", "User-Agent", "agent/1")
	rows := rec.all()
	if len(rows) != 1 {
		t.Fatalf("%d rows, want 1", len(rows))
	}
	// What is recorded is the request as it is forwarded, but for the key: its method, its
	// path and query, the headers that cross in their fixed order, and its body.
	sent := "POST /api/v1/chat/completions?x=1\nContent-Type: application/json\nUser-Agent: agent/1\n\n" + body
	sum := sha256.Sum256([]byte(sent))
	want := domain.Egress{
		ThreadID: "th_1", Provider: "router", Host: "models.example.test",
		Bytes: int64(len(sent)), PayloadSHA256: hex.EncodeToString(sum[:]), CreatedAt: 1_700_000_000_000,
	}
	if rows[0] != want {
		t.Fatalf("row %+v, want %+v", rows[0], want)
	}
	if u.rowsAtArrival[0] != 1 {
		t.Fatalf("the request reached the endpoint with %d rows written, want it recorded first", u.rowsAtArrival[0])
	}
}

func TestTheRelayRecordsNoLoopbackRequest_REQ_SBX_009(t *testing.T) {
	rec := &recorder{}
	u := newUpstream(t, rec)
	r := start(t, relay.Config{
		ThreadID: "th_1",
		Upstream: domain.Upstream{Provider: "ollama", BaseURL: u.srv.URL + "/v1"},
		Keys:     keys{"ollama": ""},
		Egress:   rec,
	})
	resp := post(t, r.BaseURL()+"/chat/completions", r.Token(), `{}`)
	if resp.StatusCode != http.StatusOK || u.requests() != 1 {
		t.Fatalf("status %d, %d requests", resp.StatusCode, u.requests())
	}
	if rows := rec.all(); len(rows) != 0 {
		t.Fatalf("a loopback request was recorded: %+v", rows)
	}
}

func TestTheRelayRefusesARequestItCannotRecord_REQ_SBX_009(t *testing.T) {
	rec := &recorder{err: errors.New("disk full")}
	u := newUpstream(t, rec)
	r := start(t, remoteConfig(u, rec))
	resp := post(t, r.BaseURL()+"/chat/completions", r.Token(), `{}`)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", resp.StatusCode)
	}
	if u.requests() != 0 {
		t.Fatal("a request that could not be recorded was forwarded")
	}
	if strings.Contains(resp.Body, "sk-real-key") {
		t.Fatal("the answer carries the key")
	}
}

func TestTheRelayStreamsTheAnswer_REQ_SBX_009(t *testing.T) {
	rec := &recorder{}
	u := newUpstream(t, rec)
	release := make(chan struct{})
	u.handle = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "data: second\n\n")
	}
	r := start(t, remoteConfig(u, rec))
	defer close(release)
	resp := open(t, r.BaseURL()+"/chat/completions", r.Token(), `{}`)
	defer func() { _ = resp.Body.Close() }()
	line := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(resp.Body).ReadString('\n')
		line <- s
	}()
	select {
	case s := <-line:
		if s != "data: first\n" {
			t.Fatalf("first line %q", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first event did not arrive before the endpoint finished: the answer is buffered")
	}
}

func TestTheRelayForwardsOnlyBelowItsEndpoint_REQ_SBX_009(t *testing.T) {
	rec := &recorder{}
	u := newUpstream(t, rec)
	r := start(t, remoteConfig(u, rec))
	base := "http://" + r.Addr()
	for _, path := range []string{"/", "/api", "/api/v1x/models", "/other/v1/models", "/api/v1/../../admin", "/api/v1/%2e%2e/admin"} {
		resp := post(t, base+path, r.Token(), `{}`)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", path, resp.StatusCode)
		}
	}
	if u.requests() != 0 || len(rec.all()) != 0 {
		t.Fatalf("a path outside the endpoint was forwarded or recorded: %d requests", u.requests())
	}
	if resp := post(t, base+"/api/v1", r.Token(), `{}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("the endpoint itself: status %d", resp.StatusCode)
	}
}

func TestTheRelayRefusesABodyOverItsLimit_REQ_SBX_009(t *testing.T) {
	rec := &recorder{}
	u := newUpstream(t, rec)
	cfg := remoteConfig(u, rec)
	cfg.MaxBody = 16
	r := start(t, cfg)
	resp := post(t, r.BaseURL()+"/x", r.Token(), strings.Repeat("a", 17))
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413", resp.StatusCode)
	}
	if u.requests() != 0 || len(rec.all()) != 0 {
		t.Fatal("a body over the limit was forwarded or recorded")
	}
}

func TestARelayIsRefusedAnEndpointItCannotForwardTo_REQ_SBX_009(t *testing.T) {
	for _, base := range []string{"", "ftp://h/v1", "http:///v1", "relative/v1", "http://h/v1?x=1", "http://user:pw@h/v1"} {
		_, err := relay.Start(relay.Config{
			ThreadID: "th_1", Upstream: domain.Upstream{Provider: "p", BaseURL: base},
			Keys: keys{"p": ""}, Egress: &recorder{},
		})
		if err == nil {
			t.Errorf("base URL %q was accepted", base)
		}
	}
	if _, err := relay.Start(relay.Config{Upstream: domain.Upstream{Provider: "p", BaseURL: "http://h/v1"}, Keys: keys{}, Egress: &recorder{}}); err == nil {
		t.Error("a relay with no thread was accepted")
	}
	if _, err := relay.Start(relay.Config{ThreadID: "t", Upstream: domain.Upstream{Provider: "p", BaseURL: "http://h/v1"}, Keys: keys{}}); err == nil {
		t.Error("a relay with no egress log was accepted")
	}
}

func TestAClosedRelayAnswersNothing_REQ_SBX_009(t *testing.T) {
	rec := &recorder{}
	u := newUpstream(t, rec)
	r, err := relay.Start(remoteConfig(u, rec))
	if err != nil {
		t.Fatal(err)
	}
	addr := r.Addr()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	d := net.Dialer{Timeout: time.Second}
	if c, err := d.DialContext(context.Background(), "tcp", addr); err == nil {
		_ = c.Close()
		t.Fatal("a closed relay still accepts connections")
	}
}

// raw sends a request written by hand and returns the status line and what the endpoint saw.
func raw(t *testing.T, addr, request string) string {
	t.Helper()
	d := net.Dialer{Timeout: time.Second}
	c, err := d.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, request); err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(c).ReadString('\n')
	return strings.TrimSpace(line)
}

func TestTheRelayRefusesAnUpgrade_REQ_SBX_009(t *testing.T) {
	rec := &recorder{}
	u := newUpstream(t, rec)
	r := start(t, remoteConfig(u, rec))
	auth := "Authorization: Bearer " + r.Token() + "\r\n"
	for name, req := range map[string]string{
		"websocket":     "GET /api/v1/realtime HTTP/1.1\r\nHost: x\r\n" + auth + "Connection: Upgrade\r\nUpgrade: websocket\r\n\r\n",
		"h2c":           "GET /api/v1/x HTTP/1.1\r\nHost: x\r\n" + auth + "Connection: keep-alive, upgrade\r\nUpgrade: h2c\r\n\r\n",
		"upgrade alone": "GET /api/v1/x HTTP/1.1\r\nHost: x\r\n" + auth + "Upgrade: websocket\r\n\r\n",
		"connect":       "CONNECT models.example.test:443 HTTP/1.1\r\nHost: models.example.test:443\r\n" + auth + "\r\n",
	} {
		if line := raw(t, r.Addr(), req); !strings.Contains(line, " 400 ") && !strings.Contains(line, " 405 ") && !strings.Contains(line, " 404 ") {
			t.Errorf("%s: answered %q, want it refused", name, line)
		}
	}
	if u.requests() != 0 || len(rec.all()) != 0 {
		t.Fatalf("an upgrade was forwarded or recorded: %d requests, %d rows", u.requests(), len(rec.all()))
	}
}

func TestTheRelayForwardsGetAndPostOnly_REQ_SBX_009(t *testing.T) {
	rec := &recorder{}
	u := newUpstream(t, rec)
	r := start(t, remoteConfig(u, rec))
	for _, m := range []string{"TRACE", "PUT", "DELETE", "PATCH", "OPTIONS"} {
		req, _ := http.NewRequestWithContext(context.Background(), m, r.BaseURL()+"/x", nil)
		req.Header.Set("Authorization", "Bearer "+r.Token())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s: status %d, want 405", m, resp.StatusCode)
		}
	}
	if u.requests() != 0 {
		t.Fatalf("%d requests of another method were forwarded", u.requests())
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, r.BaseURL()+"/models", nil)
	req.Header.Set("Authorization", "Bearer "+r.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET: status %d", resp.StatusCode)
	}
}

func TestTheRelayForwardsNoTrailer_REQ_SBX_009(t *testing.T) {
	rec := &recorder{}
	u := newUpstream(t, rec)
	r := start(t, remoteConfig(u, rec))
	req := "POST /api/v1/x HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer " + r.Token() + "\r\n" +
		"Transfer-Encoding: chunked\r\nTrailer: X-Exfil\r\n\r\n2\r\n{}\r\n0\r\nX-Exfil: " + strings.Repeat("s", 64) + "\r\n\r\n"
	if line := raw(t, r.Addr(), req); !strings.Contains(line, " 200 ") {
		t.Fatalf("answered %q", line)
	}
	if u.requests() != 1 {
		t.Fatalf("%d requests", u.requests())
	}
	if v := u.seen[0].Trailer.Get("X-Exfil"); v != "" {
		t.Fatalf("a trailer crossed outside the recorded body: %d bytes", len(v))
	}
	if rows := rec.all(); len(rows) != 1 || rows[0].Bytes != int64(len("POST /api/v1/x\n\n{}")) {
		t.Fatalf("rows %+v", rows)
	}
}

func TestTheRelayForwardsOnlyAPlainPath_REQ_SBX_009(t *testing.T) {
	rec := &recorder{}
	u := newUpstream(t, rec)
	r := start(t, remoteConfig(u, rec))
	for _, path := range []string{"/api/v1/..;/admin", "/api/v1/..%5cadmin", "/api/v1/%252e%252e/admin", "/api/v1/a;b", "/api/v1//x", "/api/v1/%2fx"} {
		line := raw(t, r.Addr(), "POST "+path+" HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer "+r.Token()+"\r\nContent-Length: 0\r\n\r\n")
		if !strings.Contains(line, " 404 ") && !strings.Contains(line, " 400 ") {
			t.Errorf("%s: answered %q, want it refused", path, line)
		}
	}
	if u.requests() != 0 {
		t.Fatalf("%d unusual paths were forwarded", u.requests())
	}
	if resp := post(t, r.BaseURL()+"/chat/completions-v2.1_x~y", r.Token(), `{}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("a plain path: status %d", resp.StatusCode)
	}
}

func TestARefusedConnectionIsClosed_REQ_SBX_009(t *testing.T) {
	rec := &recorder{}
	u := newUpstream(t, rec)
	r := start(t, remoteConfig(u, rec))
	d := net.Dialer{Timeout: time.Second}
	c, err := d.DialContext(context.Background(), "tcp", r.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(c, "GET /api/v1/x HTTP/1.1\r\nHost: x\r\n\r\n")
	b, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("the connection stayed open after its 401: %v", err)
	}
	if !strings.Contains(string(b), " 401 ") {
		t.Fatalf("answer %q", b)
	}
}

func TestTheRelayBoundsItsConnections_REQ_SBX_009(t *testing.T) {
	rec := &recorder{}
	u := newUpstream(t, rec)
	cfg := remoteConfig(u, rec)
	cfg.MaxConns = 2
	r := start(t, cfg)
	d := net.Dialer{Timeout: time.Second}
	held := make([]net.Conn, 0, 2)
	for range 2 {
		c, err := d.DialContext(context.Background(), "tcp", r.Addr())
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	time.Sleep(100 * time.Millisecond)
	c, err := d.DialContext(context.Background(), "tcp", r.Addr())
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(500 * time.Millisecond))
	_, _ = io.WriteString(c, "GET /api/v1/x HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer "+r.Token()+"\r\n\r\n")
	if line, _ := bufio.NewReader(c).ReadString('\n'); line != "" {
		t.Fatalf("a third connection was served past the limit: %q", line)
	}
	_ = c.Close()
	for _, h := range held {
		_ = h.Close()
	}
	// Once the held ones are gone, the relay serves again.
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp := post(t, r.BaseURL()+"/x", r.Token(), `{}`)
		if resp.StatusCode == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the relay did not serve again: %d", resp.StatusCode)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestTheRelayBoundsTheRequestsInFlight_REQ_SBX_009(t *testing.T) {
	rec := &recorder{}
	u := newUpstream(t, rec)
	release := make(chan struct{})
	u.handle = func(w http.ResponseWriter, _ *http.Request) { <-release; _, _ = io.WriteString(w, "ok") }
	cfg := remoteConfig(u, rec)
	cfg.MaxInFlight = 1
	r := start(t, cfg)
	done := make(chan int, 1)
	go func() {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, r.BaseURL()+"/x", strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+r.Token())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- 0
			return
		}
		_ = resp.Body.Close()
		done <- resp.StatusCode
	}()
	for u.requests() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if resp := post(t, r.BaseURL()+"/x", r.Token(), `{}`); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("a second request in flight: status %d, want 429", resp.StatusCode)
	}
	close(release)
	if code := <-done; code != http.StatusOK {
		t.Fatalf("the first request: status %d", code)
	}
}

func TestTheRelayForwardsOnlyTheHeadersAModelRequestNeeds_REQ_SBX_009(t *testing.T) {
	rec := &recorder{}
	u := newUpstream(t, rec)
	r := start(t, remoteConfig(u, rec))
	post(t, r.BaseURL()+"/x", r.Token(), `{}`,
		"Content-Type", "application/json", "Accept", "text/event-stream", "OpenAI-Beta", "assistants=v2",
		"X-Exfil", strings.Repeat("s", 4000), "Referer", "r", "Origin", "o")
	seen := u.seen[0].Header
	for _, h := range []string{"Content-Type", "Accept", "OpenAI-Beta"} {
		if seen.Get(h) == "" {
			t.Errorf("%s did not cross", h)
		}
	}
	for _, h := range []string{"X-Exfil", "Referer", "Origin"} {
		if v := seen.Get(h); v != "" {
			t.Errorf("%s crossed: %d bytes outside what is allowed", h, len(v))
		}
	}
}

func TestWhatCrossesBesidesTheBodyIsRecorded_REQ_SBX_009(t *testing.T) {
	rec := &recorder{}
	u := newUpstream(t, rec)
	r := start(t, remoteConfig(u, rec))
	post(t, r.BaseURL()+"/x", r.Token(), `{}`)
	post(t, r.BaseURL()+"/x?q="+strings.Repeat("a", 100), r.Token(), `{}`)
	post(t, r.BaseURL()+"/"+strings.Repeat("b", 100), r.Token(), `{}`)
	post(t, r.BaseURL()+"/x", r.Token(), `{}`, "User-Agent", strings.Repeat("c", 100))
	rows := rec.all()
	if len(rows) != 4 {
		t.Fatalf("%d rows", len(rows))
	}
	for i, row := range rows[1:] {
		// Each replaces a few bytes of the first request's, a path segment or the client's own
		// User-Agent, so the difference is at least 80.
		if row.Bytes < rows[0].Bytes+80 || row.PayloadSHA256 == rows[0].PayloadSHA256 {
			t.Errorf("request %d carried 100 more bytes outside its body and its row says %d bytes against %d", i+2, row.Bytes, rows[0].Bytes)
		}
	}
}
