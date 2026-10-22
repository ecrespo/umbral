// Package usagelog writes the `usage` table (Data Model §2.11, REQ-LLM-005).
package usagelog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ecrespo/umbral/internal/llmgw/domain"
	"github.com/ecrespo/umbral/internal/store"
)

// Log is the record of every model call.
type Log struct {
	db *sql.DB
}

// New builds the log over an open database.
func New(st *store.Store) (*Log, error) {
	if st == nil {
		return nil, errors.New("usagelog: a Store is required")
	}
	return &Log{db: st.DB()}, nil
}

// Record implements ports.UsageLog.
func (l *Log) Record(ctx context.Context, r domain.UsageRecord) error {
	if _, err := l.db.ExecContext(ctx, `INSERT INTO usage
		(thread_id, turn_id, model_id, provider, status, in_tokens, out_tokens, first_token_ms,
		 cost_micro_usd, error, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nullable(r.ThreadID), nullable(r.TurnID), r.ModelID, r.Provider, r.Status, r.InTokens,
		r.OutTokens, r.FirstTokenMS, r.CostMicroUSD, nullable(r.Error), r.CreatedAt); err != nil {
		return fmt.Errorf("usagelog: %w", err)
	}
	return nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
