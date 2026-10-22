package fantasyconv

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/ecrespo/umbral/internal/llmgw/domain"
	"github.com/ecrespo/umbral/internal/llmgw/ports"
)

// EgressTransport records every request to a host that is not loopback before sending it
// (Art. 4, REQ-SEC-002, DD-008): the provider, the host, the bytes and the SHA-256 of the
// body, and the thread from the request's context. It sits under Fantasy and under
// discovery alike, so no request of an adapter can leave unrecorded. A record that cannot
// be written stops the request: it fails closed.
type EgressTransport struct {
	Provider string
	Log      ports.EgressLog
	Next     http.RoundTripper
	Now      func() time.Time
}

// RoundTrip implements http.RoundTripper.
func (t EgressTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	next := t.Next
	if next == nil {
		next = http.DefaultTransport
	}
	if isLoopbackHost(req.URL.Hostname()) {
		return next.RoundTrip(req)
	}

	var body []byte
	if req.Body != nil && req.Body != http.NoBody {
		var err error
		body, err = io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		req = req.Clone(req.Context())
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		req.ContentLength = int64(len(body))
	}
	sum := sha256.Sum256(body)
	now := time.Now
	if t.Now != nil {
		now = t.Now
	}
	if t.Log == nil {
		return nil, fmt.Errorf("egress to %s refused: no egress log", req.URL.Hostname())
	}
	if err := t.Log.Record(req.Context(), domain.EgressRecord{
		ThreadID: domain.ThreadOf(req.Context()), Provider: t.Provider, Host: req.URL.Hostname(),
		Bytes: int64(len(body)), PayloadSHA256: hex.EncodeToString(sum[:]), CreatedAt: now().UnixMilli(),
	}); err != nil {
		return nil, fmt.Errorf("egress to %s refused: it could not be recorded: %w", req.URL.Hostname(), err)
	}
	return next.RoundTrip(req)
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Client returns an HTTP client whose every non-loopback request is recorded. base may be
// nil.
func Client(base *http.Client, provider string, log ports.EgressLog) *http.Client {
	c := &http.Client{}
	if base != nil {
		*c = *base
	}
	c.Transport = EgressTransport{Provider: provider, Log: log, Next: c.Transport}
	return c
}
