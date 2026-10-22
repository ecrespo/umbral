package fantasyconv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/llmgw/domain"
)

type memEgress struct {
	mu   sync.Mutex
	recs []domain.EgressRecord
	err  error
}

func (m *memEgress) Record(_ context.Context, r domain.EgressRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.recs = append(m.recs, r)
	return nil
}

type recordingRT struct{ bodies []string }

func (r *recordingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	b := ""
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		b = string(raw)
	}
	r.bodies = append(r.bodies, req.URL.Host+" "+b)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Request: req}, nil
}

// TestEveryRemoteRequestIsLogged_REQ_SEC_002: a request to a host that is not loopback is
// recorded before it is sent — provider, host, bytes, SHA-256 of the body, and the thread
// the context names — whether it is a discovery GET or a model call; the body still reaches
// the server intact. Loopback is not egress (DD-008), and a record that cannot be written
// stops the request (Art. 4).
func TestEveryRemoteRequestIsLogged_REQ_SEC_002(t *testing.T) {
	t.Parallel()

	log := &memEgress{}
	next := &recordingRT{}
	c := &http.Client{Transport: EgressTransport{
		Provider: "or", Log: log, Next: next, Now: func() time.Time { return time.UnixMilli(42) },
	}}
	send := func(c *http.Client, r *http.Request) error {
		resp, err := c.Do(r)
		if resp != nil {
			_ = resp.Body.Close()
		}
		return err
	}

	body := `{"model":"m","messages":[]}`
	req, _ := http.NewRequestWithContext(domain.WithThread(context.Background(), "thr_1"),
		http.MethodPost, "https://openrouter.ai/api/v1/chat/completions", strings.NewReader(body))
	if err := send(c, req); err != nil {
		t.Fatal(err)
	}
	get, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://openrouter.ai/api/v1/models", nil)
	if err := send(c, get); err != nil {
		t.Fatal(err)
	}
	for _, local := range []string{"http://127.0.0.1:11434/api/tags", "http://localhost:1234/v1/models", "http://[::1]:8080/v1/models"} {
		r, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, local, nil)
		if err := send(c, r); err != nil {
			t.Fatal(err)
		}
	}

	sum := sha256.Sum256([]byte(body))
	empty := sha256.Sum256(nil)
	want := []domain.EgressRecord{
		{ThreadID: "thr_1", Provider: "or", Host: "openrouter.ai", Bytes: int64(len(body)), PayloadSHA256: hex.EncodeToString(sum[:]), CreatedAt: 42},
		{Provider: "or", Host: "openrouter.ai", Bytes: 0, PayloadSHA256: hex.EncodeToString(empty[:]), CreatedAt: 42},
	}
	if len(log.recs) != 2 || log.recs[0] != want[0] || log.recs[1] != want[1] {
		t.Errorf("records = %+v\nwant %+v", log.recs, want)
	}
	if len(next.bodies) != 5 || next.bodies[0] != "openrouter.ai "+body {
		t.Errorf("sent = %q, want the body intact and every request sent", next.bodies)
	}

	log.err = errors.New("disk full")
	r, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://openrouter.ai/api/v1/models", nil)
	if err := send(c, r); err == nil || len(next.bodies) != 5 {
		t.Errorf("an unrecorded request went out (err %v)", err)
	}
	unlogged := &http.Client{Transport: EgressTransport{Provider: "x", Next: next}}
	if err := send(unlogged, r); err == nil {
		t.Error("a transport without a log let a remote request out")
	}
}
