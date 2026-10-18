package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/config"
	"github.com/ecrespo/umbral/internal/llmgw/adapters/egresslog"
	"github.com/ecrespo/umbral/internal/notify"
	ntfdomain "github.com/ecrespo/umbral/internal/notify/domain"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
	secports "github.com/ecrespo/umbral/internal/security/ports"
	"github.com/ecrespo/umbral/internal/store"
	"github.com/ecrespo/umbral/internal/tools/adapters/builtin"
	"github.com/ecrespo/umbral/internal/tools/adapters/registry"
)

// TestTheDaemonLoadsLocalRules_REQ_SEC_010: a file in `rules/` beside config.toml reaches
// policy.explain and redaction through the one rule set the daemon builds at start.
func TestTheDaemonLoadsLocalRules_REQ_SEC_010(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	settingsPath := filepath.Join(t.TempDir(), "umbral", "config.toml")
	dir := rulesDir(settingsPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "acme.json"), []byte(`{"redaction": [{"name": "acme_token", "pattern": "acme_[0-9]{8}"}],
		"destructive": [{"name": "nuke", "pattern": "^nuke( |$)"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	active := loadRules(slog.New(slog.NewTextHandler(io.Discard, nil)), settingsPath)

	db, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "umbral.db")})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	tools, err := registry.New(builtin.All(builtin.Config{})...)
	if err != nil {
		t.Fatal(err)
	}
	e, err := newExplainer(db, tools, active.Policy)
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.Explain(ctx, secdomain.ExplainRequest{Mode: secdomain.ModeAutoEdit, Tool: "run_command", Risk: secdomain.RiskExec, Target: "nuke build"})
	if err != nil || d.Step != secdomain.StepDestructive || d.Reason != secdomain.ReasonDestructive {
		t.Errorf("explain = %s by %s (%v), want the local destructive pattern to decide", d.Verdict, d.Step, err)
	}
	if got := active.Redact("key acme_12345678 and AKIAIOSFODNN7EXAMPLE").Text; strings.Contains(got, "acme_12345678") || strings.Contains(got, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("redaction = %q, want the local and the built-in rules applied", got)
	}
}

// TestRuleRecoveryEndToEnd_REQ_SEC_016: through the daemon's own wiring and SQLite, a key is
// added, two bundles are applied, every key is removed with force — which fails updates
// closed and discards what they verified — and rules.reset still works, offline and with no
// key; the local override stays on top throughout and a restart keeps the outcome. A bundle
// signed by no trusted key is rejected with its reason in egress_log and on the bus.
func TestRuleRecoveryEndToEnd_REQ_SEC_016(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	settingsPath := filepath.Join(t.TempDir(), "umbral", "config.toml")
	dir := rulesDir(settingsPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	const localRules = `{"destructive": [{"name": "local_nuke", "pattern": "^nuke( |$)"}]}`
	if err := os.WriteFile(filepath.Join(dir, "local.json"), []byte(localRules), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "umbral.db")
	db, err := store.Open(ctx, store.Options{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	egress, err := egresslog.New(db)
	if err != nil {
		t.Fatal(err)
	}
	events := bus.New()
	t.Cleanup(events.Close)
	rejected := events.Subscribe(secports.KindRuleUpdateRejected)
	// rules_check on, towards a port nothing listens on: the manager is real, the network is not.
	settings := config.Settings{RulesCheck: true, RulesURL: "https://127.0.0.1:1/rules.json"}
	active := loadRules(logger, settingsPath)
	manager, err := startRules(ctx, logger, db, settings, egress, func(s string) string { return active.Redact(s).Text }, active, events, nil)
	if err != nil {
		t.Fatal(err)
	}
	flags := func(cmd string) bool {
		_, ok := secdomain.MatchDestructive(active.Policy().Destructive, cmd)
		return ok
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AddKey(ctx, base64.StdEncoding.EncodeToString(pub), secdomain.Fingerprint(pub)); err != nil {
		t.Fatal(err)
	}
	for v := 1; v <= 2; v++ {
		bundle := fmt.Appendf(nil, `{"version": %d, "destructive": [{"name": "v%d", "pattern": "^v%d( |$)"}]}`, v, v, v)
		sig, err := secdomain.SignBundle(priv, bundle, nil)
		if err != nil {
			t.Fatal(err)
		}
		manager.Offer(ctx, bundle, sig)
	}
	if !flags("v2 x") || flags("v1 x") || flags("rm -rf /") || !flags("nuke x") {
		t.Fatal("the second bundle is not the base under the local rule")
	}

	_, strangerPriv, _ := ed25519.GenerateKey(rand.Reader)
	evil := []byte(`{"version": 9}`)
	evilSig, _ := secdomain.SignBundle(strangerPriv, evil, nil)
	manager.Offer(ctx, evil, evilSig)
	select {
	case ev := <-rejected.C():
		if e, ok := ev.(secports.RuleUpdateRejected); !ok || e.Reason != secdomain.RejectUnknownKey {
			t.Errorf("announced %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Error("no rules.update_rejected on the bus")
	}
	var rows int
	if err := db.DB().QueryRowContext(ctx, `SELECT count(*) FROM egress_log WHERE provider = 'umbral-rules:rejected:unknown_key'`).Scan(&rows); err != nil || rows != 1 {
		t.Errorf("the rejection's egress row: %d %v", rows, err)
	}

	// Every key removed with force: updates fail closed, the bundles it verified go.
	if _, _, err := manager.RemoveKey(ctx, secdomain.Fingerprint(pub), false); !errors.Is(err, secdomain.ErrLastValidKey) {
		t.Fatalf("removing the last key without force: %v", err)
	}
	_, now, err := manager.RemoveKey(ctx, secdomain.Fingerprint(pub), true)
	if err != nil || now.Source != secdomain.SourceBuiltin {
		t.Fatalf("remove --force: active %+v %v", now, err)
	}
	st, err := manager.Reset(ctx)
	if err != nil || st.Active.Source != secdomain.SourceBuiltin || st.RemoteUpdates != secdomain.UpdatesFailClosed || !st.LocalOverride {
		t.Fatalf("reset with no key: %+v %v", st, err)
	}
	if !flags("rm -rf /") || flags("v2 x") || !flags("nuke x") {
		t.Error("after the reset, the built-in rules with the local one on top are not in force")
	}

	// Nothing above wrote to the override directory (REQ-SEC-016).
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Errorf("the override directory changed: %v %v", entries, err)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "local.json")); err != nil || string(raw) != localRules {
		t.Errorf("the local rule file changed: %q %v", raw, err)
	}

	// A restart keeps the outcome: built-in rules, failed closed, the key revoked.
	cancel()
	ctx2 := t.Context()
	active2 := loadRules(logger, settingsPath)
	again, err := startRules(ctx2, logger, db, settings, egress, func(s string) string { return active2.Redact(s).Text }, active2, events, nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err = again.Status(ctx2)
	if err != nil || st.RemoteUpdates != secdomain.UpdatesFailClosed || st.Active.Source != secdomain.SourceBuiltin || len(st.TrustKeys) != 1 || st.TrustKeys[0].RevokedAt == nil {
		t.Errorf("after a restart: %+v %v", st, err)
	}
}

type capturedNotices struct {
	mu   sync.Mutex
	seen []ntfdomain.Notification
}

func (c *capturedNotices) DeliverForeground(n ntfdomain.Notification) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen = append(c.seen, n)
	return true
}

// TestARejectedBundleNotifiesTheUser_REQ_SEC_013: a rejection raises a user notification
// through the notification sink under the daemon's own `umbral:rules` source, naming the
// reason — the broadcast alone reaches no person (delta `2026-10-signed-rule-bundles`).
func TestARejectedBundleNotifiesTheUser_REQ_SEC_013(t *testing.T) {
	t.Parallel()
	sink, err := notify.New(notify.Config{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	client := &capturedNotices{}
	sink.SetDeliverer(client)
	events := bus.New()
	t.Cleanup(events.Close)
	sub := events.Subscribe(secports.KindRuleUpdateRejected)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		notifyRuleRejections(ctx, sub, sink, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()
	events.Publish(secports.RuleUpdateRejected{Reason: secdomain.RejectDowngrade, Version: 3, Source: secdomain.SourceRemote})

	deadline := time.Now().Add(5 * time.Second)
	for {
		client.mu.Lock()
		n := len(client.seen)
		client.mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.seen) != 1 {
		t.Fatalf("notifications: %+v", client.seen)
	}
	got := client.seen[0]
	if got.Source != "umbral:rules" || !strings.Contains(got.Body, "downgrade") || got.Title == "" {
		t.Errorf("notification: %+v", got)
	}
}
