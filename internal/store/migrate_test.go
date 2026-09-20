package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

// openTestStore opens a migrated database on a temporary file. It is a file rather than
// :memory: on purpose: WAL, foreign keys and the FTS triggers are what these tests are
// about, and an in-memory database does not exercise the same journal path.
func openTestStore(t *testing.T) *Store {
	t.Helper()

	s, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "umbral.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

// insertSession inserts a minimal valid session and returns its id.
func insertSession(t *testing.T, s *Store, id, state string) string {
	t.Helper()

	_, err := s.DB().ExecContext(t.Context(),
		`INSERT INTO sessions(id, shell, cwd, cols, rows, state, created_at)
		 VALUES (?, '/usr/bin/zsh', '/home/u/repo', 120, 40, ?, 1757592000000)`, id, state)
	if err != nil {
		t.Fatalf("insert session %s: %v", id, err)
	}
	return id
}

// insertBlock inserts a user block in the given state and returns its id.
func insertBlock(t *testing.T, s *Store, id, sessionID, state, command, plain string) string {
	t.Helper()

	_, err := s.DB().ExecContext(t.Context(),
		`INSERT INTO blocks(id, session_id, origin, command, cwd, state, started_at, output_plain)
		 VALUES (?, ?, 'user', ?, '/home/u/repo', ?, 1757592001000, ?)`,
		id, sessionID, command, state, plain)
	if err != nil {
		t.Fatalf("insert block %s: %v", id, err)
	}
	return id
}

// TestMigrationsApplyAndAreIdempotent checks that Open migrates an empty database to the
// latest version and that opening it again changes nothing.
func TestMigrationsApplyAndAreIdempotent(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "umbral.db")

	first, err := Open(t.Context(), Options{Path: path})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	// The expected version is derived from the embedded files rather than written here,
	// so adding a migration does not break a test that is about idempotency.
	available, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	latest := available[len(available)-1].version

	version, err := first.SchemaVersion(t.Context())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != latest {
		t.Errorf("schema version after first open = %d, want %d", version, latest)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := Open(t.Context(), Options{Path: path})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer func() { _ = second.Close() }()

	var applied int
	if err := second.DB().QueryRowContext(t.Context(),
		"SELECT count(*) FROM schema_migrations").Scan(&applied); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if applied != len(available) {
		t.Errorf("schema_migrations has %d rows after two opens, want %d", applied, len(available))
	}
}

// TestMigrationUpgradesAnExistingDatabase is the regression test for a mistake made in
// T-F0-10: the index `block.list` is ordered by was first added by editing 0001, which had
// already been applied everywhere. The runner records only the version a database reached,
// so an edited file never runs again and those databases silently kept the full-table scan
// the index exists to prevent. This pins the forward-only rule of Art. 6 from the other
// side: a database left at an older version must pick up what came after it.
func TestMigrationUpgradesAnExistingDatabase(t *testing.T) {
	t.Parallel()

	available, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(available) < 2 {
		t.Skip("only one migration exists, so there is no upgrade to make")
	}
	latest := available[len(available)-1].version

	// A database that stopped at version 1 is *built* here rather than rewound from a
	// migrated one. Rewinding means dropping whatever every later migration created, which
	// is a list that would have to be maintained in this test forever and would be wrong
	// exactly once — the first time someone added a migration and did not think of it.
	// Applying the first migration and nothing else stays correct however many follow.
	path := filepath.Join(t.TempDir(), "umbral.db")
	raw, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("open the raw database: %v", err)
	}
	if _, err := raw.ExecContext(t.Context(), available[0].sql); err != nil {
		t.Fatalf("apply %s: %v", available[0].name, err)
	}
	// 0001 creates schema_migrations itself, so only the row recording it is missing.
	if _, err := raw.ExecContext(t.Context(),
		"INSERT INTO schema_migrations(version, applied_at) VALUES (?, 0)",
		available[0].version); err != nil {
		t.Fatalf("record %s: %v", available[0].name, err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close the raw database: %v", err)
	}

	second, err := Open(t.Context(), Options{Path: path})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer func() { _ = second.Close() }()

	var name string
	err = second.DB().QueryRowContext(t.Context(),
		"SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_blocks_started'").
		Scan(&name)
	if err != nil {
		t.Fatalf("a database left at version 1 did not receive idx_blocks_started: %v", err)
	}

	// Every later migration ran, not just 0002. This is what proves a migration added after
	// this test was written still reaches a database that predates it.
	version, err := second.SchemaVersion(t.Context())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != latest {
		t.Errorf("schema version is %d after upgrading from 1, want %d", version, latest)
	}
}

// TestMigrationRefusesFutureSchema pins the forward-only rule of Art. 6: a daemon that
// meets a database from a newer version stops instead of guessing.
func TestMigrationRefusesFutureSchema(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "umbral.db")
	s, err := Open(t.Context(), Options{Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.DB().ExecContext(t.Context(),
		"INSERT INTO schema_migrations(version, applied_at) VALUES (99, 0)"); err != nil {
		t.Fatalf("forge a future migration: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := Open(t.Context(), Options{Path: path}); !errors.Is(err, ErrSchemaTooNew) {
		t.Errorf("Open on a future schema = %v, want ErrSchemaTooNew", err)
	}
}

// TestMigration0001InsertsWithForeignKeysOn is the regression test for Analyze finding
// A-01. sessions.owner_thread_id and blocks.thread_id reference threads, so if threads
// were not created by migration 0001, SQLite would reject every insert here with
// "no such table: main.threads" even though both values are NULL.
func TestMigration0001InsertsWithForeignKeysOn(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)

	var foreignKeys int
	if err := s.DB().QueryRowContext(t.Context(), "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1; this test proves nothing without it", foreignKeys)
	}

	sessionID := insertSession(t, s, "ses_a", "alive")
	insertBlock(t, s, "blk_a", sessionID, "finished", "go test ./...", "ok all tests passed")

	for _, table := range []string{"threads", "sessions", "blocks", "block_chunks"} {
		var count int
		if err := s.DB().QueryRowContext(t.Context(),
			"SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Errorf("table %s is not queryable: %v", table, err)
		}
	}
}

// TestMigration0001EnforcesAgentBlocksCarryAThread covers the schema CHECK that keeps an
// agent block from existing without the thread that produced it.
func TestMigration0001EnforcesAgentBlocksCarryAThread(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	sessionID := insertSession(t, s, "ses_a", "alive")

	_, err := s.DB().ExecContext(t.Context(),
		`INSERT INTO blocks(id, session_id, origin, state, started_at)
		 VALUES ('blk_orphan', ?, 'agent', 'running', 1757592001000)`, sessionID)
	if err == nil {
		t.Fatal("an agent block with no thread_id was accepted; the CHECK is missing")
	}
}

// TestMigration0001KeepsFtsInSync_REQ_BLK_006 covers Analyze finding A-07: block search
// is only possible if the three blocks_fts triggers exist, so insert, update and delete
// are each checked against the index.
func TestMigration0001KeepsFtsInSync_REQ_BLK_006(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	sessionID := insertSession(t, s, "ses_a", "alive")
	blockID := insertBlock(t, s, "blk_a", sessionID, "finished",
		"go test ./...", "FAIL TestParse: unexpected token")

	matches := func(query string) int {
		t.Helper()
		var n int
		if err := s.DB().QueryRowContext(t.Context(),
			"SELECT count(*) FROM blocks_fts WHERE blocks_fts MATCH ?", query).Scan(&n); err != nil {
			t.Fatalf("FTS MATCH %q: %v", query, err)
		}
		return n
	}

	if got := matches("unexpected"); got != 1 {
		t.Errorf("after insert, MATCH 'unexpected' = %d, want 1 (the AFTER INSERT trigger is missing)", got)
	}

	if _, err := s.DB().ExecContext(t.Context(),
		"UPDATE blocks SET output_plain = 'ok all tests passed' WHERE id = ?", blockID); err != nil {
		t.Fatalf("update block: %v", err)
	}
	if got := matches("passed"); got != 1 {
		t.Errorf("after update, MATCH 'passed' = %d, want 1", got)
	}
	if got := matches("unexpected"); got != 0 {
		t.Errorf("after update, MATCH 'unexpected' = %d, want 0 (the AFTER UPDATE trigger left a stale row)", got)
	}

	if _, err := s.DB().ExecContext(t.Context(), "DELETE FROM blocks WHERE id = ?", blockID); err != nil {
		t.Fatalf("delete block: %v", err)
	}
	if got := matches("passed"); got != 0 {
		t.Errorf("after delete, MATCH 'passed' = %d, want 0 (the AFTER DELETE trigger is missing)", got)
	}
}

// TestMigration0001StoresPlainOutput_REQ_BLK_007 checks that a closed block keeps its
// escape-free text, which is the version handed to the agent as context.
func TestMigration0001StoresPlainOutput_REQ_BLK_007(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	sessionID := insertSession(t, s, "ses_a", "alive")
	const plain = "PASS\nok  github.com/ecrespo/umbral/internal/store\t0.4s"
	blockID := insertBlock(t, s, "blk_a", sessionID, "finished", "go test ./...", plain)

	var got string
	if err := s.DB().QueryRowContext(t.Context(),
		"SELECT output_plain FROM blocks WHERE id = ?", blockID).Scan(&got); err != nil {
		t.Fatalf("read output_plain: %v", err)
	}
	if got != plain {
		t.Errorf("output_plain round-trip = %q, want %q", got, plain)
	}
}

// TestLoadMigrationsAreContiguous guards the migration set itself: a gap or a duplicate
// version would mean some databases silently skip a step.
func TestLoadMigrationsAreContiguous(t *testing.T) {
	t.Parallel()

	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	for i, m := range migrations {
		if want := i + 1; m.version != want {
			t.Errorf("migration %d has version %d, want %d", i, m.version, want)
		}
		if m.sql == "" {
			t.Errorf("migration %s is empty", m.name)
		}
	}
}
