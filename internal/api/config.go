package api

import (
	"context"
	"encoding/json"
	"fmt"
)

// ConfigService is the daemon's configuration as `config.get` and `config.reload` see it
// (API Spec §5.28). The composition root implements it, which is how api avoids importing
// the configuration and the keyring it reports on.
type ConfigService interface {
	Get(ctx context.Context) (ConfigView, error)
	// Reload validates both files before applying anything. Entries it refuses are a
	// ConfigInvalidError with one detail each, and nothing changes (REQ-SEC-004).
	Reload(ctx context.Context) (ConfigView, error)
}

// ConfigView is `config.get`'s and `config.reload`'s result. Credentials appear as the
// reference the file writes (`keyring:<path>`, `env:<VAR>`), never as a value.
type ConfigView struct {
	Settings  ConfigSettings    `json:"settings"`
	Providers []ConfigProvider  `json:"providers"`
	Rejected  []ConfigRejection `json:"rejected"`
}

// ConfigSettings are config.toml's keys as the daemon runs with them.
type ConfigSettings struct {
	PaneHistory     bool  `json:"pane_history"`
	MaxMessageBytes int64 `json:"max_message_bytes"`
	AllowEnv        bool  `json:"allow_env"`
	// OTelEndpoint is `[otel] endpoint`, absent when nothing is exported (REQ-OBS-003).
	OTelEndpoint string `json:"otel_endpoint,omitempty"`
}

// ConfigProvider is one provider of models.toml and its state.
type ConfigProvider struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	BaseURL    string `json:"base_url" api:"optional"`
	Credential string `json:"credential" api:"optional"`
	Health     string `json:"health"`
	Reason     string `json:"reason,omitempty"`
}

// ConfigRejection is a provider entry refused at start, which the daemon runs without.
type ConfigRejection struct {
	ProviderID string `json:"provider_id"`
	Field      string `json:"field"`
	Issue      string `json:"issue"`
}

// ConfigInvalidError reports configuration the daemon refuses, naming each rejected entry.
func ConfigInvalidError(message string, details ...ErrorField) error {
	return &apiError{err: ErrConfigInvalid, message: message, details: details}
}

func handleConfigGet(ctx context.Context, c *conn, _ json.RawMessage) (any, error) {
	svc := c.server.cfg.Configuration
	if svc == nil {
		return nil, fmt.Errorf("%w: config.get", ErrNotImplemented)
	}
	view, err := svc.Get(ctx)
	return normalizeView(view), err
}

func handleConfigReload(ctx context.Context, c *conn, _ json.RawMessage) (any, error) {
	svc := c.server.cfg.Configuration
	if svc == nil {
		return nil, fmt.Errorf("%w: config.reload", ErrNotImplemented)
	}
	view, err := svc.Reload(ctx)
	if err != nil {
		return nil, err
	}
	return normalizeView(view), nil
}

// normalizeView turns nil lists into empty ones: a client ranges over them.
func normalizeView(v ConfigView) ConfigView {
	if v.Providers == nil {
		v.Providers = []ConfigProvider{}
	}
	if v.Rejected == nil {
		v.Rejected = []ConfigRejection{}
	}
	return v
}

// configWired is config.*'s `available`.
func configWired(cfg Config) bool { return cfg.Configuration != nil }
