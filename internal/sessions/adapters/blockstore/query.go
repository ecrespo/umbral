package blockstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/sessions/ports"
)

// blockColumns is the projection every block query shares, in the order scanBlock reads.
const blockColumns = `b.id, b.session_id, b.origin, b.thread_id, b.command, b.cwd, b.host,
	b.state, b.exit_code, b.started_at, b.ended_at, b.output_bytes, b.output_truncated`

// List answers a filtered page of history, newest first (API Spec §5.10).
//
// The ordering is `started_at DESC, id DESC` and the cursor carries both, because start
// times collide: a script running ten commands inside one millisecond gives them all the
// same one, and paging on the timestamp alone would either repeat that group or skip it.
func (s *Store) List(ctx context.Context, filter domain.BlockFilter) (domain.BlockPage, error) {
	if err := filter.Validate(); err != nil {
		return domain.BlockPage{}, err
	}

	where := []string{"1 = 1"}
	args := []any{}
	if filter.SessionID != "" {
		where = append(where, "b.session_id = ?")
		args = append(args, filter.SessionID)
	}
	if filter.ThreadID != "" {
		where = append(where, "b.thread_id = ?")
		args = append(args, filter.ThreadID)
	}
	if filter.Origin != "" {
		where = append(where, "b.origin = ?")
		args = append(args, string(filter.Origin))
	}
	if filter.State != "" {
		where = append(where, "b.state = ?")
		args = append(args, string(filter.State))
	}
	if filter.ExitCode != nil {
		where = append(where, "b.exit_code = ?")
		args = append(args, *filter.ExitCode)
	}
	if !filter.Cursor.IsZero() {
		where = append(where, "(b.started_at < ? OR (b.started_at = ? AND b.id < ?))")
		millis := filter.Cursor.StartedAt.UTC().UnixMilli()
		args = append(args, millis, millis, filter.Cursor.BlockID)
	}

	// One row past the page is fetched to learn whether there is a next page, which is
	// cheaper and more honest than a second COUNT over the same predicate.
	args = append(args, filter.Limit+1)
	// The statement is assembled from constants only: `blockColumns` and every element of
	// `where` is a literal in this file, chosen by which filter fields were set. Each
	// caller-supplied value is a bound parameter in `args` and none of them reaches the
	// SQL text, so there is nothing here for an injection to travel through.
	//nolint:gosec // G202: every fragment is a compile-time constant; values are bound
	query := `SELECT ` + blockColumns + ` FROM blocks b WHERE ` +
		strings.Join(where, " AND ") + ` ORDER BY b.started_at DESC, b.id DESC LIMIT ?`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.BlockPage{}, fmt.Errorf("blockstore: list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var blocks []domain.Block
	for rows.Next() {
		block, err := scanBlock(rows)
		if err != nil {
			return domain.BlockPage{}, err
		}
		blocks = append(blocks, block)
	}
	if err := rows.Err(); err != nil {
		return domain.BlockPage{}, fmt.Errorf("blockstore: list: %w", err)
	}

	page := domain.BlockPage{Items: blocks}
	if len(blocks) > filter.Limit {
		page.Items = blocks[:filter.Limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = domain.Cursor{StartedAt: last.StartedAt, BlockID: last.ID}
	}
	return page, nil
}

// Get returns one block by id.
func (s *Store) Get(ctx context.Context, id string) (domain.Block, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+blockColumns+` FROM blocks b WHERE b.id = ?`, id)
	return scanBlock(row)
}

// Last returns the most recent closed block (REQ-CLI-002).
//
// Closed, not merely most recent: `umb block last` is asked what just happened, and a block
// that is still running has no exit code and an incomplete transcript, so answering with it
// would give the caller a half-written record of a command that has not finished.
func (s *Store) Last(ctx context.Context, sessionID string) (domain.Block, error) {
	query := `SELECT ` + blockColumns + ` FROM blocks b
		WHERE b.state IN ('finished','abandoned')`
	args := []any{}
	if sessionID != "" {
		query += ` AND b.session_id = ?`
		args = append(args, sessionID)
	}
	query += ` ORDER BY b.started_at DESC, b.id DESC LIMIT 1`

	return scanBlock(s.db.QueryRowContext(ctx, query, args...))
}

// Plain returns a block's transcript (REQ-BLK-007).
func (s *Store) Plain(ctx context.Context, id string) (string, error) {
	var plain sql.NullString
	err := s.db.QueryRowContext(ctx,
		"SELECT output_plain FROM blocks WHERE id = ?", id).Scan(&plain)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", fmt.Errorf("%w: block %s", domain.ErrNotFound, id)
	case err != nil:
		return "", fmt.Errorf("blockstore: read the transcript of %s: %w", id, err)
	}
	// A block still running has no transcript yet. Empty is the honest answer: the command
	// has not said anything that was stored, not that it said nothing.
	return plain.String, nil
}

