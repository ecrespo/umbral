// Package store owns the SQLite database described in specs/data-model/umbral-schema.md:
// connection pragmas, forward-only migrations and recovery after a daemon restart.
//
// Everything else in the daemon reaches the database through this package. It holds no
// domain rules of its own; the schema is the contract, and the data model document is
// its source of truth.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, no cgo (Constitution stack constraints)
)

// Sentinel errors this package returns. The api layer maps them to JSON-RPC codes
// through errors.Is (Tech Design §5.4).
var (
	// ErrSchemaTooNew reports a database written by a newer daemon. Migrations are
	// forward-only (Art. 6), so the older daemon refuses to start rather than guess.
	ErrSchemaTooNew = errors.New("database schema is newer than this daemon knows")
)

// DefaultFileName is the database file inside the Umbral data directory.
const DefaultFileName = "umbral.db"

// busyTimeout is the Data Model §5 value: how long a writer waits for the lock before
// giving up with SQLITE_BUSY.
const busyTimeout = 5 * time.Second

// Store is an open handle to the Umbral database.
type Store struct {
	db   *sql.DB
	path string
}

// Options configures Open. The zero value opens the database at DefaultPath with the
// pragmas the data model mandates.
type Options struct {
	// Path is the database file. Empty means DefaultPath.
	Path string
	// SkipMigrations opens the database without applying migrations. Only tests that
	// inspect a partially migrated database should set it.
	SkipMigrations bool
}

// Open opens the database, applies the pragmas from Data Model §5 and, unless the caller
// opts out, brings the schema up to the latest migration.
//
// The pragmas travel in the DSN rather than as post-connect statements because
// database/sql owns a pool: a pragma issued once would apply to one connection and
// silently not to the next.
func Open(ctx context.Context, opts Options) (*Store, error) {
	path := opts.Path
	if path == "" {
		p, err := DefaultPath()
		if err != nil {
			return nil, err
		}
		path = p
	}

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		// 0o700: the database holds full command output, classified confidential in
		// Tech Design §6.2.
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("store: create data directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}

	s := &Store{db: db, path: path}
	if err := s.verifyPragmas(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if !opts.SkipMigrations {
		if err := s.Migrate(ctx); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return s, nil
}

// DB exposes the underlying handle for the adapters that own the queries of their module.
func (s *Store) DB() *sql.DB { return s.db }

// Path reports the database file this store was opened from.
func (s *Store) Path() string { return s.path }

// Close releases the handle.
func (s *Store) Close() error { return s.db.Close() }

// dsn builds the connection string carrying the Data Model §5 pragmas.
func dsn(path string) string {
	pragmas := []string{
		"journal_mode(WAL)",
		"foreign_keys(1)",
		fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds()),
		"synchronous(NORMAL)",
	}
	q := url.Values{}
	for _, p := range pragmas {
		q.Add("_pragma", p)
	}
	return "file:" + path + "?" + q.Encode()
}

// verifyPragmas reads the pragmas back. A driver that silently ignored one would leave
// the daemon running without foreign keys, which is exactly the class of bug the
// Analyze finding A-01 was about, so this is checked rather than assumed.
func (s *Store) verifyPragmas(ctx context.Context) error {
	var foreignKeys int
	if err := s.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("store: read foreign_keys pragma: %w", err)
	}
	if foreignKeys != 1 {
		return fmt.Errorf("store: foreign_keys pragma is %d, want 1", foreignKeys)
	}

	var journalMode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return fmt.Errorf("store: read journal_mode pragma: %w", err)
	}
	// An in-memory database cannot use WAL; it reports "memory" and that is fine.
	if journalMode != "wal" && journalMode != "memory" {
		return fmt.Errorf("store: journal_mode is %q, want wal", journalMode)
	}
	return nil
}

// DefaultPath reports $XDG_DATA_HOME/umbral/umbral.db, falling back to
// ~/.local/share/umbral/umbral.db (Data Model metadata).
func DefaultPath() (string, error) {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("store: locate the data directory: %w", err)
		}
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "umbral", DefaultFileName), nil
}

// nowMillis is UTC epoch milliseconds, the only time representation the schema accepts
// (Art. 6).
func nowMillis(t time.Time) int64 { return t.UTC().UnixMilli() }
