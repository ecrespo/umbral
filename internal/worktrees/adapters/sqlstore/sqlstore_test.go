package sqlstore

import (
	"errors"
	"path/filepath"
	"slices"
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

// TestListByThread_REQ_WT_007: List returns a group's rows in every state, oldest first, every
// row for nil, and none for an empty group.
func TestListByThread_REQ_WT_007(t *testing.T) {
	t.Parallel()
	db, err := store.Open(t.Context(), store.Options{Path: filepath.Join(t.TempDir(), "umbral.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, id := range []string{"thr_a", "thr_b", "thr_c"} {
		if _, err := db.DB().ExecContext(t.Context(), `INSERT INTO threads(id, cwd, created_at, updated_at) VALUES (?, '/repo', 1, 1)`, id); err != nil {
			t.Fatal(err)
		}
	}
	s := New(db.DB())
	row := func(thread string, at int64, state domain.State) domain.Worktree {
		w := domain.Worktree{
			ID: store.NewID(store.PrefixWorktree), ThreadID: thread, RepoRoot: "/repo", RepoID: "0123456789abcdef",
			BaseCommit: "abc", Branch: "umbral/" + thread, Path: "/data/" + thread + string(state),
			State: state, CreatedAt: at, UpdatedAt: at,
		}
		if err := s.Insert(t.Context(), w); err != nil {
			t.Fatal(err)
		}
		return w
	}
	a2 := row("thr_a", 20, domain.StateActive)
	a1 := row("thr_a", 10, domain.StateMerged)
	b := row("thr_b", 15, domain.StateMissing)
	c := row("thr_c", 5, domain.StateActive)

	ids := func(ws []domain.Worktree) []string {
		out := make([]string, 0, len(ws))
		for _, w := range ws {
			out = append(out, w.ID)
		}
		return out
	}
	got, err := s.List(t.Context(), []string{"thr_a", "thr_b"})
	if err != nil || !slices.Equal(ids(got), []string{a1.ID, b.ID, a2.ID}) {
		t.Errorf("List(a, b) = %v, %v", ids(got), err)
	}
	if got, err := s.List(t.Context(), nil); err != nil || !slices.Equal(ids(got), []string{c.ID, a1.ID, b.ID, a2.ID}) {
		t.Errorf("List(nil) = %v, %v", ids(got), err)
	}
	if got, err := s.List(t.Context(), []string{}); err != nil || len(got) != 0 {
		t.Errorf("List(empty) = %v, %v", ids(got), err)
	}
}
