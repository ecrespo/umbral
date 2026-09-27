package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/ecrespo/umbral/internal/api"
	"github.com/ecrespo/umbral/internal/config"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
	secports "github.com/ecrespo/umbral/internal/security/ports"
)

// providerConfig is the daemon's providers: models.toml as loaded, each credential resolved
// against the keyring and, where REQ-SEC-012 allows it, the environment. It serves
// config.get and config.reload (API Spec §5.28) and the providers of system.status.
//
// It lives in the composition root because it is where configuration meets the keyring:
// `config` may not import `security`, and neither may know the wire.
type providerConfig struct {
	logger       *slog.Logger
	settingsPath string
	modelsPath   string
	keyring      secports.Keyring
	lookupEnv    func(string) (string, bool)

	mu       sync.RWMutex
	settings config.Settings
	models   config.Models
	resolved []secdomain.ResolvedCredential
	// observers hear every configuration applied, at start and on each reload.
	observers []func(config.Models, []secdomain.ResolvedCredential)
}

// observe calls fn with the configuration now in force and again after every reload. It is
// how the gateway learns which providers to build without importing this file's types.
func (p *providerConfig) observe(fn func(config.Models, []secdomain.ResolvedCredential)) {
	p.mu.Lock()
	p.observers = append(p.observers, fn)
	models, resolved := p.models, p.resolved
	p.mu.Unlock()
	fn(models, resolved)
}

// loadProviders reads models.toml and resolves its credentials. A models file the schema
// refuses is an error, and the daemon does not start on it, as for any other malformed
// setting; a provider entry with a plaintext key is refused on its own and logged
// (REQ-SEC-004), and a keyring the machine cannot reach disables only the providers that
// need it (REQ-SEC-008).
func loadProviders(ctx context.Context, logger *slog.Logger, settingsPath, modelsPath string,
	settings config.Settings, keyring secports.Keyring,
) (*providerConfig, error) {
	models, err := config.LoadModels(modelsPath)
	if err != nil {
		return nil, err
	}
	p := &providerConfig{
		logger: logger, settingsPath: settingsPath, modelsPath: modelsPath,
		keyring: keyring, lookupEnv: os.LookupEnv,
	}
	p.apply(settings, models, p.resolve(ctx, settings, models))
	return p, nil
}

// resolve probes the keyring, only if a provider names one, and resolves every credential.
// Probing only when needed matters on a desktop: asking a locked keyring for anything can
// raise an unlock prompt, which a user with no remote provider should never see.
func (p *providerConfig) resolve(ctx context.Context, settings config.Settings, models config.Models) []secdomain.ResolvedCredential {
	requests := make([]secdomain.CredentialRequest, 0, len(models.Providers))
	needsKeyring := false
	for _, prov := range models.Providers {
		req := secdomain.CredentialRequest{ProviderID: prov.ID, Ref: prov.Credential.Ref}
		switch prov.Credential.Kind {
		case config.CredentialKeyring:
			req.Source, needsKeyring = secdomain.SourceKeyring, true
		case config.CredentialEnv:
			// The env fallback depends on whether the keyring works, so it asks too.
			req.Source, needsKeyring = secdomain.SourceEnv, true
		default:
			req.Source = secdomain.SourceNone
		}
		requests = append(requests, req)
	}

	available := false
	if needsKeyring {
		err := p.keyring.Probe(ctx)
		available = err == nil
		if err != nil {
			p.logger.Warn("the keyring is unavailable; providers whose key is in it are disabled",
				slog.Any("error", err))
		}
	}

	return secdomain.ResolveCredentials(requests, secdomain.Lookups{
		KeyringAvailable: available,
		Keyring:          func(path string) (string, error) { return p.keyring.Get(ctx, path) },
		Env:              p.lookupEnv,
		AllowEnv:         settings.AllowEnvSecrets,
	})
}

// apply swaps in a loaded configuration and logs every provider that will not run as
// configured — by id and reason, never with a value (REQ-SEC-012).
func (p *providerConfig) apply(settings config.Settings, models config.Models, resolved []secdomain.ResolvedCredential) {
	p.mu.Lock()
	p.settings, p.models, p.resolved = settings, models, resolved
	observers := p.observers
	p.mu.Unlock()
	for _, fn := range observers {
		fn(models, resolved)
	}

	for _, r := range models.Rejected {
		p.logger.Error("a provider entry was rejected",
			slog.String("provider", r.ProviderID), slog.String("field", r.Field), slog.String("issue", r.Issue))
	}
	for _, r := range resolved {
		if r.Reason != "" {
			p.logger.Warn("a provider is not running as configured",
				slog.String("provider", r.ProviderID), slog.String("health", string(r.Health)),
				slog.String("reason", r.Reason))
		}
	}
}

