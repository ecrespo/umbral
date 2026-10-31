// Package hoststore persists remote hosts in SQLite (`remotes`, Data Model §2.14), the remote
// module's ports.Store.
package hoststore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/ecrespo/umbral/internal/remote/domain"
	"github.com/ecrespo/umbral/internal/remote/ports"
	"github.com/ecrespo/umbral/internal/store"
)

// Store is ports.Store over the daemon's database.
type Store struct {
	db *sql.DB
}

var _ ports.Store = (*Store)(nil)

// New builds the store.
func New(st *store.Store) (*Store, error) {
	if st == nil {
		return nil, errors.New("hoststore: a Store is required")
	}
	return &Store{db: st.DB()}, nil
}

// List returns every host, by name.
func (s *Store) List(ctx context.Context) ([]domain.Host, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, url, created_at FROM remotes ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("hoststore: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Host
	for rows.Next() {
		var h domain.Host
		if err := rows.Scan(&h.Name, &h.URL, &h.CreatedAt); err != nil {
			return nil, fmt.Errorf("hoststore: list: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Insert adds a host; a name already taken is domain.ErrConflict.
func (s *Store) Insert(ctx context.Context, h domain.Host) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO remotes(name, url, created_at) VALUES (?, ?, ?)`,
		h.Name, h.URL, h.CreatedAt)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: remotes.name") {
		return fmt.Errorf("%w: %s", domain.ErrConflict, h.Name)
	}
	if err != nil {
		return fmt.Errorf("hoststore: insert: %w", err)
	}
	return nil
}

// Delete removes a host by name.
func (s *Store) Delete(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM remotes WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("hoststore: delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s", domain.ErrNotFound, name)
	}
	return nil
}
