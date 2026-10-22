package modelstore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ecrespo/umbral/internal/llmgw/domain"
	"github.com/ecrespo/umbral/internal/store"
)

func open(t *testing.T) *Store {
	t.Helper()
	st, err := store.Open(context.Background(), store.Options{Path: filepath.Join(t.TempDir(), "umbral.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s, err := New(st)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestTheCatalogPersists_REQ_LLM_002: the catalog lives in the `models` table (Data Model
// §2.10): a provider's models are replaced as a set, capabilities and prices round-trip, and
// health is set per provider without touching another's.
func TestTheCatalogPersists_REQ_LLM_002(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := open(t)
	kimi := domain.Model{
		ID: "or/moonshotai/kimi-k2", Provider: "or",
		Caps:                   domain.Capabilities{Tools: true, Vision: true, Reasoning: true, JSONSchema: true, ContextWindow: 131072},
		PriceInMicroUSDPerMTok: 570_000, PriceOutMicroUSDPerMTok: 2_300_000,
		Health: domain.HealthDegraded, UpdatedAt: 1_757_000_000_000,
	}
	if err := s.Replace(ctx, "or", []domain.Model{kimi, {ID: "or/x", Provider: "or", Health: domain.HealthOK, UpdatedAt: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(ctx, "ollama", []domain.Model{{ID: "ollama/qwen3:4b", Provider: "ollama", Local: true, Health: domain.HealthOK, UpdatedAt: 2}}); err != nil {
		t.Fatal(err)
	}

	got, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != "ollama/qwen3:4b" || got[1] != kimi {
		t.Fatalf("list = %+v, want three in id order with kimi intact", got)
	}
	if !got[0].Local {
		t.Error("local did not round-trip")
	}

	if err := s.Replace(ctx, "or", []domain.Model{kimi}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHealth(ctx, "or", domain.HealthDown); err != nil {
		t.Fatal(err)
	}
	got, _ = s.List(ctx)
	if len(got) != 2 || got[1].Health != domain.HealthDown || got[0].Health != domain.HealthOK {
		t.Errorf("after replace and SetHealth = %+v, want or/x gone, kimi down, ollama ok", got)
	}
	if err := s.Retain(ctx, []string{"or"}); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.List(ctx); len(got) != 1 || got[0].Provider != "or" {
		t.Errorf("after Retain(or) = %+v, want only or's model", got)
	}
	if err := s.Retain(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.List(ctx); len(got) != 0 {
		t.Errorf("after Retain() = %+v, want nothing", got)
	}
	if err := s.SetHealth(ctx, "or", domain.Health("bogus")); err != nil {
		t.Errorf("SetHealth on no rows = %v", err)
	}
	_ = s.Replace(ctx, "or", []domain.Model{kimi})
	if err := s.SetHealth(ctx, "or", domain.Health("bogus")); err == nil {
		t.Error("a health the table refuses was accepted")
	}
	if _, err := New(nil); err == nil {
		t.Error("New(nil) accepted")
	}
}
