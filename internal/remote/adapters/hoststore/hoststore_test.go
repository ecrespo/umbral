package hoststore

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/ecrespo/umbral/internal/remote/domain"
	"github.com/ecrespo/umbral/internal/store"
)

func open(t *testing.T) (*Store, *store.Store) {
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
	return s, db
}

func TestHostsRoundTrip_REQ_RMT_001(t *testing.T) {
	s, _ := open(t)
	for _, h := range []domain.Host{
		{Name: "nas", URL: "ssh://me@nas", CreatedAt: 2},
		{Name: "build-01", URL: "ssh://build:2222", CreatedAt: 1},
	} {
		if err := s.Insert(t.Context(), h); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Insert(t.Context(), domain.Host{Name: "nas", URL: "ssh://other", CreatedAt: 3}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("a second nas = %v, want ErrConflict", err)
	}
	got, err := s.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "build-01" || got[1] != (domain.Host{Name: "nas", URL: "ssh://me@nas", CreatedAt: 2}) {
		t.Fatalf("List = %+v", got)
	}
	if err := s.Delete(t.Context(), "nas"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(t.Context(), "nas"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a second delete = %v, want ErrNotFound", err)
	}
}

// The table holds what the application checks, so a row written around it still cannot carry a
// name that would split an id or a URL that is not ssh's.
func TestMigration0009Constraints_REQ_RMT_001(t *testing.T) {
	_, db := open(t)
	for _, row := range []struct{ name, url string }{
		{"Nas", "ssh://nas"},
		{"9nas", "ssh://nas"},
		{"nas/x", "ssh://nas"},
		{"nas_x", "ssh://nas"},
		{"", "ssh://nas"},
		{"abcdefghijklmnopqrstuvwxyzabcdefg", "ssh://nas"},
		{"nas", "tcp://nas:7000"},
		{"nas", "ssh://"},
	} {
		if _, err := db.DB().ExecContext(t.Context(), `INSERT INTO remotes(name, url, created_at) VALUES (?, ?, 1)`, row.name, row.url); err == nil {
			t.Errorf("remotes accepted name %q url %q", row.name, row.url)
		}
	}
	if _, err := db.DB().ExecContext(t.Context(), `INSERT INTO remotes(name, url, created_at) VALUES ('nas-2', 'ssh://me@nas', 1)`); err != nil {
		t.Fatalf("a valid row: %v", err)
	}
}
