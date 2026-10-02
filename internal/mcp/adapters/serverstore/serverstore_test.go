package serverstore

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/ecrespo/umbral/internal/mcp/domain"
	"github.com/ecrespo/umbral/internal/store"
)

func open(t *testing.T) *Store {
	t.Helper()
	db, err := store.Open(t.Context(), store.Options{Path: filepath.Join(t.TempDir(), "umbral.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestServersRoundTrip_REQ_MCP_001: a server persists with its command, args, env references
// and trust, its state changes are kept, and a name is unique.
func TestServersRoundTrip_REQ_MCP_001(t *testing.T) {
	s := open(t)
	in := domain.Server{
		ID: store.NewID(store.PrefixMcpServer), Name: "gitlab", Transport: domain.TransportStdio,
		Command: "npx", Args: []string{"-y", "@x/gitlab-mcp"}, EnvRefs: map[string]string{"TOKEN": "keyring:umbral/gl"},
		Trust: domain.TrustUntrusted, State: domain.StateConnecting, CreatedAt: 1, UpdatedAt: 1,
	}
	if err := s.Insert(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	web := domain.Server{
		ID: store.NewID(store.PrefixMcpServer), Name: "web", Transport: domain.TransportHTTP, URL: "https://x/mcp",
		Trust: domain.TrustTrusted, State: domain.StateConnecting, CreatedAt: 2, UpdatedAt: 2,
	}
	if err := s.Insert(t.Context(), web); err != nil {
		t.Fatal(err)
	}
	dup := in
	dup.ID = store.NewID(store.PrefixMcpServer)
	if err := s.Insert(t.Context(), dup); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("a second gitlab: %v", err)
	}
	if err := s.SetState(t.Context(), in.ID, domain.StateUnavailable, "the connection ended", 5); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(t.Context())
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %+v %v", list, err)
	}
	got := list[0]
	if got.Name != "gitlab" || got.Command != "npx" || len(got.Args) != 2 || got.Args[1] != "@x/gitlab-mcp" ||
		got.EnvRefs["TOKEN"] != "keyring:umbral/gl" || got.State != domain.StateUnavailable ||
		got.LastError != "the connection ended" || got.UpdatedAt != 5 || got.Trust != domain.TrustUntrusted {
		t.Fatalf("gitlab = %+v", got)
	}
	if list[1].URL != "https://x/mcp" || list[1].Trust != domain.TrustTrusted {
		t.Fatalf("web = %+v", list[1])
	}
	if err := s.Delete(t.Context(), "gitlab"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(t.Context(), "gitlab"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("deleting it again: %v", err)
	}
}
