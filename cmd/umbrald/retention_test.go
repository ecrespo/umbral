package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/config"
	"github.com/ecrespo/umbral/internal/store"
)

// TestTheDaemonAppliesRetentionAtStart: the daemon runs Data Model §4's job when
// it starts, in the background, with the windows `[retention]` sets — and then once a day. A
// database left with an old block, an idle ephemeral thread and a block inside the window comes
// back with the first two purged and the third untouched.
func TestTheDaemonAppliesRetentionAtStart(t *testing.T) {
	bin := buildDaemon(t)
	rt, _ := isolatedRuntime(t)
	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "umbral")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(configDir, "config.toml"), "[retention]\nraw_output_days = 2\n")

	dbPath := filepath.Join(os.Getenv("XDG_DATA_HOME"), "umbral", "umbral.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(t.Context(), store.Options{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	day := 24 * time.Hour.Milliseconds()
	now := time.Now().UnixMilli()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO sessions(id, shell, cwd, cols, rows, state, created_at) VALUES ('ses_x', '/bin/sh', '/', 80, 24, 'exited', 1)`, nil},
		// Three days old: past the 2-day window config.toml sets, inside the default 30.
		{`INSERT INTO blocks(id, session_id, origin, state, started_at, ended_at) VALUES ('blk_old', 'ses_x', 'user', 'finished', ?, ?)`, []any{now - 3*day, now - 3*day}},
		{`INSERT INTO blocks(id, session_id, origin, state, started_at, ended_at) VALUES ('blk_new', 'ses_x', 'user', 'finished', ?, ?)`, []any{now - day/2, now - day/2}},
		{`INSERT INTO block_chunks(block_id, seq, data_zstd) VALUES ('blk_old', 0, x'00'), ('blk_new', 0, x'00')`, nil},
		{`INSERT INTO threads(id, cwd, ephemeral, created_at, updated_at) VALUES ('thr_umb', '/', 1, ?, ?)`, []any{now - 2*day, now - 2*day}},
	} {
		if _, err := db.DB().ExecContext(t.Context(), q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	startDaemon(t, bin, rt)

	ro, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ro.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	for {
		var oldChunks, newChunks, threads int
		err := ro.QueryRowContext(ctx, `SELECT
			(SELECT count(*) FROM block_chunks WHERE block_id = 'blk_old'),
			(SELECT count(*) FROM block_chunks WHERE block_id = 'blk_new'),
			(SELECT count(*) FROM threads WHERE id = 'thr_umb')`).Scan(&oldChunks, &newChunks, &threads)
		if err == nil && oldChunks == 0 && threads == 0 {
			if newChunks != 1 {
				t.Fatalf("the job purged a block inside the window: %d chunks left", newChunks)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("retention never ran: old chunks %d, ephemeral thread %d (%v)", oldChunks, threads, err)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// TestRetentionWindowsComeFromTheSettings: the daemon hands the store the windows the settings
// read, and the two packages' defaults are Data Model §4's same four numbers.
func TestRetentionWindowsComeFromTheSettings(t *testing.T) {
	t.Parallel()
	if got := storeRetention(config.DefaultRetention()); got != store.DefaultRetention() {
		t.Fatalf("config's defaults %+v reach the store as %+v, want %+v", config.DefaultRetention(), got, store.DefaultRetention())
	}
	in := config.Retention{RawOutputDays: 1, PlainOutputDays: 2, ClosedStructureDays: 3, AuditDays: 4}
	if got := storeRetention(in); got != (store.Retention{RawOutputDays: 1, PlainOutputDays: 2, ClosedStructureDays: 3, AuditDays: 4}) {
		t.Fatalf("storeRetention(%+v) = %+v", in, got)
	}
}
