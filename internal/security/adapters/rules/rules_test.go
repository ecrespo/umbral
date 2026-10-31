package rules

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	llmdomain "github.com/ecrespo/umbral/internal/llmgw/domain"
	"github.com/ecrespo/umbral/internal/security/domain"
)

func writeRule(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestLocalRulesWin_REQ_SEC_010: valid files in the override directory are loaded over the
// rules built into the binary and win over them; an invalid file is ignored with a warning
// naming it, and the rest still apply — never an empty rule set.
func TestLocalRulesWin_REQ_SEC_010(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeRule(t, dir, "10-acme.json", `{"redaction": [{"name": "acme_token", "pattern": "acme_[0-9]{8}"}],
		"destructive": [{"name": "nuke", "pattern": "^nuke( |$)"}]}`)
	writeRule(t, dir, "20-broken.json", `{"redaction": [{"name": "x", "pattern": "("}]}`)
	writeRule(t, dir, "README.md", `not a rule file`)
	if err := os.Mkdir(filepath.Join(dir, "sub.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A link is not read, wherever it points: only regular files of the directory count.
	elsewhere := filepath.Join(t.TempDir(), "linked.json")
	writeRule(t, filepath.Dir(elsewhere), "linked.json", `{"destructive": [{"name": "linked", "pattern": "^linked( |$)"}]}`)
	if err := os.Symlink(elsewhere, filepath.Join(dir, "30-link.json")); err != nil {
		t.Fatal(err)
	}
	writeRule(t, dir, "40-huge.json", `{"redaction": []}`+strings.Repeat(" ", MaxRuleFileBytes))

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	local := LoadDir(logger, dir)
	active := NewActive(local)

	if name, ok := domain.MatchDestructive(active.Policy().Destructive, "nuke everything"); !ok || name != "nuke" {
		t.Errorf("the local destructive pattern does not apply: %q %v", name, ok)
	}
	if _, ok := domain.MatchDestructive(active.Policy().Destructive, "rm -rf build"); !ok {
		t.Error("a built-in pattern was lost: the local file must add to the built-ins, not replace them")
	}
	if got := active.Redact("key acme_12345678 sk-ant-" + strings.Repeat("a", 30)).Text; !strings.Contains(got, "[REDACTED:acme_token]") || !strings.Contains(got, "[REDACTED:anthropic_key]") {
		t.Errorf("redaction = %q, want both the local and the built-in rule applied", got)
	}
	if !strings.Contains(logs.String(), "20-broken.json") || !strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("the invalid file was not warned about:\n%s", logs.String())
	}
	if name, _ := domain.MatchDestructive(active.Policy().Destructive, "linked x"); name == "linked" || strings.Contains(logs.String(), "30-link.json") {
		t.Errorf("a link was followed:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "40-huge.json") || !strings.Contains(logs.String(), "over 1048576 bytes") {
		t.Errorf("an oversized file was not warned about with its reason:\n%s", logs.String())
	}

	// Every file invalid, or no directory at all: the built-in rules, never an empty set.
	only := t.TempDir()
	writeRule(t, only, "bad.json", `{`)
	for name, set := range map[string]domain.RuleSet{
		"every file invalid": LoadDir(logger, only),
		"no directory":       LoadDir(logger, filepath.Join(only, "missing")),
	} {
		got := NewActive(set).Policy().Destructive
		if len(got) != len(domain.DefaultDestructivePatterns) {
			t.Errorf("%s: %d destructive patterns, want the %d built-in", name, len(got), len(domain.DefaultDestructivePatterns))
		}
	}
}

// TestALocalRuleStaysOverABundle_REQ_SEC_011: a bundle replaces the built-in rules — a name it
// leaves out is gone — and never the local directory, which stays on top of whatever base is
// in force (DD-016, delta `2026-10-rule-overrides` decision 2).
func TestALocalRuleStaysOverABundle_REQ_SEC_011(t *testing.T) {
	t.Parallel()
	local, err := domain.ParseRules([]byte(`{"destructive": [{"name": "nuke", "pattern": "^nuke( |$)"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := domain.ParseRules([]byte(`{"version": 7, "destructive": [{"name": "nuke", "pattern": "^never$"}, {"name": "boom", "pattern": "^boom( |$)"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	active := NewActive(local.Rules)
	active.SetBase(bundle.Rules)
	if _, ok := domain.MatchDestructive(active.Policy().Destructive, "nuke it"); !ok {
		t.Error("the bundle's nuke displaced the local one")
	}
	if _, ok := domain.MatchDestructive(active.Policy().Destructive, "boom"); !ok {
		t.Error("the bundle's own pattern does not apply")
	}
	// Matched by name: on Windows every line is also the floor's windows_shell.
	if name, ok := domain.MatchDestructive(active.Policy().Destructive, "rm -rf build"); ok && name != domain.UnknownWindowsShell {
		t.Error("a built-in pattern the bundle leaves out still applies; a bundle replaces the built-ins")
	}
}

type recordingEgress struct {
	mu   sync.Mutex
	recs []llmdomain.EgressRecord
}

func (r *recordingEgress) Record(_ context.Context, rec llmdomain.EgressRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recs = append(r.recs, rec)
	return nil
}

type offered struct {
	bundle, sig []byte
}

type recordingUpdater struct {
	mu  sync.Mutex
	got []offered
}

func (r *recordingUpdater) Offer(_ context.Context, bundle, sig []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, offered{bundle, sig})
}

// TestRuleUpdateOptInAndLogged_REQ_SEC_011: with rules_check off nothing is requested; on, the
// bundle and its signature are fetched, each request is recorded in egress_log, and what came
// back is handed to the verifier — never applied by the fetcher itself.
func TestRuleUpdateOptInAndLogged_REQ_SEC_011(t *testing.T) {
	t.Parallel()
	var hits atomic.Int64
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/rules.json":
			_, _ = w.Write([]byte(`{"version": 1}`))
		case "/rules.json.sig":
			_, _ = w.Write([]byte(`{"signatures": []}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	newFetcher := func(enabled bool, egress *recordingEgress, up *recordingUpdater) *Fetcher {
		f, err := NewFetcher(FetchConfig{
			Enabled: enabled, URL: srv.URL + "/rules.json", Client: srv.Client(),
			Egress: egress, Updater: up, Logger: slog.New(slog.DiscardHandler),
		})
		if err != nil {
			t.Fatal(err)
		}
		return f
	}

	offEgress, offUp := &recordingEgress{}, &recordingUpdater{}
	off := newFetcher(false, offEgress, offUp)
	off.Check(t.Context())
	off.Run(t.Context()) // returns at once when off
	if hits.Load() != 0 || len(offEgress.recs) != 0 || len(offUp.got) != 0 {
		t.Fatalf("rules_check off: %d requests, %d egress rows, %d offers; want none", hits.Load(), len(offEgress.recs), len(offUp.got))
	}

	egress, up := &recordingEgress{}, &recordingUpdater{}
	newFetcher(true, egress, up).Check(t.Context())
	if hits.Load() != 2 {
		t.Errorf("%d requests, want the bundle and its signature", hits.Load())
	}
	if len(egress.recs) != 2 {
		t.Fatalf("%d egress rows, want one per request", len(egress.recs))
	}
	for _, rec := range egress.recs {
		if rec.Provider != EgressProvider || rec.Host == "" || len(rec.PayloadSHA256) != 64 || rec.CreatedAt == 0 {
			t.Errorf("egress row %+v", rec)
		}
	}
	if len(up.got) != 1 || string(up.got[0].bundle) != `{"version": 1}` || string(up.got[0].sig) != `{"signatures": []}` {
		t.Errorf("offered %+v", up.got)
	}
}

// TestAFailedFetchOffersNothing_REQ_SEC_011: a fetch that fails is logged and retried at the
// next check (DD-016); it is not a rejected bundle, so nothing reaches the verifier.
func TestAFailedFetchOffersNothing_REQ_SEC_011(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	egress, up := &recordingEgress{}, &recordingUpdater{}
	f, err := NewFetcher(FetchConfig{
		Enabled: true, URL: srv.URL + "/rules.json", Client: srv.Client(),
		Egress: egress, Updater: up, Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	f.Check(t.Context())
	if len(up.got) != 0 {
		t.Errorf("a 404 offered %+v", up.got)
	}
	if len(egress.recs) == 0 {
		t.Error("the request that failed was not recorded; it still left the machine")
	}
}

// TestARedirectIsRecordedBeforeItIsFollowed_REQ_SEC_011: the default rules_url redirects to
// another host, and every hop is an egress of its own, so each is recorded before it is
// followed (Art. 4). A hop that leaves https is not followed at all.
func TestARedirectIsRecordedBeforeItIsFollowed_REQ_SEC_011(t *testing.T) {
	t.Parallel()
	egress := &recordingEgress{}
	var assetHosts []int
	asset := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		egress.mu.Lock()
		assetHosts = append(assetHosts, len(egress.recs))
		egress.mu.Unlock()
		_, _ = w.Write([]byte(`{"version": 1}`))
	}))
	t.Cleanup(asset.Close)
	var plainHits atomic.Int64
	plain := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { plainHits.Add(1) }))
	t.Cleanup(plain.Close)
	release := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest/rules.json", "/latest/rules.json.sig":
			http.Redirect(w, r, asset.URL+"/asset"+r.URL.Path, http.StatusFound)
		default:
			http.Redirect(w, r, plain.URL+r.URL.Path, http.StatusFound)
		}
	}))
	t.Cleanup(release.Close)

	up := &recordingUpdater{}
	f, err := NewFetcher(FetchConfig{
		Enabled: true, URL: release.URL + "/latest/rules.json", Client: release.Client(),
		Egress: egress, Updater: up, Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	f.Check(t.Context())
	if len(up.got) != 1 {
		t.Fatalf("offered %d, want the redirected bundle", len(up.got))
	}
	assetHost := strings.TrimPrefix(asset.URL, "https://")
	var toAsset int
	for _, rec := range egress.recs {
		if rec.Host == assetHost {
			toAsset++
		}
	}
	if len(egress.recs) != 4 || toAsset != 2 {
		t.Fatalf("egress rows %+v, want the release and the asset host for each of two requests", egress.recs)
	}
	// Each request to the asset host found its own row already written.
	if len(assetHosts) != 2 || assetHosts[0] != 2 || assetHosts[1] != 4 {
		t.Errorf("rows written when the asset host was reached: %v, want 2 then 4", assetHosts)
	}

	egress2, up2 := &recordingEgress{}, &recordingUpdater{}
	f2, err := NewFetcher(FetchConfig{
		Enabled: true, URL: release.URL + "/elsewhere/rules.json", Client: release.Client(),
		Egress: egress2, Updater: up2, Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	f2.Check(t.Context())
	if plainHits.Load() != 0 || len(up2.got) != 0 {
		t.Errorf("a redirect to http was followed: %d hits, %d offers", plainHits.Load(), len(up2.got))
	}
}

// TestFiveRedirectsAndNoMore_REQ_SEC_011: a chain of five redirects is followed, a sixth is
// not (delta `2026-10-rule-overrides`, decision 6).
func TestFiveRedirectsAndNoMore_REQ_SEC_011(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /hops/N/rules.json redirects N times before answering.
		var n int
		var rest string
		if _, err := fmt.Sscanf(r.URL.Path, "/hops/%d/%s", &n, &rest); err != nil {
			http.NotFound(w, r)
			return
		}
		if n > 0 {
			http.Redirect(w, r, fmt.Sprintf("/hops/%d/%s", n-1, rest), http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(`{"version": 1}`))
	}))
	t.Cleanup(srv.Close)
	for hops, want := range map[int]int{5: 1, 6: 0} {
		up := &recordingUpdater{}
		f, err := NewFetcher(FetchConfig{
			Enabled: true, URL: fmt.Sprintf("%s/hops/%d/rules.json", srv.URL, hops), Client: srv.Client(),
			Egress: &recordingEgress{}, Updater: up, Logger: slog.New(slog.DiscardHandler),
		})
		if err != nil {
			t.Fatal(err)
		}
		f.Check(t.Context())
		if len(up.got) != want {
			t.Errorf("%d redirects: %d offers, want %d", hops, len(up.got), want)
		}
	}
}

// TestAURLCarryingASecretIsRefused_REQ_SEC_011: Art. 4 puts every request through redaction,
// so a rules_url the active redactor would change — a token in its query — is refused before
// anything leaves; with rules_check off the URL is never looked at.
func TestAURLCarryingASecretIsRefused_REQ_SEC_011(t *testing.T) {
	t.Parallel()
	redact := func(s string) string { return domain.Redact(s).Text }
	leaky := "https://rules.example/rules.json?key=sk-ant-" + strings.Repeat("a", 30)
	if _, err := NewFetcher(FetchConfig{
		Enabled: true, URL: leaky, Redact: redact,
		Egress: &recordingEgress{}, Updater: &recordingUpdater{},
	}); err == nil || strings.Contains(err.Error(), "sk-ant-") {
		t.Errorf("a URL carrying a secret: %v, want refused without echoing it", err)
	}
	if _, err := NewFetcher(FetchConfig{
		Enabled: false, URL: leaky, Redact: redact,
		Egress: &recordingEgress{}, Updater: &recordingUpdater{},
	}); err != nil {
		t.Errorf("rules_check off: %v, want no check of a URL never used", err)
	}
	if _, err := NewFetcher(FetchConfig{
		Enabled: true, URL: "https://rules.example/rules.json", Redact: redact,
		Egress: &recordingEgress{}, Updater: &recordingUpdater{},
	}); err != nil {
		t.Errorf("a clean URL: %v", err)
	}
}

// TestAFailedFetchLogsTheHostOnly_REQ_SEC_011: a redirect target can carry short-lived signed
// query parameters, so a failure is logged by host, never by URL (Art. 7).
func TestAFailedFetchLogsTheHostOnly_REQ_SEC_011(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://127.0.0.1:1/asset?X-Amz-Signature=deadbeefcafe", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	var logs bytes.Buffer
	f, err := NewFetcher(FetchConfig{
		Enabled: true, URL: srv.URL + "/rules.json", Client: srv.Client(),
		Egress: &recordingEgress{}, Updater: &recordingUpdater{}, Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	f.Check(t.Context())
	if !strings.Contains(logs.String(), "level=WARN") || strings.Contains(logs.String(), "deadbeefcafe") {
		t.Errorf("the failure log carries the signed URL or nothing:\n%s", logs.String())
	}
}

// TestAFailedClosedStoreFetchesNothing_REQ_SEC_015: with remote updates failed closed the
// fetcher makes no request at all, so nothing leaves the machine until a key is added.
func TestAFailedClosedStoreFetchesNothing_REQ_SEC_015(t *testing.T) {
	t.Parallel()
	var hits atomic.Int64
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	t.Cleanup(srv.Close)
	var open atomic.Bool
	egress := &recordingEgress{}
	f, err := NewFetcher(FetchConfig{
		Enabled: true, URL: srv.URL + "/rules.json", Client: srv.Client(),
		Allowed: func(context.Context) bool { return open.Load() },
		Egress:  egress, Updater: &recordingUpdater{}, Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	f.Check(t.Context())
	if hits.Load() != 0 || len(egress.recs) != 0 {
		t.Fatalf("failed closed: %d requests, %d egress rows", hits.Load(), len(egress.recs))
	}
	open.Store(true)
	f.Check(t.Context())
	if hits.Load() == 0 {
		t.Error("reopened, the fetcher still fetches nothing")
	}
}