// Raw reassembles a block's stored chunks, stopping at limit bytes.
func (s *Store) Raw(ctx context.Context, id string, limit int) ([]byte, bool, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT data_zstd FROM block_chunks WHERE block_id = ? ORDER BY seq", id)
	if err != nil {
		return nil, false, fmt.Errorf("blockstore: read the chunks of %s: %w", id, err)
	}
	defer func() { _ = rows.Close() }()

	var out []byte
	truncated := false
	for rows.Next() {
		var compressed []byte
		if err := rows.Scan(&compressed); err != nil {
			return nil, false, fmt.Errorf("blockstore: read the chunks of %s: %w", id, err)
		}
		chunk, err := Decompress(compressed)
		if err != nil {
			return nil, false, err
		}
		if room := limit - len(out); len(chunk) >= room {
			out = append(out, chunk[:room]...)
			// There is more, either in the rest of this chunk or in the ones after it.
			// `rows.Next()` is also false when the iteration failed, so the error is
			// checked before the answer is trusted: telling a caller it has the whole
			// block because a read broke is the one wrong answer here.
			truncated = len(chunk) > room || rows.Next()
			if err := rows.Err(); err != nil {
				return nil, false, fmt.Errorf("blockstore: read the chunks of %s: %w", id, err)
			}
			return out, truncated, nil
		}
		out = append(out, chunk...)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("blockstore: read the chunks of %s: %w", id, err)
	}
	return out, truncated, nil
}

// Search runs the FTS5 query over commands and transcripts (REQ-BLK-006).
//
// The external-content table holds no copy of the rows, so every hit is joined back to
// `blocks` on the rowid the triggers of Data Model §2.4 keep in step.
//
// The ordering is the reason this meets its 200 ms budget. Ordering by `started_at` puts a
// temporary B-tree over the whole match set, so a term matching half the history sorts fifty
// thousand rows to return fifty: 139 ms at 100,000 blocks, and no index helps, because the
// rows arrive from the full-text index in rowid order and have to be re-sorted whatever
// exists. Ordering by that rowid instead lets SQLite walk the full-text index backwards and
// stop at the limit: 0.45 ms, three hundred times faster. The two orderings agree, because a
// block's row is written when its command starts, so insertion order is start order by
// construction.
func (s *Store) Search(ctx context.Context, query domain.SearchQuery) (domain.SearchPage, error) {
	if err := query.Validate(); err != nil {
		return domain.SearchPage{}, err
	}

	where := []string{"blocks_fts MATCH ?"}
	args := []any{query.Query}
	if query.SessionID != "" {
		where = append(where, "b.session_id = ?")
		args = append(args, query.SessionID)
	}
	if !query.Cursor.IsZero() {
		if query.Cursor.Row == 0 {
			return domain.SearchPage{}, fmt.Errorf(
				"%w: that cursor belongs to block.list, not to block.search", domain.ErrValidation)
		}
		where = append(where, "blocks_fts.rowid < ?")
		args = append(args, query.Cursor.Row)
	}
	args = append(args, query.Limit+1)

	// Column 1 is output_plain. The ellipsis is what a client shows when the match is in
	// the middle of a long transcript; 20 tokens is enough to read a line of output around
	// the hit without carrying the whole block into a list.
	//
	// As in List, every fragment concatenated here is a literal in this file and every
	// caller-supplied value is a bound parameter, the search text included.
	//nolint:gosec // G202: every fragment is a compile-time constant; values are bound
	sqlText := `SELECT ` + blockColumns + `, snippet(blocks_fts, 1, '[', ']', '…', 20), blocks_fts.rowid
		FROM blocks_fts
		JOIN blocks b ON b.rowid = blocks_fts.rowid
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY blocks_fts.rowid DESC LIMIT ?`

	rows, err := s.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return domain.SearchPage{}, searchError(err)
	}
	defer func() { _ = rows.Close() }()

	var (
		hits     []domain.SearchHit
		lastRows []int64
	)
	for rows.Next() {
		block, snippet, row, err := scanHit(rows)
		if err != nil {
			return domain.SearchPage{}, err
		}
		hits = append(hits, domain.SearchHit{Block: block, Snippet: snippet})
		lastRows = append(lastRows, row)
	}
	if err := rows.Err(); err != nil {
		return domain.SearchPage{}, searchError(err)
	}

	page := domain.SearchPage{Items: hits}
	if len(hits) > query.Limit {
		page.Items = hits[:query.Limit]
		page.NextCursor = domain.SearchCursor(lastRows[query.Limit-1])
	}
	return page, nil
}

