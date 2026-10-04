package store

import (
	"testing"
	"time"
)

// The retention job (Data Model §4, T-F1-22). Every test runs the job at retentionNow over rows
// written relative to it, so "older than the window" is a fact of the fixture and not of the
// clock the test happens to run on.

var retentionNow = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

func daysAgo(d float64) int64 {
	return retentionNow.Add(-time.Duration(d * 24 * float64(time.Hour))).UnixMilli()
}

func exec(t *testing.T, s *Store, query string, args ...any) {
	t.Helper()
	if _, err := s.DB().ExecContext(t.Context(), query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func count(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRowContext(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// closedBlock writes a finished user block that ended `ended` ms since the epoch, with a
// transcript and two raw chunks.
func closedBlock(t *testing.T, s *Store, id, session string, ended int64) {
	t.Helper()
	exec(t, s, `INSERT INTO blocks(id, session_id, origin, command, state, started_at, ended_at, output_bytes, output_plain)
		VALUES (?, ?, 'user', 'go test ./...', 'finished', ?, ?, 20, 'FAIL needle-'||?)`, id, session, ended-1000, ended, id)
	for seq := range 2 {
		exec(t, s, `INSERT INTO block_chunks(block_id, seq, data_zstd) VALUES (?, ?, x'00')`, id, seq)
	}
}

// TestRetentionPurgesRawChunks is T-F1-22's Done: a block's raw chunks are deleted once it has
// been closed for longer than `retention.raw_output_days` (30 by default); its row, its
// transcript and its search entry stay, and an open block is never touched however old.
func TestRetentionPurgesRawChunks(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	insertSession(t, s, "ses_a", "exited")
	closedBlock(t, s, "blk_old", "ses_a", daysAgo(31))
	closedBlock(t, s, "blk_recent", "ses_a", daysAgo(29))
	exec(t, s, `INSERT INTO blocks(id, session_id, origin, command, state, started_at, output_bytes)
		VALUES ('blk_running', 'ses_a', 'user', 'sleep inf', 'running', ?, 5)`, daysAgo(90))
	exec(t, s, `INSERT INTO block_chunks(block_id, seq, data_zstd) VALUES ('blk_running', 0, x'00')`)
	// An abandoned block that never recorded its end is judged by its start.
	exec(t, s, `INSERT INTO blocks(id, session_id, origin, command, state, started_at, output_bytes)
		VALUES ('blk_abandoned', 'ses_a', 'user', 'make', 'abandoned', ?, 5)`, daysAgo(40))
	exec(t, s, `INSERT INTO block_chunks(block_id, seq, data_zstd) VALUES ('blk_abandoned', 0, x'00')`)

	report, err := s.ApplyRetention(t.Context(), retentionNow, DefaultRetention())
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]int{"blk_old": 0, "blk_recent": 2, "blk_running": 1, "blk_abandoned": 0} {
		if got := count(t, s, `SELECT count(*) FROM block_chunks WHERE block_id = ?`, id); got != want {
			t.Errorf("%s keeps %d chunks, want %d", id, got, want)
		}
	}
	if report.RawChunks != 3 {
		t.Errorf("report.RawChunks = %d, want 3", report.RawChunks)
	}
	if got := count(t, s, `SELECT count(*) FROM blocks WHERE id = 'blk_old' AND output_plain IS NOT NULL AND output_bytes = 20`); got != 1 {
		t.Error("purging raw output took the block's row, transcript or size with it")
	}
	if got := count(t, s, `SELECT count(*) FROM blocks_fts WHERE blocks_fts MATCH '"needle-blk_old"'`); got != 1 {
		t.Error("the block's transcript left the search index with its raw chunks")
	}

	// A shorter window, as `retention.raw_output_days` sets it, reaches the recent block too.
	r := DefaultRetention()
	r.RawOutputDays = 7
	if _, err := s.ApplyRetention(t.Context(), retentionNow, r); err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, `SELECT count(*) FROM block_chunks WHERE block_id = 'blk_recent'`); got != 0 {
		t.Errorf("a 7-day window kept %d chunks of a 29-day-old block", got)
	}
}

// TestRetentionClearsOldTranscripts_REQ_BLK_006: past `retention.plain_output_days` (180) a
// block's transcript is cleared and leaves the search index; the block, its command and its
// command's search entry stay.
func TestRetentionClearsOldTranscripts_REQ_BLK_006(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	insertSession(t, s, "ses_a", "exited")
	closedBlock(t, s, "blk_old", "ses_a", daysAgo(181))
	closedBlock(t, s, "blk_recent", "ses_a", daysAgo(179))

	report, err := s.ApplyRetention(t.Context(), retentionNow, DefaultRetention())
	if err != nil {
		t.Fatal(err)
	}
	if report.Transcripts != 1 {
		t.Errorf("report.Transcripts = %d, want 1", report.Transcripts)
	}
	if got := count(t, s, `SELECT count(*) FROM blocks WHERE output_plain IS NULL`); got != 1 {
		t.Errorf("%d transcripts cleared, want 1", got)
	}
	if got := count(t, s, `SELECT count(*) FROM blocks_fts WHERE blocks_fts MATCH '"needle-blk_old"'`); got != 0 {
		t.Error("a cleared transcript is still found by search")
	}
	if got := count(t, s, `SELECT count(*) FROM blocks_fts WHERE blocks_fts MATCH '"needle-blk_recent"'`); got != 1 {
		t.Error("a transcript inside the window left the index")
	}
	if got := count(t, s, `SELECT count(*) FROM blocks_fts WHERE blocks_fts MATCH 'command:test'`); got != 2 {
		t.Errorf("the commands' search entries: %d, want both", got)
	}
}

// TestRetentionPurgesEphemeralThreads: an ephemeral thread (`umb ai`) idle for 24 h
// is deleted with everything that is only its — messages, tool calls, approvals, its agent
// blocks — while its usage rows stay as audit with no thread; a non-ephemeral thread, a
// recent one and one whose turn is still running are kept.
func TestRetentionPurgesEphemeralThreads(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	thread := func(id string, ephemeral int, state string, updated int64) {
		exec(t, s, `INSERT INTO threads(id, cwd, state, ephemeral, created_at, updated_at) VALUES (?, '/w', ?, ?, ?, ?)`,
			id, state, ephemeral, updated, updated)
		exec(t, s, `INSERT INTO messages(id, thread_id, turn_id, role, content, created_at) VALUES (?, ?, 'trn_1', 'user', 'hi', ?)`,
			"msg_"+id, id, updated)
	}
	thread("thr_old", 1, "idle", daysAgo(2))
	thread("thr_new", 1, "idle", daysAgo(0.5))
	thread("thr_kept", 0, "idle", daysAgo(400))
	thread("thr_busy", 1, "running", daysAgo(2))
	// thr_old ran a command in its own PTY: a session it owns, an agent block, a tool call
	// pointing at the block, an approval of that call, and a usage row.
	insertSession(t, s, "ses_agent", "exited")
	exec(t, s, `UPDATE sessions SET owner_thread_id = 'thr_old' WHERE id = 'ses_agent'`)
	exec(t, s, `INSERT INTO blocks(id, session_id, origin, thread_id, command, state, started_at, ended_at)
		VALUES ('blk_agent', 'ses_agent', 'agent', 'thr_old', 'go test', 'finished', ?, ?)`, daysAgo(2), daysAgo(2))
	exec(t, s, `INSERT INTO tool_calls(id, thread_id, message_id, tool, risk, args_json, status, block_id, started_at)
		VALUES ('tc_1', 'thr_old', 'msg_thr_old', 'run_command', 'Exec', '{}', 'ok', 'blk_agent', ?)`, daysAgo(2))
	exec(t, s, `INSERT INTO approvals(id, thread_id, tool_call_id, tool, risk, reason, summary, state, created_at)
		VALUES ('apr_1', 'thr_old', 'tc_1', 'run_command', 'Exec', 'policy', 'go test', 'approved', ?)`, daysAgo(2))
	exec(t, s, `INSERT INTO usage(thread_id, model_id, provider, status, created_at) VALUES ('thr_old', 'ollama/m', 'ollama', 'ok', ?)`, daysAgo(2))

	report, err := s.ApplyRetention(t.Context(), retentionNow, DefaultRetention())
	if err != nil {
		t.Fatal(err)
	}
	if report.EphemeralThreads != 1 {
		t.Errorf("report.EphemeralThreads = %d, want 1", report.EphemeralThreads)
	}
	for _, id := range []string{"thr_new", "thr_kept", "thr_busy"} {
		if count(t, s, `SELECT count(*) FROM threads WHERE id = ?`, id) != 1 || count(t, s, `SELECT count(*) FROM messages WHERE thread_id = ?`, id) != 1 {
			t.Errorf("%s was purged", id)
		}
	}
	for table, query := range map[string]string{
		"threads":    `SELECT count(*) FROM threads WHERE id = 'thr_old'`,
		"messages":   `SELECT count(*) FROM messages WHERE thread_id = 'thr_old'`,
		"tool_calls": `SELECT count(*) FROM tool_calls`,
		"approvals":  `SELECT count(*) FROM approvals`,
		"blocks":     `SELECT count(*) FROM blocks WHERE id = 'blk_agent'`,
	} {
		if got := count(t, s, query); got != 0 {
			t.Errorf("%s still holds %d rows of the purged thread", table, got)
		}
	}
	if got := count(t, s, `SELECT count(*) FROM usage WHERE thread_id IS NULL`); got != 1 {
		t.Error("the purged thread's usage row did not stay as audit")
	}
	if got := count(t, s, `SELECT count(*) FROM sessions WHERE id = 'ses_agent' AND owner_thread_id IS NULL`); got != 1 {
		t.Error("the thread's session was not kept, released from its owner")
	}
}

// TestAThreadInUseIsKept: a thread whose PTY session is still alive, or
// whose block is still open, is not purged — they are in use, whatever the thread's age.
func TestAThreadInUseIsKept(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	exec(t, s, `INSERT INTO threads(id, cwd, ephemeral, created_at, updated_at) VALUES ('thr_old', '/w', 1, ?, ?)`, daysAgo(3), daysAgo(3))
	insertSession(t, s, "ses_agent", "alive")
	exec(t, s, `UPDATE sessions SET owner_thread_id = 'thr_old' WHERE id = 'ses_agent'`)
	if _, err := s.ApplyRetention(t.Context(), retentionNow, DefaultRetention()); err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, `SELECT count(*) FROM threads WHERE id = 'thr_old'`); got != 1 {
		t.Error("a thread with a live session was purged")
	}

	exec(t, s, `INSERT INTO threads(id, cwd, ephemeral, created_at, updated_at) VALUES ('thr_open', '/w', 1, ?, ?)`, daysAgo(3), daysAgo(3))
	insertSession(t, s, "ses_done", "exited")
	exec(t, s, `INSERT INTO blocks(id, session_id, origin, thread_id, command, state, started_at)
		VALUES ('blk_open', 'ses_done', 'agent', 'thr_open', 'sleep', 'running', ?)`, daysAgo(3))
	if _, err := s.ApplyRetention(t.Context(), retentionNow, DefaultRetention()); err != nil {
		t.Fatalf("a thread with an open block stopped the job: %v", err)
	}
	if got := count(t, s, `SELECT count(*) FROM threads WHERE id = 'thr_open'`); got != 1 {
		t.Error("a thread with an open block was purged")
	}
}

// TestRetentionPurgesClosedStructure: workspaces, tabs and panes closed for longer
// than `retention.closed_structure_days` (30) are deleted, with what cascades from them; open
// ones and recently closed ones stay.
func TestRetentionPurgesClosedStructure(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	ws := func(id string, closed *int64) {
		exec(t, s, `INSERT INTO workspaces(id, label, cwd, created_at, closed_at) VALUES (?, ?, '/w', ?, ?)`, id, id, daysAgo(100), closed)
	}
	tab := func(id, w string, closed *int64) {
		exec(t, s, `INSERT INTO tabs(id, workspace_id, label, created_at, closed_at) VALUES (?, ?, 't', ?, ?)`, id, w, daysAgo(100), closed)
	}
	pane := func(id, tb string, closed *int64) {
		exec(t, s, `INSERT INTO panes(id, tab_id, cwd, created_at, closed_at) VALUES (?, ?, '/w', ?, ?)`, id, tb, daysAgo(100), closed)
	}
	old, recent := daysAgo(31), daysAgo(29)
	ws("w1", &old)
	tab("w1:t1", "w1", &old)
	pane("w1:p1", "w1:t1", &old)
	ws("w2", nil)
	tab("w2:t1", "w2", nil)
	pane("w2:p1", "w2:t1", nil)
	pane("w2:p2", "w2:t1", &old)
	pane("w2:p3", "w2:t1", &recent)
	tab("w2:t2", "w2", &old)
	pane("w2:p4", "w2:t2", nil)
	ws("w3", &recent)
	exec(t, s, `INSERT INTO pane_metadata(pane_id, source, key, value, updated_at) VALUES ('w2:p2', 'ext', 'k', 'v', ?)`, daysAgo(0))

	report, err := s.ApplyRetention(t.Context(), retentionNow, DefaultRetention())
	if err != nil {
		t.Fatal(err)
	}
	gone := []string{"w1", "w1:t1", "w1:p1", "w2:p2", "w2:t2", "w2:p4"}
	kept := []string{"w2", "w2:t1", "w2:p1", "w2:p3", "w3"}
	exists := func(id string) bool {
		return count(t, s, `SELECT (SELECT count(*) FROM workspaces WHERE id = ?1) + (SELECT count(*) FROM tabs WHERE id = ?1) + (SELECT count(*) FROM panes WHERE id = ?1)`, id) == 1
	}
	for _, id := range gone {
		if exists(id) {
			t.Errorf("%s was kept", id)
		}
	}
	for _, id := range kept {
		if !exists(id) {
			t.Errorf("%s was purged", id)
		}
	}
	if report.ClosedStructure != 3 {
		t.Errorf("report.ClosedStructure = %d, want 3 (w1, w2:t2, w2:p2; the rest cascade)", report.ClosedStructure)
	}
}

// TestRetentionPurgesAuditAndExpiredMetadata: `egress_log` and `usage` are kept
// `retention.audit_days` (365) and then deleted; pane metadata is deleted once its own TTL has
// passed, and at the latest 24 h after it was written (Data Model §4).
func TestRetentionPurgesAuditAndExpiredMetadata(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	for _, at := range []int64{daysAgo(366), daysAgo(364)} {
		exec(t, s, `INSERT INTO egress_log(provider, host, bytes, payload_sha256, created_at) VALUES ('p', 'h', 1, ?, ?)`,
			"0000000000000000000000000000000000000000000000000000000000000000", at)
		exec(t, s, `INSERT INTO usage(model_id, provider, status, created_at) VALUES ('m', 'p', 'ok', ?)`, at)
	}
	exec(t, s, `INSERT INTO workspaces(id, label, cwd, created_at) VALUES ('w1', 'w', '/w', 1)`)
	exec(t, s, `INSERT INTO tabs(id, workspace_id, label, created_at) VALUES ('w1:t1', 'w1', 't', 1)`)
	exec(t, s, `INSERT INTO panes(id, tab_id, cwd, created_at) VALUES ('w1:p1', 'w1:t1', '/w', 1)`)
	meta := func(key string, expires *int64, updated int64) {
		exec(t, s, `INSERT INTO pane_metadata(pane_id, source, key, value, expires_at, updated_at) VALUES ('w1:p1', 'ext', ?, 'v', ?, ?)`, key, expires, updated)
	}
	past, future := daysAgo(0.01), retentionNow.Add(time.Hour).UnixMilli()
	meta("expired", &past, daysAgo(0.1))
	meta("live", &future, daysAgo(0.1))
	meta("stale", nil, daysAgo(1.1))
	meta("fresh", nil, daysAgo(0.5))

	report, err := s.ApplyRetention(t.Context(), retentionNow, DefaultRetention())
	if err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, `SELECT count(*) FROM egress_log`) + count(t, s, `SELECT count(*) FROM usage`); got != 2 {
		t.Errorf("%d audit rows left, want the 2 inside the year", got)
	}
	if report.AuditRows != 2 {
		t.Errorf("report.AuditRows = %d, want 2", report.AuditRows)
	}
	var keys []string
	rows, err := s.DB().QueryContext(t.Context(), `SELECT key FROM pane_metadata ORDER BY key`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != "fresh" || keys[1] != "live" {
		t.Errorf("pane metadata left: %v, want [fresh live]", keys)
	}
}

// TestRetentionIsBatched: a purge larger than one batch is completed in one run, so a job that
// keeps each write transaction short still reaches the end.
func TestRetentionIsBatched(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	insertSession(t, s, "ses_a", "exited")
	for i := range retentionBatch*2 + 7 {
		closedBlock(t, s, "blk_"+time.Duration(i).String(), "ses_a", daysAgo(200))
	}
	report, err := s.ApplyRetention(t.Context(), retentionNow, DefaultRetention())
	if err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, `SELECT count(*) FROM block_chunks`); got != 0 {
		t.Errorf("%d chunks left after one run", got)
	}
	if got := count(t, s, `SELECT count(*) FROM blocks WHERE output_plain IS NOT NULL`); got != 0 {
		t.Errorf("%d transcripts left after one run", got)
	}
	if report.RawChunks != int64(2*(retentionBatch*2+7)) {
		t.Errorf("report.RawChunks = %d", report.RawChunks)
	}
}

