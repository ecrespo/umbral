// Package blockstore persists blocks and their output in SQLite (Data Model §2.2, §2.3).
//
// It is an adapter, not part of the store package, because the schema belongs to store and
// the meaning of these rows belongs to sessions. store owns the migrations; this owns the
// statements that fill them.
package blockstore

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"github.com/klauspost/compress/zstd"

	"github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/sessions/ports"
	"github.com/ecrespo/umbral/internal/store"
)

// Store writes blocks. One instance is shared by every session: SQLite serialises writes
// itself and the encoder below is safe for concurrent use.
type Store struct {
	db *sql.DB

	// encoder is reused across every block. Building one per chunk would allocate a
	// window per call, and a busy build writes thousands of chunks a second.
	encoder *zstd.Encoder
	once    sync.Once
}

// New builds a block store over an open database.
func New(st *store.Store) (*Store, error) {
	if st == nil {
		return nil, fmt.Errorf("blockstore: a Store is required")
	}
	// SpeedFastest is the right end of the dial here: the daemon compresses on the
	// goroutine draining a PTY, so time spent here is latency a person watching a build
	// can feel, and the ratio difference on terminal output, which is mostly ASCII with
	// long repeated runs, is small.
	encoder, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.SpeedFastest),
		zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, fmt.Errorf("blockstore: build the zstd encoder: %w", err)
	}
	return &Store{db: st.DB(), encoder: encoder}, nil
}

// Create writes a new block's row (REQ-BLK-001).
func (s *Store) Create(ctx context.Context, block domain.Block) error {
	if err := block.Validate(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO blocks(id, session_id, origin, thread_id, command, cwd, host,
		                   state, started_at, output_bytes, output_truncated)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0)`,
		block.ID, block.SessionID, string(block.Origin), nullString(block.ThreadID),
		block.Command, block.CWD, block.Host,
		string(block.State), block.StartedAt.UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("blockstore: create %s: %w", block.ID, err)
	}
	return nil
}

// AppendChunk stores one run of raw output, compressed (Data Model §2.3).
func (s *Store) AppendChunk(ctx context.Context, blockID string, seq int64, raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	compressed := s.encoder.EncodeAll(raw, nil)

	_, err := s.db.ExecContext(ctx,
		"INSERT INTO block_chunks(block_id, seq, data_zstd) VALUES (?, ?, ?)",
		blockID, seq, compressed)
	if err != nil {
		return fmt.Errorf("blockstore: append chunk %d of %s: %w", seq, blockID, err)
	}
	return nil
}

// SetState records a state change on an open block (REQ-BLK-004).
func (s *Store) SetState(ctx context.Context, blockID string, state domain.BlockState) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE blocks SET state = ? WHERE id = ?", string(state), blockID)
	if err != nil {
		return fmt.Errorf("blockstore: set the state of %s: %w", blockID, err)
	}
	return nil
}

// Finish closes the block (REQ-BLK-002, REQ-BLK-007).
//
// The plain text is written in the same statement as the state, so a block is never
// visible as finished without the transcript FTS5 is about to index from it.
func (s *Store) Finish(ctx context.Context, block domain.Block, plain string) error {
	var endedAt sql.NullInt64
	if block.EndedAt != nil {
		endedAt = sql.NullInt64{Int64: block.EndedAt.UTC().UnixMilli(), Valid: true}
	}
	var duration sql.NullInt64
	if ms := block.DurationMs(); ms != nil {
		duration = sql.NullInt64{Int64: *ms, Valid: true}
	}
	var exitCode sql.NullInt64
	if block.ExitCode != nil {
		exitCode = sql.NullInt64{Int64: int64(*block.ExitCode), Valid: true}
	}

	_, err := s.db.ExecContext(ctx, `
		UPDATE blocks
		   SET state = ?, exit_code = ?, ended_at = ?, duration_ms = ?,
		       output_bytes = ?, output_truncated = ?, output_plain = ?
		 WHERE id = ?`,
		string(block.State), exitCode, endedAt, duration,
		block.OutputBytes, boolToInt(block.OutputTruncated), plain, block.ID)
	if err != nil {
		return fmt.Errorf("blockstore: finish %s: %w", block.ID, err)
	}
	return nil
}

// Close releases the encoder. It is safe to call more than once.
func (s *Store) Close() error {
	var err error
	s.once.Do(func() { err = s.encoder.Close() })
	return err
}

// Decompress expands one stored chunk. It is here rather than in the reader that will need
// it in T-F0-10 so that the compression format has exactly one definition.
func Decompress(chunk []byte) ([]byte, error) {
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, fmt.Errorf("blockstore: build the zstd decoder: %w", err)
	}
	defer decoder.Close()

	raw, err := decoder.DecodeAll(chunk, nil)
	if err != nil {
		return nil, fmt.Errorf("blockstore: decompress: %w", err)
	}
	return raw, nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// Store implements the sessions module's BlockStore port.
var _ ports.BlockStore = (*Store)(nil)