// searchError turns SQLite's complaint about a malformed query into a validation error.
//
// An unbalanced quote in a search box is the user's typo, not the daemon's fault, and
// answering INTERNAL_ERROR for it would put a trace id in the log for every mistyped query.
//
// The mapping is safe because of how the statement is built: its SQL text is a constant in
// this file and every other input is a bound parameter, so the MATCH argument is the only
// thing a caller can make ill-formed. SQLite reports all of its objections to it as logic
// errors, with wording that varies by the mistake: "unterminated string", "no such column",
// "unknown special query", "fts5: syntax error near". A missing table is the exception, and
// it means the schema is wrong rather than the query.
func searchError(err error) error {
	message := err.Error()
	if strings.Contains(message, "no such table") {
		return fmt.Errorf("blockstore: search: %w", err)
	}
	if strings.Contains(message, "SQL logic error") || strings.Contains(message, "fts5") {
		return fmt.Errorf("%w: the search query is not valid FTS5 syntax", domain.ErrValidation)
	}
	return fmt.Errorf("blockstore: search: %w", err)
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanBlock(row rowScanner) (domain.Block, error) {
	block, _, err := scanBlockWith(row, nil, nil)
	return block, err
}

// scanHit reads a search result: the block, its snippet and its position in insertion
// order, which is what the next page's cursor is built from.
func scanHit(row rowScanner) (domain.Block, string, int64, error) {
	var (
		snippet sql.NullString
		rowid   int64
	)
	block, _, err := scanBlockWith(row, &snippet, &rowid)
	return block, snippet.String, rowid, err
}

// scanBlockWith reads the shared projection, plus a snippet and a rowid when the caller
// asked for them.
func scanBlockWith(row rowScanner, snippet *sql.NullString, rowid *int64) (domain.Block, bool, error) {
	var (
		block                       domain.Block
		origin, state               string
		threadID                    sql.NullString
		exitCode                    sql.NullInt64
		startedAt, endedAt          sql.NullInt64
		outputBytes, outputTruncate int64
	)

	dest := []any{
		&block.ID, &block.SessionID, &origin, &threadID, &block.Command, &block.CWD,
		&block.Host, &state, &exitCode, &startedAt, &endedAt, &outputBytes, &outputTruncate,
	}
	if snippet != nil {
		dest = append(dest, snippet, rowid)
	}

	err := row.Scan(dest...)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.Block{}, false, fmt.Errorf("%w: block", domain.ErrNotFound)
	case err != nil:
		return domain.Block{}, false, fmt.Errorf("blockstore: scan: %w", err)
	}

	block.Origin = domain.BlockOrigin(origin)
	block.State = domain.BlockState(state)
	block.ThreadID = threadID.String
	block.StartedAt = time.UnixMilli(startedAt.Int64).UTC()
	block.OutputBytes = outputBytes
	block.OutputTruncated = outputTruncate == 1
	if exitCode.Valid {
		code := int(exitCode.Int64)
		block.ExitCode = &code
	}
	if endedAt.Valid {
		ended := time.UnixMilli(endedAt.Int64).UTC()
		block.EndedAt = &ended
	}
	return block, true, nil
}

// Store implements the reading half of the sessions module's block ports.
var _ ports.BlockReader = (*Store)(nil)
