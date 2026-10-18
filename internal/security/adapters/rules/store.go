package rules

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	llmdomain "github.com/ecrespo/umbral/internal/llmgw/domain"
	"github.com/ecrespo/umbral/internal/security/domain"
	"github.com/ecrespo/umbral/internal/security/ports"
	"github.com/ecrespo/umbral/internal/store"
)

// Store is the trust store and bundle history over `trust_keys`, `rule_bundles`,
// `rule_rejections`, `rule_update_state` and `trust_key_events` (Data Model §2.4e, migration
// 0006). Each Update is one SQLite transaction.
type Store struct {
	db *sql.DB
}

var _ ports.RuleStore = (*Store)(nil)

// NewStore builds the store over the daemon's database.
func NewStore(s *store.Store) *Store { return &Store{db: s.DB()} }

// Update runs fn in one transaction, committed when fn returns nil.
func (s *Store) Update(ctx context.Context, fn func(ports.RuleTx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(&ruleTx{ctx: ctx, tx: tx}); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

type ruleTx struct {
	ctx context.Context
	tx  *sql.Tx
}

func (t *ruleTx) exec(q string, args ...any) error {
	_, err := t.tx.ExecContext(t.ctx, q, args...)
	return err
}

func (t *ruleTx) Keys() ([]domain.TrustKey, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT id, public_key, fingerprint, added_at, revoked_at FROM trust_keys ORDER BY added_at, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.TrustKey
	for rows.Next() {
		var k domain.TrustKey
		var revoked sql.NullInt64
		if err := rows.Scan(&k.ID, &k.PublicKey, &k.Fingerprint, &k.AddedAt, &revoked); err != nil {
			return nil, err
		}
		if revoked.Valid {
			k.RevokedAt = &revoked.Int64
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (t *ruleTx) InsertKey(k domain.TrustKey) error {
	return t.exec(`INSERT INTO trust_keys (id, public_key, fingerprint, added_at, revoked_at) VALUES (?, ?, ?, ?, ?)`,
		k.ID, k.PublicKey, k.Fingerprint, k.AddedAt, nullable(k.RevokedAt))
}

func (t *ruleTx) SetRevoked(id string, at *int64) error {
	res, err := t.tx.ExecContext(t.ctx, `UPDATE trust_keys SET revoked_at = ? WHERE id = ?`, nullable(at), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("rules: no trust key %s", id)
	}
	return nil
}

func (t *ruleTx) KeyEvent(e domain.KeyEvent) error {
	return t.exec(`INSERT INTO trust_key_events (key_id, fingerprint, action, at) VALUES (?, ?, ?, ?)`, e.KeyID, e.Fingerprint, e.Action, e.At)
}

func (t *ruleTx) Bundles() ([]domain.BundleRecord, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT version, sha256, source, verified_with, installed_at, active, content FROM rule_bundles ORDER BY version DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.BundleRecord
	for rows.Next() {
		var b domain.BundleRecord
		var verified sql.NullString
		if err := rows.Scan(&b.Version, &b.SHA256, &b.Source, &verified, &b.InstalledAt, &b.Active, &b.Content); err != nil {
			return nil, err
		}
		b.VerifiedWith = verified.String
		out = append(out, b)
	}
	return out, rows.Err()
}

// PutBundle writes a row, replacing one of the same version. A remote row without
// verified_with or without its bytes cannot exist: the DDL allows it and this refuses it
// (Data Model §2.4e).
func (t *ruleTx) PutBundle(b domain.BundleRecord) error {
	if b.Source == domain.SourceRemote && (b.VerifiedWith == "" || len(b.Content) == 0) {
		return errors.New("rules: a remote bundle needs verified_with and its bytes")
	}
	var verified any
	if b.VerifiedWith != "" {
		verified = b.VerifiedWith
	}
	active := 0
	if b.Active {
		active = 1
		if err := t.exec(`UPDATE rule_bundles SET active = 0 WHERE active = 1 AND version <> ?`, b.Version); err != nil {
			return err
		}
	}
	return t.exec(`INSERT INTO rule_bundles (version, sha256, source, verified_with, installed_at, active, content)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(version) DO UPDATE SET sha256 = excluded.sha256, source = excluded.source,
			verified_with = excluded.verified_with, installed_at = excluded.installed_at,
			active = excluded.active, content = excluded.content`,
		b.Version, b.SHA256, b.Source, verified, b.InstalledAt, active, b.Content)
}

func (t *ruleTx) DeleteBundle(version int64) error {
	return t.exec(`DELETE FROM rule_bundles WHERE version = ?`, version)
}

// SetActive clears the old active row before setting the new one, since the partial unique
// index allows one active row at any moment of the statement sequence.
func (t *ruleTx) SetActive(version int64) error {
	if err := t.exec(`UPDATE rule_bundles SET active = 0 WHERE active = 1 AND version <> ?`, version); err != nil {
		return err
	}
	res, err := t.tx.ExecContext(t.ctx, `UPDATE rule_bundles SET active = 1 WHERE version = ?`, version)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("rules: no bundle of version %d", version)
	}
	return nil
}

func (t *ruleTx) Rejection(sha256 string) (bool, error) {
	var one int
	err := t.tx.QueryRowContext(t.ctx, `SELECT 1 FROM rule_rejections WHERE sha256 = ?`, sha256).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (t *ruleTx) PutRejection(r domain.Rejection) error {
	var version any
	if r.Version != 0 {
		version = r.Version
	}
	return t.exec(`INSERT INTO rule_rejections (sha256, reason, version, at) VALUES (?, ?, ?, ?)
		ON CONFLICT(sha256) DO UPDATE SET reason = excluded.reason, version = excluded.version, at = excluded.at`,
		r.SHA256, string(r.Reason), version, r.At)
}

func (t *ruleTx) DeleteRejection(sha256 string) error {
	return t.exec(`DELETE FROM rule_rejections WHERE sha256 = ?`, sha256)
}

func (t *ruleTx) DeleteRejections(reason domain.RejectReason) error {
	return t.exec(`DELETE FROM rule_rejections WHERE reason = ?`, string(reason))
}

func (t *ruleTx) State() (domain.UpdateState, error) {
	var s domain.UpdateState
	var reason sql.NullString
	var at sql.NullInt64
	err := t.tx.QueryRowContext(t.ctx, `SELECT consecutive_failures, fail_closed, ever_had_valid_key, highest_version, last_rejection_reason, last_rejection_at
		FROM rule_update_state WHERE id = 1`).Scan(&s.ConsecutiveFailures, &s.FailClosed, &s.EverHadValidKey, &s.HighestVersion, &reason, &at)
	s.LastRejection, s.LastRejectionAt = domain.RejectReason(reason.String), at.Int64
	return s, err
}

func (t *ruleTx) SetState(s domain.UpdateState) error {
	var reason, at any
	if s.LastRejection != "" {
		reason, at = string(s.LastRejection), s.LastRejectionAt
	}
	return t.exec(`UPDATE rule_update_state SET consecutive_failures = ?, fail_closed = ?, ever_had_valid_key = ?, highest_version = ?,
		last_rejection_reason = ?, last_rejection_at = ? WHERE id = 1`,
		s.ConsecutiveFailures, s.FailClosed, s.EverHadValidKey, s.HighestVersion, reason, at)
}

func (t *ruleTx) Egress(rec llmdomain.EgressRecord) error {
	var thread any
	if rec.ThreadID != "" {
		thread = rec.ThreadID
	}
	return t.exec(`INSERT INTO egress_log (thread_id, provider, host, bytes, payload_sha256, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		thread, rec.Provider, rec.Host, rec.Bytes, rec.PayloadSHA256, rec.CreatedAt)
}

func nullable(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}
