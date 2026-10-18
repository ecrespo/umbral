package main

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/ecrespo/umbral/internal/config"
	llmports "github.com/ecrespo/umbral/internal/llmgw/ports"
	"github.com/ecrespo/umbral/internal/security/adapters/rules"
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

// startRuleFetcher runs REQ-SEC-011's opt-in fetch in the background. With `[update]
// rules_check` off it makes no request at all. A rules_url the active redactor would change is
// refused, and the daemon does not start (Art. 4).
func startRuleFetcher(ctx context.Context, logger *slog.Logger, settings config.Settings, egress llmports.EgressLog, redact func(string) string) error {
	logger = logger.With(slog.String("module", "rules"))
	f, err := rules.NewFetcher(rules.FetchConfig{
		Enabled: settings.RulesCheck, URL: settings.RulesURL, Redact: redact,
		Egress: egress, Updater: unverifiedBundles{logger: logger}, Logger: logger,
	})
	if err != nil {
		return err
	}
	go f.Run(ctx)
	return nil
}

// unverifiedBundles is the updater until the trust store exists (T-F1-30): a bundle that
// cannot be verified is never applied (Art. 5, REQ-SEC-011), so every one is discarded with
// the reason logged.
type unverifiedBundles struct {
	logger *slog.Logger
}

func (u unverifiedBundles) Offer(ctx context.Context, bundle, _ []byte) {
	u.logger.WarnContext(ctx, "a rule bundle was fetched and discarded: this build cannot verify signatures yet",
		slog.Int("bytes", len(bundle)))
}
