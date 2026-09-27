// Package threadstore persists threads, messages, tool calls and policy rules in SQLite
// (Data Model §2.4–§2.9), the agents module's ports.Store.
package threadstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ecrespo/umbral/internal/agents/domain"
	"github.com/ecrespo/umbral/internal/agents/ports"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
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
		return nil, errors.New("threadstore: a Store is required")
	}
	return &Store{db: st.DB()}, nil
}

const threadColumns = `id, title, mode, COALESCE(model, ''), model_class, cwd, state, attention_state,
	ephemeral, max_steps, budget_tokens, tokens_used, cost_micro_usd, created_at, updated_at`

type scanner interface{ Scan(dest ...any) error }

func scanThread(row scanner) (domain.Thread, error) {
	var t domain.Thread
	var mode, state string
	err := row.Scan(&t.ID, &t.Title, &mode, &t.Model, &t.ModelClass, &t.Cwd, &state, &t.AttentionState,
		&t.Ephemeral, &t.MaxSteps, &t.BudgetTokens, &t.TokensUsed, &t.CostMicroUSD, &t.CreatedAt, &t.UpdatedAt)
	t.Mode, t.State = secdomain.Mode(mode), domain.State(state)
	return t, err
}

// CreateThread inserts a thread.
func (s *Store) CreateThread(ctx context.Context, t domain.Thread) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO threads(id, title, mode, model, model_class, cwd, state, attention_state, ephemeral,
		                    max_steps, budget_tokens, tokens_used, cost_micro_usd, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?, ?)`,
		t.ID, t.Title, string(t.Mode), nullable(t.Model), t.ModelClass, t.Cwd, string(t.State), t.AttentionState,
		t.Ephemeral, t.MaxSteps, t.BudgetTokens, t.CreatedAt, t.UpdatedAt)
	if err != nil {
		return fmt.Errorf("threadstore: create thread: %w", err)
	}
	return nil
}

// Thread reads one thread.
func (s *Store) Thread(ctx context.Context, id string) (domain.Thread, error) {
	t, err := scanThread(s.db.QueryRowContext(ctx, `SELECT `+threadColumns+` FROM threads WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Thread{}, fmt.Errorf("%w: thread %s", domain.ErrNotFound, id)
	}
	if err != nil {
		return domain.Thread{}, fmt.Errorf("threadstore: read thread: %w", err)
	}
	return t, nil
}

