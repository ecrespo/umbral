package llmgw

import (
	"context"
	"errors"
	"io"
	"iter"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/llmgw/domain"
)

// fakeProvider lists what the test says and counts how often it was asked.
type fakeProvider struct {
	id     string
	local  bool
	models []string
	err    error

	mu    sync.Mutex
	calls int
}

func (p *fakeProvider) ID() string  { return p.id }
func (p *fakeProvider) Local() bool { return p.local }

func (p *fakeProvider) Models(context.Context) ([]domain.Model, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	out := make([]domain.Model, 0, len(p.models))
	for _, name := range p.models {
		out = append(out, domain.Model{ID: domain.QualifiedID(p.id, name), Provider: p.id, Local: p.local})
	}
	return out, nil
}

func (p *fakeProvider) Stream(context.Context, domain.Request) (iter.Seq2[domain.Event, error], error) {
	return nil, errors.New("not used")
}

func (p *fakeProvider) asked() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// memStore is the catalog's store in memory.
type memStore struct {
	mu   sync.Mutex
	rows map[string]domain.Model
}

func newMemStore() *memStore { return &memStore{rows: map[string]domain.Model{}} }

func (s *memStore) Replace(_ context.Context, provider string, models []domain.Model) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, m := range s.rows {
		if m.Provider == provider {
			delete(s.rows, id)
		}
	}
	for _, m := range models {
		s.rows[m.ID] = m
	}
	return nil
}

func (s *memStore) SetHealth(_ context.Context, provider string, h domain.Health) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, m := range s.rows {
		if m.Provider == provider {
			m.Health = h
			s.rows[id] = m
		}
	}
	return nil
}

func (s *memStore) Retain(_ context.Context, keep []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := map[string]bool{}
	for _, k := range keep {
		kept[k] = true
	}
	for id, m := range s.rows {
		if !kept[m.Provider] {
			delete(s.rows, id)
		}
	}
	return nil
}

