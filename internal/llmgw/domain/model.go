package domain

import "strings"

// Health is a model's state in the catalog (Data Model §2.10, API Spec §4 Model).
type Health string

// Health values.
const (
	HealthOK       Health = "ok"
	HealthDegraded Health = "degraded"
	HealthDown     Health = "down"
	HealthUnknown  Health = "unknown"
)

// Reasons a model is not `ok`, besides the credential reasons its provider carries
// (`keyring_unavailable`, `env_secret`, …, delta `2026-09-provider-config`).
const (
	// ReasonDiscoveryFailed: the provider did not answer its model list on the last refresh;
	// its models are the ones it listed before, marked down.
	ReasonDiscoveryFailed = "discovery_failed"
	// ReasonNoAdapter: the provider's type has no adapter in this build yet.
	ReasonNoAdapter = "no_adapter"
	// ReasonInvalidConfig: the provider's entry is one no adapter accepts.
	ReasonInvalidConfig = "invalid_config"
	// ReasonOffline: `router.offline = true` and the provider is remote, so it is not
	// contacted at all (REQ-LLM-004).
	ReasonOffline = "offline"
)

// Capabilities are what a model can do, as far as its provider says.
type Capabilities struct {
	Tools, Vision, Reasoning, JSONSchema bool
	// ContextWindow is in tokens; 0 when the provider does not say.
	ContextWindow int64
}

// Model is one entry of the catalog. ID is `<provider>/<name>`, where provider is the id
// models.toml gives it and name is what the provider calls the model — which may contain
// slashes itself (`openrouter/moonshotai/kimi-k2`).
type Model struct {
	ID       string
	Provider string
	Local    bool
	Caps     Capabilities
	// Prices are micro-USD per million tokens (Art. 6).
	PriceInMicroUSDPerMTok  int64
	PriceOutMicroUSDPerMTok int64
	Health                  Health
	// Reason says why Health is not ok; it is its provider's, not stored per model.
	Reason string
	// UpdatedAt is epoch milliseconds (Art. 6).
	UpdatedAt int64
}

// QualifiedID joins a provider id and a model name into a catalog id.
func QualifiedID(provider, name string) string { return provider + "/" + name }

// Name is the model's name at its provider: the id without the provider prefix.
func (m Model) Name() string { return strings.TrimPrefix(m.ID, m.Provider+"/") }

// MicroUSDPerMTok converts a price in USD per token, as providers publish it, to micro-USD
// per million tokens: 1 USD/token is 10^12 µUSD/Mtok. Rounded to the nearest unit.
func MicroUSDPerMTok(usdPerToken float64) int64 {
	if usdPerToken <= 0 {
		return 0
	}
	return int64(usdPerToken*1e12 + 0.5)
}