// TestRetentionBatchesAreBoundedInRows: no statement of the job touches more than
// retentionBatch rows, raw chunks included — one block holds up to 256 of them, so bounding
// the blocks would let one transaction delete 128,000 rows while a turn waits to write.
func TestRetentionBatchesAreBoundedInRows(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	insertSession(t, s, "ses_a", "exited")
	exec(t, s, `INSERT INTO blocks(id, session_id, origin, command, state, started_at, ended_at)
		VALUES ('blk_big', 'ses_a', 'user', 'make', 'finished', ?, ?)`, daysAgo(40), daysAgo(40))
	for seq := range retentionBatch + 50 {
		exec(t, s, `INSERT INTO block_chunks(block_id, seq, data_zstd) VALUES ('blk_big', ?, x'00')`, seq)
	}
	var largest int64
	s.onRetentionBatch = func(n int64) { largest = max(largest, n) }
	if _, err := s.ApplyRetention(t.Context(), retentionNow, DefaultRetention()); err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, `SELECT count(*) FROM block_chunks`); got != 0 {
		t.Fatalf("%d chunks left", got)
	}
	if largest > retentionBatch {
		t.Fatalf("one batch touched %d rows, over the bound of %d", largest, retentionBatch)
	}
}

// TestAThreadThatStoppedQualifyingIsNotDeleted: the idle conditions are checked again inside
// the thread's own transaction, so a thread a send reached after the listing keeps its
// messages, and a thread that cannot be purged is skipped rather than stopping the job.
func TestAThreadThatStoppedQualifyingIsNotDeleted(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	exec(t, s, `INSERT INTO threads(id, cwd, state, ephemeral, created_at, updated_at) VALUES ('thr_a', '/w', 'running', 1, ?, ?)`, daysAgo(3), daysAgo(3))
	exec(t, s, `INSERT INTO messages(id, thread_id, turn_id, role, content, created_at) VALUES ('msg_a', 'thr_a', 'trn_1', 'user', 'hi', 1)`)
	deleted, err := s.deleteThread(t.Context(), "thr_a", daysAgo(1))
	if err != nil || deleted {
		t.Fatalf("deleteThread on a running thread = %v, %v", deleted, err)
	}
	if count(t, s, `SELECT count(*) FROM messages WHERE thread_id = 'thr_a'`) != 1 {
		t.Fatal("a thread that stopped qualifying lost its messages")
	}
}