func (s *memStore) List(context.Context) ([]domain.Model, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Model, 0, len(s.rows))
	for _, m := range s.rows {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func byID(models []domain.Model) map[string]domain.Model {
	out := map[string]domain.Model{}
	for _, m := range models {
		out[m.ID] = m
	}
	return out
}

func newCatalog(store *memStore) *Catalog {
	c := NewCatalog(store, quiet())
	c.now = func() time.Time { return time.UnixMilli(1_757_000_000_000) }
	return c
}

// TestCatalogDiscoversEveryProvider_REQ_LLM_002: a refresh asks every provider that may run
// for its models and stores them, `ok` once the provider has answered; model.list with
// refresh asks again, and a model a provider stopped listing is gone.
func TestCatalogDiscoversEveryProvider_REQ_LLM_002(t *testing.T) {
	t.Parallel()

	local := &fakeProvider{id: "ollama", local: true, models: []string{"qwen3:4b", "gpt-oss:20b"}}
	remote := &fakeProvider{id: "or", models: []string{"moonshotai/kimi-k2"}}
	store := newMemStore()
	c := newCatalog(store)
	c.Configure(false, []Entry{
		{ID: "ollama", Provider: local, Health: domain.HealthUnknown},
		{ID: "or", Provider: remote, Health: domain.HealthDegraded, Reason: "env_secret"},
	})
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	got, err := c.List(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	m := byID(got)
	if len(got) != 3 {
		t.Fatalf("catalog = %+v, want 3 models", got)
	}
	if q := m["ollama/qwen3:4b"]; q.Health != domain.HealthOK || !q.Local || q.UpdatedAt != 1_757_000_000_000 {
		t.Errorf("qwen3 = %+v, want ok, local, stamped", q)
	}
	if k := m["or/moonshotai/kimi-k2"]; k.Health != domain.HealthDegraded || k.Reason != "env_secret" {
		t.Errorf("kimi = %+v, want degraded / env_secret: its key came from the environment", k)
	}
	if local.asked() != 1 || remote.asked() != 1 {
		t.Errorf("providers asked %d and %d times, want once each", local.asked(), remote.asked())
	}

	if _, err := c.List(context.Background(), false); err != nil || local.asked() != 1 {
		t.Errorf("a list without refresh asked the provider (%d)", local.asked())
	}
	local.models = []string{"qwen3:4b"}
	got, _ = c.List(context.Background(), true)
	if local.asked() != 2 || remote.asked() != 2 {
		t.Errorf("refresh asked %d and %d times, want twice each", local.asked(), remote.asked())
	}
	if _, stale := byID(got)["ollama/gpt-oss:20b"]; stale || len(got) != 2 {
		t.Errorf("after refresh = %+v, want gpt-oss gone", got)
	}
}

// TestModelsOfADownProviderAreDown_REQ_SEC_008: a provider whose credential could not be
// resolved is never called — not even to list its models — and whatever the catalog held for
// it is listed `down` with the provider's reason (delta `2026-09-provider-config`, decision 8).
func TestModelsOfADownProviderAreDown_REQ_SEC_008(t *testing.T) {
	t.Parallel()

	hf := &fakeProvider{id: "hf", models: []string{"openai/gpt-oss-120b"}}
	store := newMemStore()
	c := newCatalog(store)
	c.Configure(false, []Entry{{ID: "hf", Provider: hf, Health: domain.HealthUnknown}})
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The next start finds no keyring.
	c.Configure(false, []Entry{{ID: "hf", Health: domain.HealthDown, Reason: "keyring_unavailable"}})
	got, err := c.List(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Health != domain.HealthDown || got[0].Reason != "keyring_unavailable" {
		t.Errorf("catalog = %+v, want the model down / keyring_unavailable", got)
	}
	if hf.asked() != 1 {
		t.Errorf("a provider without its key was asked %d times, want only the first, keyed, refresh", hf.asked())
	}
	if stored, _ := store.List(context.Background()); stored[0].Health != domain.HealthDown {
		t.Errorf("stored health = %s, want down: the table says so too", stored[0].Health)
	}
	if _, ok := c.Provider("hf"); ok {
		t.Error("a down provider is offered for calls")
	}
}

// TestAProviderThatDoesNotAnswerKeepsItsModelsDown: a failed discovery does not erase what the
// provider listed before; those models are down with `discovery_failed`, and the next answer
// brings them back. Other providers are unaffected, and the refresh reports the failure.
func TestAProviderThatDoesNotAnswerKeepsItsModelsDown(t *testing.T) {
	t.Parallel()

	flaky := &fakeProvider{id: "lms", local: true, models: []string{"m1"}}
	steady := &fakeProvider{id: "ollama", local: true, models: []string{"m2"}}
	store := newMemStore()
	c := newCatalog(store)
	c.Configure(false, []Entry{{ID: "lms", Provider: flaky, Health: domain.HealthUnknown}, {ID: "ollama", Provider: steady, Health: domain.HealthUnknown}})
	_ = c.Refresh(context.Background())

	flaky.err = &domain.ProviderError{Provider: "lms", Err: errors.New("connection refused")}
	if err := c.Refresh(context.Background()); err == nil {
		t.Error("a failed discovery was not reported")
	}
	m := byID(mustList(t, c))
	if f := m["lms/m1"]; f.Health != domain.HealthDown || f.Reason != domain.ReasonDiscoveryFailed {
		t.Errorf("lms/m1 = %+v, want down / discovery_failed", f)
	}
	if s := m["ollama/m2"]; s.Health != domain.HealthOK {
		t.Errorf("ollama/m2 = %+v, want ok", s)
	}
	if _, ok := c.Provider("lms"); ok {
		t.Error("a provider failing discovery is offered for calls")
	}
	if stored, _ := store.List(context.Background()); byID(stored)["lms/m1"].Health != domain.HealthDown {
		t.Error("the table still says lms/m1 is up")
	}

	flaky.err = nil
	_ = c.Refresh(context.Background())
	if f := byID(mustList(t, c))["lms/m1"]; f.Health != domain.HealthOK || f.Reason != "" {
		t.Errorf("lms/m1 after recovery = %+v, want ok", f)
	}
	if p, ok := c.Provider("lms"); !ok || p != flaky {
		t.Error("a recovered provider is not offered")
	}
}

// TestOnlyConfiguredProvidersAreListed: a provider removed from models.toml takes its models
// out of model.list, and one with no adapter yet is reported without being called.
func TestOnlyConfiguredProvidersAreListed(t *testing.T) {
	t.Parallel()

	gone := &fakeProvider{id: "old", models: []string{"m"}}
	store := newMemStore()
	c := newCatalog(store)
	c.Configure(false, []Entry{{ID: "old", Provider: gone, Health: domain.HealthUnknown}})
	_ = c.Refresh(context.Background())

	c.Configure(false, []Entry{{ID: "yz", Health: domain.HealthUnknown, Reason: domain.ReasonNoAdapter}})
	if err := c.Refresh(context.Background()); err != nil {
		t.Errorf("a provider with no adapter failed the refresh: %v", err)
	}
	if got := mustList(t, c); len(got) != 0 {
		t.Errorf("catalog = %+v, want nothing from a removed provider", got)
	}
	if _, ok := c.Provider("old"); ok {
		t.Error("a removed provider is still offered")
	}
}

func mustList(t *testing.T, c *Catalog) []domain.Model {
	t.Helper()
	got, err := c.List(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// TestOfflineContactsNoRemoteProvider_REQ_LLM_004: with `router.offline = true` a remote
// provider is not contacted at all — not even for its model list — and whatever the catalog
// held for it is listed `down` with reason `offline`; local providers are discovered as usual.
func TestOfflineContactsNoRemoteProvider_REQ_LLM_004(t *testing.T) {
	t.Parallel()

	remote := &fakeProvider{id: "or", models: []string{"kimi"}}
	local := &fakeProvider{id: "ollama", local: true, models: []string{"qwen"}}
	store := newMemStore()
	c := newCatalog(store)
	c.Configure(false, []Entry{{ID: "or", Provider: remote, Health: domain.HealthUnknown}, {ID: "ollama", Provider: local, Health: domain.HealthUnknown}})
	_ = c.Refresh(context.Background())

	c.Configure(true, []Entry{{ID: "or", Provider: remote, Health: domain.HealthUnknown}, {ID: "ollama", Provider: local, Health: domain.HealthUnknown}})
	got := byID(mustListRefresh(t, c))
	if remote.asked() != 1 || local.asked() != 2 {
		t.Errorf("asked remote %d and local %d times, want 1 and 2", remote.asked(), local.asked())
	}
	if k := got["or/kimi"]; k.Health != domain.HealthDown || k.Reason != domain.ReasonOffline {
		t.Errorf("or/kimi = %+v, want down / offline", k)
	}
	if q := got["ollama/qwen"]; q.Health != domain.HealthOK {
		t.Errorf("ollama/qwen = %+v, want ok", q)
	}
	if _, ok := c.Provider("or"); ok {
		t.Error("a remote provider is offered while offline")
	}
	if _, ok := c.Provider("ollama"); !ok {
		t.Error("a local provider is not offered while offline")
	}
}

// TestAReloadKeepsWhatItKnows: a reload that keeps a provider keeps its last discovery
// failure until the next refresh says otherwise, so its models are not `down` without a
// reason in between; a provider the reload removes loses its stored rows on the next refresh.
func TestAReloadKeepsWhatItKnows(t *testing.T) {
	t.Parallel()

	flaky := &fakeProvider{id: "lms", local: true, models: []string{"m"}, err: errors.New("refused")}
	old := &fakeProvider{id: "old", local: true, models: []string{"x"}}
	store := newMemStore()
	c := newCatalog(store)
	c.Configure(false, []Entry{{ID: "old", Provider: old, Health: domain.HealthUnknown}})
	_ = c.Refresh(context.Background())
	_ = store.Replace(context.Background(), "lms", []domain.Model{{ID: "lms/m", Provider: "lms", Health: domain.HealthOK}})
	c.Configure(false, []Entry{{ID: "lms", Provider: flaky, Health: domain.HealthUnknown}, {ID: "old", Provider: old, Health: domain.HealthUnknown}})
	_ = c.Refresh(context.Background())

	c.Configure(false, []Entry{{ID: "lms", Provider: flaky, Health: domain.HealthUnknown}})
	if m := byID(mustList(t, c))["lms/m"]; m.Health != domain.HealthDown || m.Reason != domain.ReasonDiscoveryFailed {
		t.Errorf("right after the reload lms/m = %+v, want down / discovery_failed", m)
	}
	_ = c.Refresh(context.Background())
	if stored, _ := store.List(context.Background()); len(byID(stored)) != 1 {
		t.Errorf("stored = %+v, want old's rows gone", stored)
	}
}

func mustListRefresh(t *testing.T, c *Catalog) []domain.Model {
	t.Helper()
	got, err := c.List(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
