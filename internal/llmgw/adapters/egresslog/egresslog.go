// Package egresslog writes the `egress_log` table (Data Model §2, REQ-SEC-002).
package egresslog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ecrespo/umbral/internal/llmgw/domain"
	"github.com/ecrespo/umbral/internal/store"
)

// Log is the egress audit trail.
type Log struct {
	db *sql.DB
}

// New builds the log over an open database.
func New(st *store.Store) (*Log, error) {
	if st == nil {
		return nil, errors.New("egresslog: a Store is required")
	}
	return &Log{db: st.DB()}, nil
}

// Record implements ports.EgressLog.
func (l *Log) Record(ctx context.Context, r domain.EgressRecord) error {
	var thread any
	if r.ThreadID != "" {
		thread = r.ThreadID
	}
	if _, err := l.db.ExecContext(ctx, `INSERT INTO egress_log
		(thread_id, provider, host, bytes, payload_sha256, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		thread, r.Provider, r.Host, r.Bytes, r.PayloadSHA256, r.CreatedAt); err != nil {
		return fmt.Errorf("egresslog: %w", err)
	}
	return nil
}
