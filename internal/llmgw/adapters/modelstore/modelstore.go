// Package modelstore keeps the model catalog in the `models` table (Data Model §2.10).
package modelstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ecrespo/umbral/internal/llmgw/domain"
	"github.com/ecrespo/umbral/internal/store"
)

// Store is the catalog's persistence.
type Store struct {
	db *sql.DB
}

// New builds the store over an open database.
func New(st *store.Store) (*Store, error) {
	if st == nil {
		return nil, errors.New("modelstore: a Store is required")
	}
	return &Store{db: st.DB()}, nil
}

// caps is `caps_json`: {tools, vision, reasoning, json_schema}; the context window has its
// own column.
type caps struct {
	Tools      bool `json:"tools"`
	Vision     bool `json:"vision"`
	Reasoning  bool `json:"reasoning"`
	JSONSchema bool `json:"json_schema"`
}

// Replace implements ports.ModelStore, in one transaction.
func (s *Store) Replace(ctx context.Context, provider string, models []domain.Model) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("modelstore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM models WHERE provider = ?`, provider); err != nil {
		return fmt.Errorf("modelstore: clear %s: %w", provider, err)
	}
	for _, m := range models {
		c, err := json.Marshal(caps{m.Caps.Tools, m.Caps.Vision, m.Caps.Reasoning, m.Caps.JSONSchema})
		if err != nil {
			return err
		}
		local := 0
		if m.Local {
			local = 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO models
			(id, provider, local, caps_json, context_window, price_in_micro_usd_per_mtok,
			 price_out_micro_usd_per_mtok, health, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			m.ID, provider, local, string(c), m.Caps.ContextWindow, m.PriceInMicroUSDPerMTok,
			m.PriceOutMicroUSDPerMTok, string(m.Health), m.UpdatedAt); err != nil {
			return fmt.Errorf("modelstore: insert %s: %w", m.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("modelstore: commit: %w", err)
	}
	return nil
}

// SetHealth implements ports.ModelStore.
func (s *Store) SetHealth(ctx context.Context, provider string, health domain.Health) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE models SET health = ? WHERE provider = ?`,
		string(health), provider); err != nil {
		return fmt.Errorf("modelstore: health of %s: %w", provider, err)
	}
	return nil
}

// Retain implements ports.ModelStore. The ids travel as one JSON array parameter.
func (s *Store) Retain(ctx context.Context, keep []string) error {
	ids, err := json.Marshal(append([]string{}, keep...))
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM models WHERE provider NOT IN (SELECT value FROM json_each(?))`, string(ids)); err != nil {
		return fmt.Errorf("modelstore: retain: %w", err)
	}
	return nil
}

// List implements ports.ModelStore.
func (s *Store) List(ctx context.Context) ([]domain.Model, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, provider, local, caps_json, context_window,
		price_in_micro_usd_per_mtok, price_out_micro_usd_per_mtok, health, updated_at
		FROM models ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("modelstore: list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Model
	for rows.Next() {
		var (
			m      domain.Model
			local  int
			raw    string
			health string
			c      caps
		)
		if err := rows.Scan(&m.ID, &m.Provider, &local, &raw, &m.Caps.ContextWindow,
			&m.PriceInMicroUSDPerMTok, &m.PriceOutMicroUSDPerMTok, &health, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("modelstore: scan: %w", err)
		}
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return nil, fmt.Errorf("modelstore: caps of %s: %w", m.ID, err)
		}
		m.Local, m.Health = local == 1, domain.Health(health)
		m.Caps.Tools, m.Caps.Vision, m.Caps.Reasoning, m.Caps.JSONSchema = c.Tools, c.Vision, c.Reasoning, c.JSONSchema
		out = append(out, m)
	}
	return out, rows.Err()
}
