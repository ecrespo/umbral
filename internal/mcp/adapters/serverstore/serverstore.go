// Package serverstore persists MCP servers in SQLite (`mcp_servers`, Data Model §2.13), the
// mcp module's ports.Store.
package serverstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ecrespo/umbral/internal/mcp/domain"
	"github.com/ecrespo/umbral/internal/mcp/ports"
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
		return nil, errors.New("serverstore: a Store is required")
	}
	return &Store{db: st.DB()}, nil
}

// List returns every server, by name.
func (s *Store) List(ctx context.Context) ([]domain.Server, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, transport, COALESCE(command, ''), args_json, COALESCE(url, ''), env_refs_json,
		       trust, state, COALESCE(last_error, ''), created_at, updated_at
		FROM mcp_servers ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("serverstore: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Server
	for rows.Next() {
		var srv domain.Server
		var transport, trust, state, args, env string
		if err := rows.Scan(&srv.ID, &srv.Name, &transport, &srv.Command, &args, &srv.URL, &env,
			&trust, &state, &srv.LastError, &srv.CreatedAt, &srv.UpdatedAt); err != nil {
			return nil, fmt.Errorf("serverstore: list: %w", err)
		}
		srv.Transport, srv.Trust, srv.State = domain.Transport(transport), domain.Trust(trust), domain.State(state)
		if err := json.Unmarshal([]byte(args), &srv.Args); err != nil {
			return nil, fmt.Errorf("serverstore: %s: args_json: %w", srv.Name, err)
		}
		if err := json.Unmarshal([]byte(env), &srv.EnvRefs); err != nil {
			return nil, fmt.Errorf("serverstore: %s: env_refs_json: %w", srv.Name, err)
		}
		out = append(out, srv)
	}
	return out, rows.Err()
}

// Insert adds a server; a name already taken is domain.ErrConflict.
func (s *Store) Insert(ctx context.Context, srv domain.Server) error {
	args, err := json.Marshal(nonNil(srv.Args))
	if err != nil {
		return err
	}
	env := srv.EnvRefs
	if env == nil {
		env = map[string]string{}
	}
	envJSON, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO mcp_servers(id, name, transport, command, args_json, url, env_refs_json, trust, state,
		                        last_error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		srv.ID, srv.Name, string(srv.Transport), nullable(srv.Command), string(args), nullable(srv.URL),
		string(envJSON), string(srv.Trust), string(srv.State), nullable(srv.LastError), srv.CreatedAt, srv.UpdatedAt)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: mcp_servers.name") {
		return fmt.Errorf("%w: %s", domain.ErrConflict, srv.Name)
	}
	if err != nil {
		return fmt.Errorf("serverstore: insert: %w", err)
	}
	return nil
}

// Delete removes a server by name.
func (s *Store) Delete(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM mcp_servers WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("serverstore: delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s", domain.ErrNotFound, name)
	}
	return nil
}

// SetState records a server's state and the reason for it.
func (s *Store) SetState(ctx context.Context, id string, state domain.State, lastError string, now int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE mcp_servers SET state = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		string(state), nullable(lastError), now, id)
	if err != nil {
		return fmt.Errorf("serverstore: set state: %w", err)
	}
	return nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nonNil(a []string) []string {
	if a == nil {
		return []string{}
	}
	return a
}
