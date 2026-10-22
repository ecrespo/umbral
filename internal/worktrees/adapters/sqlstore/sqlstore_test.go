package sqlstore

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/ecrespo/umbral/internal/store"
	"github.com/ecrespo/umbral/internal/worktrees/domain"
)

// TestWorktreeRowsRoundTrip_REQ_WT_001: a row is stored and read back whole, the thread's active
// worktree is found while it is active and not after, a second active row for one thread is
// refused, and an unknown id is ErrNotFound.
func TestWorktreeRowsRoundTrip_REQ_WT_001(t *testing.T) {
	t.Parallel()
	db, err := store.Open(t.Context(), store.Options{Path: filepath.Join(t.TempDir(), "umbral.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.DB().ExecContext(t.Context(), `INSERT INTO threads(id, cwd, created_at, updated_at) VALUES ('thr_a', '/repo', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	s := New(db.DB())
	ctx := t.Context()
	w := domain.Worktree{
		ID: store.NewID(store.PrefixWorktree), ThreadID: "thr_a", RepoRoot: "/repo", RepoID: "0123456789abcdef",
		BaseCommit: "abc", OriginBranch: "main", Branch: "umbral/thr_a", Path: "/data/wt/thr_a",
		State: domain.StateActive, CreatedAt: 5, UpdatedAt: 5,
	}
	if err := s.Insert(ctx, w); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(ctx, w.ID); err != nil || got != w {
		t.Fatalf("Get = %+v, %v; want %+v", got, err, w)
	}
	if got, ok, err := s.Active(ctx, "thr_a"); err != nil || !ok || got != w {
		t.Fatalf("Active = %+v, %v, %v", got, ok, err)
	}
	second := w
	second.ID, second.Path = store.NewID(store.PrefixWorktree), "/data/wt/other"
	if err := s.Insert(ctx, second); err == nil {
		t.Error("a second active worktree for one thread was stored")
	}
	if list, err := s.ListActive(ctx); err != nil || len(list) != 1 {
		t.Errorf("ListActive = %v, %v", list, err)
	}
	if err := s.SetState(ctx, w.ID, domain.StateMerged, 9); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Active(ctx, "thr_a"); ok {
		t.Error("a merged worktree is still the thread's active one")
	}
	if got, _ := s.Get(ctx, w.ID); got.State != domain.StateMerged || got.UpdatedAt != 9 {
		t.Errorf("after SetState: %+v", got)
	}
	if _, err := s.Get(ctx, "wtr_none"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Get of an unknown id: %v", err)
	}
	if err := s.SetState(ctx, "wtr_none", domain.StateMissing, 1); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("SetState of an unknown id: %v", err)
	}
}