// Threads lists the threads that are not ephemeral, most recently updated first.
func (s *Store) Threads(ctx context.Context) ([]domain.Thread, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+threadColumns+` FROM threads
		WHERE ephemeral = 0 ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("threadstore: list threads: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Thread
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, fmt.Errorf("threadstore: list threads: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpdateThread changes a thread's mode, model or title. A mode change while a turn runs is
// domain.ErrConflict (API §5.22).
func (s *Store) UpdateThread(ctx context.Context, id string, p domain.UpdateParams, now int64) (domain.Thread, error) {
	sets, args := []string{"updated_at = ?"}, []any{now}
	if p.Mode != nil {
		sets, args = append(sets, "mode = ?"), append(args, string(*p.Mode))
	}
	if p.Model != nil {
		sets, args = append(sets, "model = ?"), append(args, nullable(*p.Model))
	}
	if p.Title != nil {
		sets, args = append(sets, "title = ?"), append(args, *p.Title)
	}
	where := "id = ?"
	if p.Mode != nil {
		where += " AND state NOT IN ('running', 'awaiting_approval')"
	}
	res, err := s.db.ExecContext(ctx, `UPDATE threads SET `+strings.Join(sets, ", ")+` WHERE `+where, append(args, id)...) //nolint:gosec // the SET list is fixed column names
	if err != nil {
		return domain.Thread{}, fmt.Errorf("threadstore: update thread: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.Thread(ctx, id); err != nil {
			return domain.Thread{}, err
		}
		return domain.Thread{}, fmt.Errorf("%w: the mode cannot change during a turn", domain.ErrConflict)
	}
	return s.Thread(ctx, id)
}

// BeginTurn persists the user's message and marks the thread running (ports.Store). The
// database's transactions are IMMEDIATE (store.Open), so no other writer runs in between.
func (s *Store) BeginTurn(ctx context.Context, msg domain.Message, now int64) (domain.Message, domain.Thread, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Message{}, domain.Thread{}, fmt.Errorf("threadstore: begin turn: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if msg.ClientMsgID != "" {
		orig, found, err := messageByClientID(ctx, tx, msg.ThreadID, msg.ClientMsgID)
		if err != nil {
			return domain.Message{}, domain.Thread{}, err
		}
		if found {
			return orig, domain.Thread{}, ports.ErrDuplicate
		}
	}
	t, err := scanThread(tx.QueryRowContext(ctx, `SELECT `+threadColumns+` FROM threads WHERE id = ?`, msg.ThreadID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Message{}, domain.Thread{}, fmt.Errorf("%w: thread %s", domain.ErrNotFound, msg.ThreadID)
	}
	if err != nil {
		return domain.Message{}, domain.Thread{}, fmt.Errorf("threadstore: begin turn: %w", err)
	}
	switch {
	case t.State == domain.StateRunning || t.State == domain.StateAwaitingApproval:
		return domain.Message{}, domain.Thread{}, fmt.Errorf("%w: thread %s", domain.ErrConflict, msg.ThreadID)
	case t.TokensUsed >= t.BudgetTokens:
		return domain.Message{}, domain.Thread{}, fmt.Errorf("%w: %d of %d tokens used", domain.ErrBudgetExceeded, t.TokensUsed, t.BudgetTokens)
	}
	if err := insertMessage(ctx, tx, msg); err != nil {
		return domain.Message{}, domain.Thread{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE threads SET state = 'running', attention_state = 'working', updated_at = ? WHERE id = ?`,
		now, msg.ThreadID); err != nil {
		return domain.Message{}, domain.Thread{}, fmt.Errorf("threadstore: begin turn: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.Message{}, domain.Thread{}, fmt.Errorf("threadstore: begin turn: %w", err)
	}
	t.State, t.AttentionState, t.UpdatedAt = domain.StateRunning, "working", now
	return msg, t, nil
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

const messageColumns = `id, thread_id, turn_id, role, content, COALESCE(client_msg_id, ''), attachments_json, tainted, created_at`

func scanMessage(row scanner) (domain.Message, error) {
	var m domain.Message
	var role, attachments string
	if err := row.Scan(&m.ID, &m.ThreadID, &m.TurnID, &role, &m.Content, &m.ClientMsgID, &attachments, &m.Tainted, &m.CreatedAt); err != nil {
		return domain.Message{}, err
	}
	m.Role = domain.Role(role)
	if err := json.Unmarshal([]byte(attachments), &m.Attachments); err != nil {
		return domain.Message{}, fmt.Errorf("threadstore: attachments of %s: %w", m.ID, err)
	}
	return m, nil
}

func messageByClientID(ctx context.Context, q querier, threadID, clientMsgID string) (domain.Message, bool, error) {
	m, err := scanMessage(q.QueryRowContext(ctx, `SELECT `+messageColumns+` FROM messages
		WHERE thread_id = ? AND client_msg_id = ?`, threadID, clientMsgID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Message{}, false, nil
	}
	if err != nil {
		return domain.Message{}, false, fmt.Errorf("threadstore: find client_msg_id: %w", err)
	}
	return m, true, nil
}

// MessageByClientID finds the message a client id names in a thread.
func (s *Store) MessageByClientID(ctx context.Context, threadID, clientMsgID string) (domain.Message, bool, error) {
	return messageByClientID(ctx, s.db, threadID, clientMsgID)
}

func insertMessage(ctx context.Context, q querier, m domain.Message) error {
	attachments := m.Attachments
	if attachments == nil {
		attachments = []domain.Attachment{}
	}
	blob, err := json.Marshal(attachments)
	if err != nil {
		return fmt.Errorf("threadstore: attachments: %w", err)
	}
	_, err = q.ExecContext(ctx, `
		INSERT INTO messages(id, thread_id, turn_id, role, content, client_msg_id, attachments_json, tainted, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ThreadID, m.TurnID, string(m.Role), m.Content, nullable(m.ClientMsgID), string(blob), m.Tainted, m.CreatedAt)
	if err != nil {
		return fmt.Errorf("threadstore: insert message: %w", err)
	}
	return nil
}

// AppendMessage persists a message of a running turn.
func (s *Store) AppendMessage(ctx context.Context, msg domain.Message) error {
	return insertMessage(ctx, s.db, msg)
}

// AppendContent adds streamed text to a persisted message.
func (s *Store) AppendContent(ctx context.Context, messageID, text string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE messages SET content = content || ? WHERE id = ?`, text, messageID)
	if err != nil {
		return fmt.Errorf("threadstore: append content: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("threadstore: append content: no message %s", messageID)
	}
	return nil
}

// result is what `tool_calls.result_json` holds: the text the model reads and its taint.
type result struct {
	Text    string `json:"text"`
	Tainted bool   `json:"tainted,omitempty"`
}

// SaveToolCall inserts a tool call or updates its status and result.
func (s *Store) SaveToolCall(ctx context.Context, c domain.ToolCall) error {
	var resultJSON any
	if c.Status != domain.ToolPending {
		blob, err := json.Marshal(result{Text: c.Result, Tainted: c.Tainted})
		if err != nil {
			return fmt.Errorf("threadstore: tool result: %w", err)
		}
		resultJSON = string(blob)
	}
	args := string(c.Args)
	if args == "" {
		args = "{}"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tool_calls(id, thread_id, message_id, tool, risk, args_json, status, result_summary, result_json,
		                       block_id, started_at, ended_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET status = excluded.status, result_summary = excluded.result_summary,
			result_json = excluded.result_json, block_id = excluded.block_id, ended_at = excluded.ended_at`,
		c.ID, c.ThreadID, c.MessageID, c.Tool, c.Risk, args, string(c.Status), nullable(c.ResultSummary), resultJSON,
		nullable(c.BlockID), c.StartedAt, c.EndedAt)
	if err != nil {
		return fmt.Errorf("threadstore: save tool call: %w", err)
	}
	return nil
}

// FinishTurn adds the turn's usage to the thread and sets its state.
func (s *Store) FinishTurn(ctx context.Context, threadID string, state domain.State, usage domain.Usage, now int64) error {
	attention := "done"
	if state == domain.StateStopped {
		attention = "idle"
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE threads SET state = ?, attention_state = ?, tokens_used = tokens_used + ?,
			cost_micro_usd = cost_micro_usd + ?, updated_at = ?
		WHERE id = ?`,
		string(state), attention, usage.InTokens+usage.OutTokens, usage.CostMicroUSD, now, threadID)
	if err != nil {
		return fmt.Errorf("threadstore: finish turn: %w", err)
	}
	return nil
}

// Messages is a thread's history, oldest first.
func (s *Store) Messages(ctx context.Context, threadID string) ([]domain.Message, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+messageColumns+` FROM messages
		WHERE thread_id = ? ORDER BY created_at, rowid`, threadID)
	if err != nil {
		return nil, fmt.Errorf("threadstore: messages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ToolCalls is a thread's tool calls, oldest first.
func (s *Store) ToolCalls(ctx context.Context, threadID string) ([]domain.ToolCall, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, thread_id, message_id, tool, risk, args_json, status, COALESCE(result_summary, ''),
		       COALESCE(result_json, ''), COALESCE(block_id, ''), started_at, ended_at
		FROM tool_calls WHERE thread_id = ? ORDER BY started_at, rowid`, threadID)
	if err != nil {
		return nil, fmt.Errorf("threadstore: tool calls: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.ToolCall
	for rows.Next() {
		var c domain.ToolCall
		var args, status, resultJSON string
		var ended sql.NullInt64
		if err := rows.Scan(&c.ID, &c.ThreadID, &c.MessageID, &c.Tool, &c.Risk, &args, &status, &c.ResultSummary,
			&resultJSON, &c.BlockID, &c.StartedAt, &ended); err != nil {
			return nil, fmt.Errorf("threadstore: tool calls: %w", err)
		}
		c.Args, c.Status = json.RawMessage(args), domain.ToolStatus(status)
		if ended.Valid {
			c.EndedAt = &ended.Int64
		}
		if resultJSON != "" {
			var r result
			if err := json.Unmarshal([]byte(resultJSON), &r); err != nil {
				return nil, fmt.Errorf("threadstore: result of %s: %w", c.ID, err)
			}
			c.Result, c.Tainted = r.Text, r.Tainted
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Rules are the persisted rules for a thread: its own and the global ones.
func (s *Store) Rules(ctx context.Context, threadID string) ([]secdomain.Rule, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(thread_id, ''), tool, pattern, decision, source FROM policy_rules
		WHERE thread_id IS NULL OR thread_id = ? ORDER BY id`, threadID)
	if err != nil {
		return nil, fmt.Errorf("threadstore: rules: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []secdomain.Rule
	for rows.Next() {
		var r secdomain.Rule
		var decision string
		if err := rows.Scan(&r.ThreadID, &r.Tool, &r.Pattern, &decision, &r.Source); err != nil {
			return nil, fmt.Errorf("threadstore: rules: %w", err)
		}
		r.Decision = secdomain.Verdict(decision)
		out = append(out, r)
	}
	return out, rows.Err()
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
