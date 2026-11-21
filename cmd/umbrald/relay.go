package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/ecrespo/umbral/internal/acp/adapters/relay"
	acpdomain "github.com/ecrespo/umbral/internal/acp/domain"
	acpports "github.com/ecrespo/umbral/internal/acp/ports"
	llmdomain "github.com/ecrespo/umbral/internal/llmgw/domain"
	llmports "github.com/ecrespo/umbral/internal/llmgw/ports"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
)

// relayKeys gives a hosted thread's relay its provider's key from the credentials models.toml
// resolved, read at each request so a reload is followed (REQ-SBX-009). The agent is never
// given it.
type relayKeys struct{ providers *providerConfig }

var _ acpports.Keys = relayKeys{}

func (k relayKeys) Key(_ context.Context, provider string) (string, error) {
	k.providers.mu.RLock()
	defer k.providers.mu.RUnlock()
	for _, r := range k.providers.resolved {
		if r.ProviderID != provider {
			continue
		}
		if r.Health == secdomain.HealthDown {
			return "", fmt.Errorf("provider %s: its credential is unavailable: %s", provider, r.Reason)
		}
		return r.Secret.Reveal(), nil
	}
	return "", fmt.Errorf("provider %q is not configured", provider)
}

// relayEgress writes a relay's rows to the egress log Umbral's own model calls are recorded
// in (Art. 4, Tech Design §4).
type relayEgress struct{ log llmports.EgressLog }

var _ acpports.EgressLog = relayEgress{}

func (e relayEgress) Record(ctx context.Context, r acpdomain.Egress) error {
	return e.log.Record(ctx, llmdomain.EgressRecord{
		ThreadID: r.ThreadID, Provider: r.Provider, Host: r.Host,
		Bytes: r.Bytes, PayloadSHA256: r.PayloadSHA256, CreatedAt: r.CreatedAt,
	})
}

// acpRelays serves hosted threads' model relays (T-HRD-20c) over models.toml's providers and
// the gateway's egress log.
type acpRelays struct {
	providers *providerConfig
	egress    llmports.EgressLog
}

var _ acpports.Relays = acpRelays{}

func (r acpRelays) Upstream(provider string) (string, error) {
	cfg, err := relayConfig(r.providers, r.egress, "", provider)
	if err != nil {
		return "", err
	}
	return cfg.Upstream.BaseURL, nil
}

func (r acpRelays) Start(_ context.Context, threadID, provider string, port uint16) (acpports.Relay, error) {
	cfg, err := relayConfig(r.providers, r.egress, threadID, provider)
	if err != nil {
		return nil, err
	}
	cfg.Port = port
	// The relay serves the agent until its process ends, past the launch request that starts
	// it, and is closed with that process, not with ctx.
	rl, err := relay.Start(cfg) //nolint:contextcheck // outlives the launch; closed with the agent
	if err != nil {
		return nil, err
	}
	return startedRelay{rl}, nil
}

// startedRelay is a running relay as the acp host sees it.
type startedRelay struct{ *relay.Relay }

func (s startedRelay) Origin() string { return "http://" + s.Addr() }

// relayConfig is the relay of a hosted thread whose agent uses providerID (T-HRD-20b): its
// endpoint is the provider's base URL in models.toml, its key the provider's credential and
// its log the gateway's. A provider without a base URL — one loaded in process — has nothing
// to relay to. The port is the caller's to set.
func relayConfig(providers *providerConfig, egress llmports.EgressLog, threadID, providerID string) (relay.Config, error) {
	if egress == nil {
		return relay.Config{}, errors.New("relay: no egress log")
	}
	providers.mu.RLock()
	defer providers.mu.RUnlock()
	for _, prov := range providers.models.Providers {
		if prov.ID != providerID {
			continue
		}
		if prov.BaseURL == "" {
			return relay.Config{}, fmt.Errorf("relay: provider %s has no endpoint to forward to", providerID)
		}
		return relay.Config{
			ThreadID: threadID,
			Upstream: acpdomain.Upstream{Provider: prov.ID, BaseURL: prov.BaseURL},
			Keys:     relayKeys{providers: providers},
			Egress:   relayEgress{log: egress},
		}, nil
	}
	return relay.Config{}, fmt.Errorf("relay: provider %q is not configured", providerID)
}