// TestAThreadThatCannotBeDeletedDoesNotStopTheJob: a thread whose rows another thread still
// references fails its own transaction; the job skips it, purges the rest and finishes.
func TestAThreadThatCannotBeDeletedDoesNotStopTheJob(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	for _, id := range []string{"thr_stuck", "thr_free"} {
		exec(t, s, `INSERT INTO threads(id, cwd, ephemeral, created_at, updated_at) VALUES (?, '/w', 1, ?, ?)`, id, daysAgo(3), daysAgo(3))
	}
	exec(t, s, `INSERT INTO threads(id, cwd, created_at, updated_at) VALUES ('thr_other', '/w', 1, 1)`)
	exec(t, s, `INSERT INTO messages(id, thread_id, turn_id, role, content, created_at) VALUES ('msg_o', 'thr_other', 'trn_1', 'user', 'x', 1)`)
	insertSession(t, s, "ses_a", "exited")
	exec(t, s, `INSERT INTO blocks(id, session_id, origin, thread_id, command, state, started_at, ended_at)
		VALUES ('blk_stuck', 'ses_a', 'agent', 'thr_stuck', 'ls', 'finished', 1, 2)`)
	// Another thread's tool call points at thr_stuck's block, which therefore cannot go.
	exec(t, s, `INSERT INTO tool_calls(id, thread_id, message_id, tool, risk, args_json, status, block_id, started_at)
		VALUES ('tc_o', 'thr_other', 'msg_o', 'run_command', 'Exec', '{}', 'ok', 'blk_stuck', 1)`)

	report, err := s.ApplyRetention(t.Context(), retentionNow, DefaultRetention())
	if err != nil {
		t.Fatalf("one thread that cannot be deleted stopped the job: %v", err)
	}
	if report.EphemeralThreads != 1 || count(t, s, `SELECT count(*) FROM threads WHERE id = 'thr_free'`) != 0 {
		t.Fatalf("the purgeable thread was not purged: %+v", report)
	}
	if count(t, s, `SELECT count(*) FROM threads WHERE id = 'thr_stuck'`) != 1 {
		t.Fatal("the stuck thread is half gone")
	}
}
