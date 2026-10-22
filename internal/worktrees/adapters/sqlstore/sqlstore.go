// Package sqlstore keeps worktree rows in Umbral's SQLite database (Data Model §2.4g).
package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ecrespo/umbral/internal/worktrees/domain"
	"github.com/ecrespo/umbral/internal/worktrees/ports"
)

// Store implements ports.Store over the daemon's database.
type Store struct{ db *sql.DB }

var _ ports.Store = (*Store)(nil)

// New returns a Store over db, which migration 0007 has reached.
func New(db *sql.DB) *Store { return &Store{db: db} }

const columns = `id, thread_id, repo_root, repo_id, base_commit, origin_branch, branch, path, state, created_at, updated_at`

func scan(row interface{ Scan(...any) error }) (domain.Worktree, error) {
	var w domain.Worktree
	var state string
	err := row.Scan(&w.ID, &w.ThreadID, &w.RepoRoot, &w.RepoID, &w.BaseCommit, &w.OriginBranch, &w.Branch, &w.Path,
		&state, &w.CreatedAt, &w.UpdatedAt)
	w.State = domain.State(state)
	return w, err
}

// Insert implements ports.Store.
func (s *Store) Insert(ctx context.Context, w domain.Worktree) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO worktrees (`+columns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, w.ThreadID, w.RepoRoot, w.RepoID, w.BaseCommit, w.OriginBranch, w.Branch, w.Path, string(w.State), w.CreatedAt, w.UpdatedAt)
	if err != nil {
		return fmt.Errorf("worktrees: insert %s: %w", w.ID, err)
	}
	return nil
}

// Get implements ports.Store.
func (s *Store) Get(ctx context.Context, id string) (domain.Worktree, error) {
	w, err := scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM worktrees WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Worktree{}, domain.ErrNotFound
	}
	return w, err
}

// Active implements ports.Store.
func (s *Store) Active(ctx context.Context, threadID string) (domain.Worktree, bool, error) {
	w, err := scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM worktrees WHERE thread_id = ? AND state = 'active'`, threadID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Worktree{}, false, nil
	}
	return w, err == nil, err
}

// ListActive implements ports.Store.
func (s *Store) ListActive(ctx context.Context) ([]domain.Worktree, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM worktrees WHERE state = 'active' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Worktree
	for rows.Next() {
		w, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// SetState implements ports.Store.
func (s *Store) SetState(ctx context.Context, id string, state domain.State, at int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE worktrees SET state = ?, updated_at = ? WHERE id = ?`, string(state), at, id)
	if err != nil {
		return fmt.Errorf("worktrees: set %s %s: %w", id, state, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}
