package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ecrespo/umbral/internal/acp/adapters/relay"
	acpdomain "github.com/ecrespo/umbral/internal/acp/domain"
	"github.com/ecrespo/umbral/internal/config"
	llmdomain "github.com/ecrespo/umbral/internal/llmgw/domain"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
)

type gatewayEgress struct {
	mu   sync.Mutex
	rows []llmdomain.EgressRecord
}

func (g *gatewayEgress) Record(_ context.Context, r llmdomain.EgressRecord) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rows = append(g.rows, r)
	return nil
}

func relayProviders(secret string, health secdomain.Health) *providerConfig {
	return &providerConfig{
		models: config.Models{Providers: []config.Provider{
			{ID: "router", Type: "openai-compat", BaseURL: "http://models.example.test/v1", Credential: config.CredentialRef{Kind: config.CredentialKeyring, Ref: "umbral/router"}},
			{ID: "local", Type: "yzma", ModelsDir: "/models"},
		}},
		resolved: []secdomain.ResolvedCredential{
			{ProviderID: "router", Health: health, Secret: secdomain.NewSecret(secret)},
			{ProviderID: "local", Health: secdomain.HealthUnknown},
		},
	}
}

func TestARelaysKeyIsTheProvidersResolvedOne_REQ_SBX_009(t *testing.T) {
	ctx := context.Background()
	p := relayProviders("sk-first", secdomain.HealthUnknown)
	keys := relayKeys{providers: p}
	if k, err := keys.Key(ctx, "router"); err != nil || k != "sk-first" {
		t.Fatalf("Key = %q, %v", k, err)
	}
	p.resolved[0].Secret = secdomain.NewSecret("sk-reloaded")
	if k, _ := keys.Key(ctx, "router"); k != "sk-reloaded" {
		t.Fatalf("after a reload Key = %q", k)
	}
	p.resolved[0] = secdomain.ResolvedCredential{ProviderID: "router", Health: secdomain.HealthDown, Reason: secdomain.ReasonPlaintextSecret}
	if _, err := keys.Key(ctx, "router"); err == nil {
		t.Fatal("a provider whose credential is down gave a key")
	}
	if _, err := keys.Key(ctx, "nobody"); err == nil {
		t.Fatal("an unknown provider gave a key")
	}
}

func TestARelayIsRefusedAProviderWithoutAnEndpoint_REQ_SBX_009(t *testing.T) {
	p := relayProviders("sk", secdomain.HealthUnknown)
	for _, id := range []string{"local", "nobody", ""} {
		if _, err := relayConfig(p, &gatewayEgress{}, "th_1", id); err == nil {
			t.Errorf("provider %q was given a relay", id)
		}
	}
	if _, err := relayConfig(p, nil, "th_1", "router"); err == nil {
		t.Error("a relay with no egress log was configured")
	}
}

// TestARelayRecordsInTheGatewaysEgressLog_REQ_SBX_009 runs the relay as the daemon builds
// it: the provider's endpoint and key from models.toml, its rows in the log Umbral's own
// calls are recorded in, naming the thread.
func TestARelayRecordsInTheGatewaysEgressLog_REQ_SBX_009(t *testing.T) {
	var auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()
	log := &gatewayEgress{}
	cfg, err := relayConfig(relayProviders("sk-real", secdomain.HealthUnknown), log, "th_1", "router")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Transport = &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, srv.Listener.Addr().String())
	}}
	r, err := relay.Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, r.BaseURL()+"/chat/completions", strings.NewReader(`{"a":1}`))
	req.Header.Set("Authorization", "Bearer "+r.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || auth != "Bearer sk-real" || path != "/v1/chat/completions" {
		t.Fatalf("status %d, Authorization %q, path %q", resp.StatusCode, auth, path)
	}
	if len(log.rows) != 1 {
		t.Fatalf("%d rows", len(log.rows))
	}
	row := log.rows[0]
	if row.ThreadID != "th_1" || row.Provider != "router" || row.Host != "models.example.test" || row.Bytes <= 7 || row.PayloadSHA256 == "" || row.CreatedAt == 0 {
		t.Fatalf("row %+v", row)
	}
}

func TestARelaysRecordErrorStopsTheRequest_REQ_SBX_009(t *testing.T) {
	want := errors.New("disk full")
	err := relayEgress{log: failingEgress{want}}.Record(context.Background(), acpdomain.Egress{ThreadID: "th_1"})
	if !errors.Is(err, want) {
		t.Fatalf("Record = %v, want the log's error", err)
	}
}

type failingEgress struct{ err error }

func (f failingEgress) Record(context.Context, llmdomain.EgressRecord) error { return f.err }