// Get implements api.ConfigService.
func (p *providerConfig) Get(context.Context) (api.ConfigView, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.viewLocked(), nil
}

// Reload implements api.ConfigService: both files are validated before anything is applied,
// and a refused entry refuses the reload (API Spec §5.28).
func (p *providerConfig) Reload(ctx context.Context) (api.ConfigView, error) {
	settings, err := config.LoadSettings(p.logger, p.settingsPath)
	if err != nil {
		return api.ConfigView{}, fileInvalid(p.settingsPath, err)
	}
	models, err := config.LoadModels(p.modelsPath)
	if err != nil {
		return api.ConfigView{}, fileInvalid(p.modelsPath, err)
	}
	if len(models.Rejected) > 0 {
		details := make([]api.ErrorField, 0, len(models.Rejected))
		for _, r := range models.Rejected {
			details = append(details, api.ErrorField{
				Field: "providers." + r.ProviderID + "." + r.Field, Issue: r.Issue,
			})
		}
		return api.ConfigView{}, api.ConfigInvalidError(
			fmt.Sprintf("%d provider entries were rejected; nothing was applied", len(details)), details...)
	}
	p.apply(settings, models, p.resolve(ctx, settings, models))
	return p.Get(ctx)
}

// fileInvalid is a file that does not parse: one details entry that names the file, so a
// client lists every CONFIG_INVALID the same way (API Spec §5.28).
func fileInvalid(path string, err error) error {
	return api.ConfigInvalidError("a configuration file is invalid; nothing was applied",
		api.ErrorField{Field: filepath.Base(path), Issue: err.Error()})
}

// Statuses are the providers of system.status: every configured one, and every rejected
// one as down, so `umb status` shows why a provider the user wrote is missing.
func (p *providerConfig) Statuses() []api.ProviderStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]api.ProviderStatus, 0, len(p.resolved)+len(p.models.Rejected))
	for _, r := range p.resolved {
		out = append(out, api.ProviderStatus{ID: r.ProviderID, Health: string(r.Health), Reason: r.Reason})
	}
	for _, r := range p.models.Rejected {
		out = append(out, api.ProviderStatus{
			ID: r.ProviderID, Health: string(secdomain.HealthDown), Reason: secdomain.ReasonPlaintextSecret,
		})
	}
	return out
}

// Credential is a provider's resolved secret, zero when it may not start with one. It is how
// the gateway (T-F1-05) will reach a key.
func (p *providerConfig) Credential(providerID string) (secdomain.Secret, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, r := range p.resolved {
		if r.ProviderID == providerID {
			return r.Secret, nil
		}
	}
	return secdomain.Secret{}, errors.New("no such provider")
}

func (p *providerConfig) viewLocked() api.ConfigView {
	view := api.ConfigView{
		Settings: api.ConfigSettings{
			PaneHistory:     p.settings.PaneHistory,
			MaxMessageBytes: int64(p.settings.MaxMessageBytes),
			AllowEnv:        p.settings.AllowEnvSecrets,
		},
		Providers: make([]api.ConfigProvider, 0, len(p.models.Providers)),
		Rejected:  make([]api.ConfigRejection, 0, len(p.models.Rejected)),
	}
	byID := make(map[string]secdomain.ResolvedCredential, len(p.resolved))
	for _, r := range p.resolved {
		byID[r.ProviderID] = r
	}
	for _, prov := range p.models.Providers {
		r := byID[prov.ID]
		view.Providers = append(view.Providers, api.ConfigProvider{
			ID: prov.ID, Type: prov.Type, BaseURL: prov.BaseURL,
			Credential: prov.Credential.String(), Health: string(r.Health), Reason: r.Reason,
		})
	}
	for _, r := range p.models.Rejected {
		view.Rejected = append(view.Rejected, api.ConfigRejection{
			ProviderID: r.ProviderID, Field: r.Field, Issue: r.Issue,
		})
	}
	return view
}
