package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/ecrespo/umbral/internal/api"
	"github.com/ecrespo/umbral/internal/config"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
)

// fakeKeyring is a keyring whose probe answers as the test says and which holds nothing.
type fakeKeyring struct {
	up     bool
	probes int
}

func (k *fakeKeyring) Probe(context.Context) error {
	k.probes++
	if !k.up {
		return secdomain.ErrKeyringUnavailable
	}
	return nil
}

func (k *fakeKeyring) Get(context.Context, string) (string, error) {
	return "", secdomain.ErrSecretNotFound
}

func writeConfigFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const envProvider = `
[[providers]]
id = "openrouter"
type = "openrouter"
base_url = "https://openrouter.ai/api/v1"
api_key = "env:UMBRAL_TEST_UNSET_KEY"
`

// TestAnEnvProviderAsksTheKeyringFirst_REQ_SEC_012: with a working keyring and
// allow_env on, `env:` is still refused — the keyring is the store — which is only true if
// an env-only models file makes the daemon probe the keyring at all.
func TestAnEnvProviderAsksTheKeyringFirst_REQ_SEC_012(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	settingsPath := writeConfigFile(t, dir, config.SettingsFileName, "[secrets]\nallow_env = true\n")
	modelsPath := writeConfigFile(t, dir, config.ModelsFileName, envProvider)
	settings, err := config.LoadSettings(nil, settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	kr := &fakeKeyring{up: true}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p, err := loadProviders(context.Background(), logger, settingsPath, modelsPath, settings, kr)
	if err != nil {
		t.Fatal(err)
	}
	if kr.probes != 1 {
		t.Errorf("an env-only models file probed the keyring %d times, want 1", kr.probes)
	}
	st := p.Statuses()
	if len(st) != 1 || st[0].Health != string(secdomain.HealthDown) || st[0].Reason != secdomain.ReasonEnvNotAllowed {
		t.Errorf("statuses = %+v, want openrouter down / env_secret_not_allowed", st)
	}
}

// TestAMalformedFileReloadNamesTheFile: a reload of a file that does not parse is
// CONFIG_INVALID with a details entry naming that file (API Spec §5.28), like any entry.
func TestAMalformedFileReloadNamesTheFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	settingsPath := writeConfigFile(t, dir, config.SettingsFileName, "")
	modelsPath := writeConfigFile(t, dir, config.ModelsFileName, "")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p, err := loadProviders(context.Background(), logger, settingsPath, modelsPath, config.Settings{}, &fakeKeyring{})
	if err != nil {
		t.Fatal(err)
	}
	writeConfigFile(t, dir, config.ModelsFileName, "[[providers]\n")
	_, err = p.Reload(context.Background())
	if !errors.Is(err, api.ErrConfigInvalid) {
		t.Fatalf("reload = %v, want CONFIG_INVALID", err)
	}
	details := api.ErrorDetails(err)
	if len(details) != 1 || details[0].Field != config.ModelsFileName {
		t.Errorf("details = %+v, want one entry naming %s", details, config.ModelsFileName)
	}
}

// TestConfigGetShowsTheEndpointInUse: `[otel] endpoint` needs a restart, so after a reload
// that adds, changes or removes it, config.get still shows the one the daemon exports to —
// not the file's, which nothing is using (delta `2026-10-otel`, decisions 4 and 8).
func TestConfigGetShowsTheEndpointInUse(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	settingsPath := writeConfigFile(t, dir, config.SettingsFileName, "")
	modelsPath := writeConfigFile(t, dir, config.ModelsFileName, "")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p, err := loadProviders(context.Background(), logger, settingsPath, modelsPath, config.Settings{}, &fakeKeyring{})
	if err != nil {
		t.Fatal(err)
	}
	writeConfigFile(t, dir, config.SettingsFileName, "[otel]\nendpoint = \"http://127.0.0.1:4318\"\n")
	view, err := p.Reload(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.Settings.OTelEndpoint != "" {
		t.Fatalf("after adding an endpoint by reload, config.get shows %q; nothing is exported", view.Settings.OTelEndpoint)
	}

	started := config.Settings{OTelEndpoint: "http://127.0.0.1:4318"}
	p, err = loadProviders(context.Background(), logger, settingsPath, modelsPath, started, &fakeKeyring{})
	if err != nil {
		t.Fatal(err)
	}
	writeConfigFile(t, dir, config.SettingsFileName, "")
	if view, err = p.Reload(context.Background()); err != nil || view.Settings.OTelEndpoint != started.OTelEndpoint {
		t.Fatalf("after removing it by reload, config.get shows %q (%v), want the one in use", view.Settings.OTelEndpoint, err)
	}
}
