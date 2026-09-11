package blockstore

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/store"
)

// newStore opens a database with migration 0001 applied and one session to hang blocks off,
// since every block row holds a foreign key against one.
func newStore(t *testing.T) (*Store, *store.Store, string) {
	t.Helper()

	db, err := store.Open(t.Context(), store.Options{Path: filepath.Join(t.TempDir(), "umbral.db")})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	sessionID := store.NewID(store.PrefixSession)
	if _, err := db.DB().ExecContext(t.Context(), `
		INSERT INTO sessions(id, shell, cwd, cols, rows, state, created_at)
		VALUES (?, '/bin/sh', '/', 80, 24, 'alive', 0)`, sessionID); err != nil {
		t.Fatalf("seeding a session: %v", err)
	}

	blocks, err := New(db)
	if err != nil {
		t.Fatalf("blockstore.New: %v", err)
	}
	t.Cleanup(func() { _ = blocks.Close() })

	return blocks, db, sessionID
}

func TestStoreRoundTripsABlockAndItsChunks(t *testing.T) {
	blocks, db, sessionID := newStore(t)

	startedAt := time.UnixMilli(1_757_592_000_000).UTC()
	endedAt := startedAt.Add(1500 * time.Millisecond)
	exitCode := 2
	block := domain.Block{
		ID: store.NewID(store.PrefixBlock), SessionID: sessionID, Origin: domain.OriginUser,
		Command: "go build ./...", CWD: "/home/u/repo", Host: "thinkpad",
		State: domain.BlockRunning, StartedAt: startedAt,
	}

	if err := blocks.Create(t.Context(), block); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Two chunks, so the ordering by seq is actually exercised.
	first := []byte("\x1b[1;31mfirst chunk\x1b[0m\r\n")
	second := []byte(strings.Repeat("second ", 4096))
	if err := blocks.AppendChunk(t.Context(), block.ID, 0, first); err != nil {
		t.Fatalf("AppendChunk 0: %v", err)
	}
	if err := blocks.AppendChunk(t.Context(), block.ID, 1, second); err != nil {
		t.Fatalf("AppendChunk 1: %v", err)
	}

	block.State = domain.BlockFinished
	block.ExitCode = &exitCode
	block.EndedAt = &endedAt
	block.OutputBytes = int64(len(first) + len(second))
	block.OutputTruncated = true
	if err := blocks.Finish(t.Context(), block, "first chunk\n"); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	var (
		state, plain string
		exit, dur    int64
		bytes        int64
		truncated    int
	)
	if err := db.DB().QueryRowContext(t.Context(), `
		SELECT state, exit_code, duration_ms, output_bytes, output_truncated, output_plain
		  FROM blocks WHERE id = ?`, block.ID).
		Scan(&state, &exit, &dur, &bytes, &truncated, &plain); err != nil {
		t.Fatalf("reading the row: %v", err)
	}
	switch {
	case state != "finished":
		t.Errorf("state is %q", state)
	case exit != 2:
		t.Errorf("exit_code is %d, want 2", exit)
	case dur != 1500:
		t.Errorf("duration_ms is %d, want 1500", dur)
	case bytes != int64(len(first)+len(second)):
		t.Errorf("output_bytes is %d", bytes)
	case truncated != 1:
		t.Errorf("output_truncated is %d, want 1", truncated)
	case plain != "first chunk\n":
		t.Errorf("output_plain is %q", plain)
	}

	// The chunks must come back byte for byte: they are what a faithful re-render uses,
	// so a lossy round trip here is a corrupted replay later.
	rows, err := db.DB().QueryContext(t.Context(),
		"SELECT data_zstd FROM block_chunks WHERE block_id = ? ORDER BY seq", block.ID)
	if err != nil {
		t.Fatalf("reading the chunks: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var restored []byte
	var stored int
	for rows.Next() {
		var compressed []byte
		if err := rows.Scan(&compressed); err != nil {
			t.Fatalf("scanning a chunk: %v", err)
		}
		stored += len(compressed)
		raw, err := Decompress(compressed)
		if err != nil {
			t.Fatalf("Decompress: %v", err)
		}
		restored = append(restored, raw...)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading the chunks: %v", err)
	}

	want := string(first) + string(second)
	if string(restored) != want {
		t.Errorf("the chunks did not round-trip: got %d bytes, want %d", len(restored), len(want))
	}
	// Terminal output is long repeated runs of ASCII, which is what zstd is for. A store
	// that grew the data would be worse than no compression at all.
	if stored >= len(want) {
		t.Errorf("the stored chunks are %d bytes for %d of output", stored, len(want))
	}
}

func TestStoreRefusesAnAgentBlockWithNoThread(t *testing.T) {
	blocks, _, sessionID := newStore(t)

	// The schema's CHECK says the same thing. Refusing here means the caller gets the
	// domain's validation error rather than a constraint violation from SQLite.
	err := blocks.Create(t.Context(), domain.Block{
		ID: store.NewID(store.PrefixBlock), SessionID: sessionID,
		Origin: domain.OriginAgent, State: domain.BlockRunning,
		StartedAt: time.UnixMilli(0).UTC(),
	})
	if err == nil {
		t.Fatal("an agent block with no thread was accepted")
	}
	if !strings.Contains(err.Error(), "thread") {
		t.Errorf("error is %v, want it to name the missing thread", err)
	}
}

func TestStoreEmptyChunkIsNotStored(t *testing.T) {
	blocks, db, sessionID := newStore(t)

	block := domain.Block{
		ID: store.NewID(store.PrefixBlock), SessionID: sessionID, Origin: domain.OriginUser,
		State: domain.BlockRunning, StartedAt: time.UnixMilli(0).UTC(),
	}
	if err := blocks.Create(t.Context(), block); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := blocks.AppendChunk(t.Context(), block.ID, 0, nil); err != nil {
		t.Fatalf("AppendChunk: %v", err)
	}

	var count int
	if err := db.DB().QueryRowContext(t.Context(),
		"SELECT count(*) FROM block_chunks WHERE block_id = ?", block.ID).Scan(&count); err != nil {
		t.Fatalf("counting chunks: %v", err)
	}
	if count != 0 {
		t.Errorf("stored %d chunks for no output, want none", count)
	}
}
