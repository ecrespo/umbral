package main

import (
	"context"
	"log/slog"
	"net/url"
	"path/filepath"

	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/config"
	llmports "github.com/ecrespo/umbral/internal/llmgw/ports"
	"github.com/ecrespo/umbral/internal/security"
	"github.com/ecrespo/umbral/internal/security/adapters/rules"
	"github.com/ecrespo/umbral/internal/security/adapters/rules/trustseed"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
	secports "github.com/ecrespo/umbral/internal/security/ports"
	"github.com/ecrespo/umbral/internal/store"
)

// rulesDir is REQ-SEC-010's override directory, beside config.toml:
// `$XDG_CONFIG_HOME/umbral/rules/`.
func rulesDir(settingsPath string) string {
	return filepath.Join(filepath.Dir(settingsPath), "rules")
}

// loadRules builds the rule set in force at start: the built-in rules, with the override
// directory's valid files on top (T-F1-29). The agent runtime, policy.explain and every
// redaction read the one value returned, so they cannot disagree.
func loadRules(logger *slog.Logger, settingsPath string) *rules.Active {
	return rules.NewActive(rules.LoadDir(logger.With(slog.String("module", "rules")), rulesDir(settingsPath)))
}

// startRules builds the rule manager over the database, applies the trust seed and the active
// bundle, and runs REQ-SEC-011's opt-in fetch in the background. With `[update] rules_check`
// off the fetcher makes no request at all, and with updates failed closed it makes none
// either (REQ-SEC-015). A rules_url the active redactor would change is refused, and the
// daemon does not start (Art. 4).
func startRules(ctx context.Context, logger *slog.Logger, db *store.Store, settings config.Settings, egress llmports.EgressLog,
	redact func(string) string, active *rules.Active, events *bus.Bus, metrics security.RuleMetrics,
) (*security.Rules, error) {
	logger = logger.With(slog.String("module", "rules"))
	seed, err := trustseed.Load()
	if err != nil {
		return nil, err
	}
	var host string
	if u, err := url.Parse(settings.RulesURL); err == nil {
		host = u.Host
	}
	manager, err := security.NewRules(security.RulesConfig{
		Store: rules.NewStore(db), Base: active, Seed: seed, Builtin: secdomain.BuiltinRules(),
		Enabled: settings.RulesCheck, Host: host, LocalOverride: active.HasLocal(),
		Publish:  func(e secports.RuleUpdateRejected) { events.Publish(e) },
		Metrics:  metrics,
		NewKeyID: func() string { return store.NewID(store.PrefixTrustKey) },
		Logger:   logger,
	})
	if err != nil {
		return nil, err
	}
	if err := manager.Start(ctx); err != nil {
		return nil, err
	}
	f, err := rules.NewFetcher(rules.FetchConfig{
		Enabled: settings.RulesCheck, URL: settings.RulesURL, Redact: redact, Allowed: manager.UpdatesAllowed,
		Egress: egress, Updater: manager, Logger: logger,
	})
	if err != nil {
		return nil, err
	}
	go f.Run(ctx)
	return manager, nil
}
