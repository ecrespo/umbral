package blockstore

import (
	"context"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/store"
)

// benchBlocks is the history size REQ-BLK-006 names.
const benchBlocks = 100_000

// commandVocabulary and outputVocabulary build transcripts that look like a real history:
// a few commands repeated thousands of times, and output whose words repeat the way build
// logs do. A corpus of random strings would make every search match one row and hide the
// cost REQ-BLK-006 is really about, which is a query that matches a great many.
var (
	commandVocabulary = []string{
		"go test ./...", "go build ./...", "git status", "git log --oneline -20",
		"make lint", "npm run build", "kubectl get pods", "docker compose up -d",
		"rg TODO", "task ci",
	}
	outputVocabulary = []string{
		"ok", "FAIL", "PASS", "warning", "error", "deprecated", "compiling", "linking",
		"internal/store", "internal/api", "internal/sessions", "cmd/umbrald",
		"0.21s", "1.04s", "no changes", "3 files changed", "nothing to commit",
	}
)

// seedHistory builds a database with n blocks. It is deliberately not a committed fixture:
// a 100,000-row SQLite file is a binary blob nobody can review, and generating it from a
// fixed seed means the corpus is reproducible and readable in this file instead.
func seedHistory(tb testing.TB, n int) *Store {
	tb.Helper()

	db, err := store.Open(context.Background(), store.Options{
		Path: filepath.Join(tb.TempDir(), "umbral.db"),
	})
	if err != nil {
		tb.Fatalf("store.Open: %v", err)
	}
	tb.Cleanup(func() { _ = db.Close() })

	sessionID := store.NewID(store.PrefixSession)
	if _, err := db.DB().ExecContext(context.Background(), `
		INSERT INTO sessions(id, shell, cwd, cols, rows, state, created_at)
		VALUES (?, '/usr/bin/bash', '/repo', 120, 40, 'alive', 0)`, sessionID); err != nil {
		tb.Fatalf("seeding a session: %v", err)
	}

	// One transaction for the whole corpus. Row by row, the FTS triggers make this take
	// minutes rather than seconds, and the benchmark would spend its life in setup.
	tx, err := db.DB().BeginTx(context.Background(), nil)
	if err != nil {
		tb.Fatalf("begin: %v", err)
	}
	insert, err := tx.PrepareContext(context.Background(), `
		INSERT INTO blocks(id, session_id, origin, command, cwd, host, state,
		                   exit_code, started_at, ended_at, duration_ms,
		                   output_bytes, output_truncated, output_plain)
		VALUES (?, ?, 'user', ?, '/repo', 'thinkpad', 'finished', ?, ?, ?, ?, ?, 0, ?)`)
	if err != nil {
		tb.Fatalf("prepare: %v", err)
	}
	defer func() { _ = insert.Close() }()

	random := rand.New(rand.NewPCG(1, 2))
	base := time.UnixMilli(1_700_000_000_000).UTC()
	for i := range n {
		command := commandVocabulary[random.IntN(len(commandVocabulary))]
		plain := randomTranscript(random)
		startedAt := base.Add(time.Duration(i) * time.Second).UnixMilli()
		if _, err := insert.ExecContext(context.Background(),
			store.NewID(store.PrefixBlock), sessionID, command,
			random.IntN(2), startedAt, startedAt+1000, 1000, len(plain), plain,
		); err != nil {
			tb.Fatalf("insert %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		tb.Fatalf("commit: %v", err)
	}

	blocks, err := New(db)
	if err != nil {
		tb.Fatalf("blockstore.New: %v", err)
	}
	tb.Cleanup(func() { _ = blocks.Close() })
	return blocks
}

// randomTranscript builds a dozen lines of plausible build output.
func randomTranscript(random *rand.Rand) string {
	var out []byte
	for range 12 {
		for word := range 6 {
			if word > 0 {
				out = append(out, ' ')
			}
			out = append(out, outputVocabulary[random.IntN(len(outputVocabulary))]...)
		}
		out = append(out, '\n')
	}
	return string(out)
}

// BenchmarkBlockSearch100k_REQ_BLK_006 measures `block.search` against the 100,000-block
// history REQ-BLK-006 names, and fails the run if p95 misses the 200 ms budget.
//
// It reports p95 rather than the mean because that is what the requirement is written in,
// and because the mean of a search that is fast for rare terms and slow for common ones
// says nothing about either.
func BenchmarkBlockSearch100k_REQ_BLK_006(b *testing.B) {
	blocks := seedHistory(b, benchBlocks)

	// The queries are FTS5 as API Spec §5.12 specifies, so a path has to be quoted: a bare
	// slash is a syntax error in that grammar, and quoting is what a client typing a path
	// into a search box has to do.
	queries := []string{
		`FAIL`,                // matches a large fraction of the corpus
		`"internal/sessions"`, // a path, as a quoted phrase
		`deprecated warning`,  // two common terms, implicit AND
		`nothing`,             // rarer
		`compiling AND error`, // explicit boolean
	}

	var samples []time.Duration
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		query := queries[i%len(queries)]
		started := time.Now()
		page, err := blocks.Search(b.Context(), domain.SearchQuery{Query: query, Limit: 50})
		elapsed := time.Since(started)
		if err != nil {
			b.Fatalf("Search(%q): %v", query, err)
		}
		if len(page.Items) == 0 {
			b.Fatalf("Search(%q) found nothing in a corpus built to contain it", query)
		}
		samples = append(samples, elapsed)
	}
	b.StopTimer()

	p95 := percentile(samples, 0.95)
	b.ReportMetric(float64(p95.Microseconds())/1000, "p95_ms")
	if p95 > 200*time.Millisecond {
		b.Fatalf("block.search p95 is %v over %d queries against %d blocks, budget is 200 ms (REQ-BLK-006)",
			p95, len(samples), benchBlocks)
	}
}

// BenchmarkBlockListPage100k measures the paging path the TUI scrolls with. It has no
// budget in the PRD; it is here because a list that got slower than the search would be a
// surprise worth seeing.
func BenchmarkBlockListPage100k(b *testing.B) {
	blocks := seedHistory(b, benchBlocks)

	var samples []time.Duration
	b.ResetTimer()
	for b.Loop() {
		started := time.Now()
		page, err := blocks.List(b.Context(), domain.BlockFilter{Limit: 50})
		elapsed := time.Since(started)
		if err != nil {
			b.Fatalf("List: %v", err)
		}
		if len(page.Items) != 50 {
			b.Fatalf("List returned %d rows, want 50", len(page.Items))
		}
		samples = append(samples, elapsed)
	}
	b.StopTimer()
	b.ReportMetric(float64(percentile(samples, 0.95).Microseconds())/1000, "p95_ms")
}

func percentile(samples []time.Duration, fraction float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	sorted := slices.Clone(samples)
	slices.Sort(sorted)
	index := int(float64(len(sorted)-1) * fraction)
	return sorted[index]
}

// TestSearchCorpusIsSearchable guards the benchmark's premise.
//
// A benchmark whose queries match nothing would report a very fast search and prove
// nothing, so the corpus is checked at a size small enough to run in the ordinary suite.
func TestSearchCorpusIsSearchable(t *testing.T) {
	blocks := seedHistory(t, 2000)

	for _, query := range []string{`FAIL`, `"internal/sessions"`, `deprecated warning`, `nothing`} {
		page, err := blocks.Search(t.Context(), domain.SearchQuery{Query: query, Limit: 50})
		if err != nil {
			t.Fatalf("Search(%q): %v", query, err)
		}
		if len(page.Items) == 0 {
			t.Errorf("Search(%q) found nothing in the generated corpus", query)
		}
	}
}
