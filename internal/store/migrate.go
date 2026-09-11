package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationName matches "0001_terminal.sql": a zero-padded version and a description.
var migrationName = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)

// migration is one forward-only step (Art. 6). There is no Down: a mistake is corrected
// by a new migration, never by rewinding a user's database.
type migration struct {
	version int
	name    string
	sql     string
}

// Migrate applies every migration the database has not seen yet, each one inside its own
// transaction, and refuses to touch a database written by a newer daemon.
func (s *Store) Migrate(ctx context.Context) error {
	available, err := loadMigrations()
	if err != nil {
		return err
	}

	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	current, err := s.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if latest := available[len(available)-1].version; current > latest {
		return fmt.Errorf("%w: database is at version %d, this daemon knows up to %d",
			ErrSchemaTooNew, current, latest)
	}

	for _, m := range available {
		if m.version <= current {
			continue
		}
		if err := s.apply(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

// SchemaVersion reports the highest applied migration, or 0 for an empty database.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var version sql.NullInt64
	err := s.db.QueryRowContext(ctx, "SELECT max(version) FROM schema_migrations").Scan(&version)
	switch {
	case err != nil && isNoSuchTable(err):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("store: read schema version: %w", err)
	case !version.Valid:
		return 0, nil
	}
	return int(version.Int64), nil
}

// apply runs one migration and records it, both inside the same transaction. A failure
// half-way leaves the database exactly as it was.
func (s *Store) apply(ctx context.Context, m migration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin migration %s: %w", m.name, err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("store: apply migration %s: %w", m.name, err)
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)",
		m.version, nowMillis(time.Now())); err != nil {
		return fmt.Errorf("store: record migration %s: %w", m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit migration %s: %w", m.name, err)
	}
	return nil
}

// loadMigrations reads the embedded files and returns them ordered by version.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store: read migrations: %w", err)
	}

	out := make([]migration, 0, len(entries))
	seen := make(map[int]string, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		match := migrationName.FindStringSubmatch(e.Name())
		if match == nil {
			return nil, fmt.Errorf("store: migration %q does not match NNNN_description.sql", e.Name())
		}
		version, err := strconv.Atoi(match[1])
		if err != nil {
			return nil, fmt.Errorf("store: migration %q has an unreadable version: %w", e.Name(), err)
		}
		if other, dup := seen[version]; dup {
			return nil, fmt.Errorf("store: migrations %q and %q share version %d", other, e.Name(), version)
		}
		seen[version] = e.Name()

		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("store: read migration %q: %w", e.Name(), err)
		}
		out = append(out, migration{version: version, name: e.Name(), sql: string(body)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("store: no migrations are embedded")
	}

	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	if out[0].version != 1 {
		return nil, fmt.Errorf("store: migrations start at version %d, want 1", out[0].version)
	}
	for i, m := range out {
		if want := i + 1; m.version != want {
			return nil, fmt.Errorf("store: migration versions have a gap: found %d, want %d", m.version, want)
		}
	}
	return out, nil
}

// isNoSuchTable reports whether err is SQLite's "no such table", which is how an
// unmigrated database announces itself.
func isNoSuchTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}
