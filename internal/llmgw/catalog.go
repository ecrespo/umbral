// Package llmgw is the model gateway's core: the catalog of configured providers and their
// models (REQ-LLM-002), and — from T-F1-07 — the router that walks candidates. It orchestrates
// the module's ports and lives outside domain, which must stay pure, and outside adapters,
// which must stay replaceable (the same shape as sessions and workspaces).
package llmgw

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ecrespo/umbral/internal/llmgw/domain"
	"github.com/ecrespo/umbral/internal/llmgw/ports"
)

// Entry is one provider of models.toml as the daemon runs it. Provider is nil when it may not
// run — a credential it could not resolve, or a type with no adapter — and Health and Reason
// then say why; they come from credential resolution (delta `2026-09-provider-config`).
type Entry struct {
	ID       string
	Provider ports.Provider
	Health   domain.Health
	Reason   string
}

// Catalog is the configured providers and the models they serve.
type Catalog struct {
	store  ports.ModelStore
	logger *slog.Logger
	now    func() time.Time

	// refresh serialises refreshes: two model.list calls with refresh must not interleave
	// their Replace calls.
	refresh sync.Mutex

	mu      sync.RWMutex
	offline bool
	entries map[string]Entry
	// failed holds the providers whose last discovery failed.
	failed map[string]bool
}

// NewCatalog builds an empty catalog over its store.
func NewCatalog(store ports.ModelStore, logger *slog.Logger) *Catalog {
	return &Catalog{
		store: store, logger: logger, now: time.Now,
		entries: map[string]Entry{}, failed: map[string]bool{},
	}
}

// Configure replaces the providers, at start and on config.reload, and whether the router is
// offline. It does not contact any; Refresh does. A provider that survives the reload keeps
// its last discovery failure until the next refresh says otherwise.
func (c *Catalog) Configure(offline bool, entries []Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.offline = offline
	c.entries = make(map[string]Entry, len(entries))
	for _, e := range entries {
		c.entries[e.ID] = e
	}
	for id := range c.failed {
		if _, ok := c.entries[id]; !ok {
			delete(c.failed, id)
		}
	}
}

// offlineLocked reports whether e may not be contacted because the router is offline and
// the provider is remote (REQ-LLM-004).
func (c *Catalog) offlineLocked(e Entry) bool {
	return c.offline && e.Provider != nil && !e.Provider.Local()
}

// Refresh discovers the models of every provider that may run and stores them (REQ-LLM-002).
// A provider that may not run is never contacted: its stored models are marked down
// (REQ-SEC-008), and so are a remote provider's while the router is offline (REQ-LLM-004).
// The models of providers no longer configured are deleted. A provider that does not answer keeps the models it listed before, marked
// down, and the refresh carries on with the others; the error joins every failure.
func (c *Catalog) Refresh(ctx context.Context) error {
	c.refresh.Lock()
	defer c.refresh.Unlock()

	c.mu.RLock()
	entries := make([]Entry, 0, len(c.entries))
	keep := make([]string, 0, len(c.entries))
	offline := map[string]bool{}
	for id, e := range c.entries {
		entries = append(entries, e)
		keep = append(keep, id)
		offline[id] = c.offlineLocked(e)
	}
	c.mu.RUnlock()

	var errs []error
	if err := c.store.Retain(ctx, keep); err != nil {
		errs = append(errs, err)
	}
	for _, e := range entries {
		if offline[e.ID] {
			if err := c.store.SetHealth(ctx, e.ID, domain.HealthDown); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if e.Provider == nil {
			health := e.Health
			if health != domain.HealthDown {
				health = domain.HealthUnknown
			}
			if err := c.store.SetHealth(ctx, e.ID, health); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		models, err := e.Provider.Models(ctx)
		if err != nil {
			c.setFailed(e.ID, true)
			c.logger.Warn("a provider did not list its models; they are marked down",
				slog.String("provider", e.ID), slog.Any("error", err))
			if serr := c.store.SetHealth(ctx, e.ID, domain.HealthDown); serr != nil {
				errs = append(errs, serr)
			}
			errs = append(errs, fmt.Errorf("provider %s: %w", e.ID, err))
			continue
		}
		c.setFailed(e.ID, false)
		health := domain.HealthOK
		if e.Health == domain.HealthDegraded {
			health = domain.HealthDegraded
		}
		stamp := c.now().UnixMilli()
		for i := range models {
			models[i].Provider, models[i].Health, models[i].UpdatedAt = e.ID, health, stamp
		}
		if err := c.store.Replace(ctx, e.ID, models); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c *Catalog) setFailed(id string, failed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if failed {
		c.failed[id] = true
	} else {
		delete(c.failed, id)
	}
}

// List is model.list (API Spec §5.26): the stored models of the configured providers, each
// with its provider's reason. With refresh, it discovers first; a discovery failure is
// reported in the models' health, not as an error of the list.
func (c *Catalog) List(ctx context.Context, refresh bool) ([]domain.Model, error) {
	if refresh {
		_ = c.Refresh(ctx)
	}
	stored, err := c.store.List(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]domain.Model, 0, len(stored))
	for _, m := range stored {
		e, ok := c.entries[m.Provider]
		if !ok {
			continue
		}
		switch {
		case c.offlineLocked(e):
			m.Health, m.Reason = domain.HealthDown, domain.ReasonOffline
		case e.Provider == nil:
			m.Health, m.Reason = e.Health, e.Reason
		case c.failed[m.Provider]:
			m.Health, m.Reason = domain.HealthDown, domain.ReasonDiscoveryFailed
		default:
			m.Reason = e.Reason
		}
		out = append(out, m)
	}
	return out, nil
}

// Provider returns a provider that may be called: configured, with an adapter and a
// credential, not failing discovery, and not remote while the router is offline.
func (c *Catalog) Provider(id string) (ports.Provider, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[id]
	if !ok || e.Provider == nil || c.failed[id] || c.offlineLocked(e) {
		return nil, false
	}
	return e.Provider, true
}
