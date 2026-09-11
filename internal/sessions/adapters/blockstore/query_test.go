package blockstore

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/store"
)

// seed writes a block with the given command, transcript and start time, so a test can
// arrange a history whose order and content it knows exactly.
func seed(t *testing.T, s *Store, sessionID, command, plain string, startedAt time.Time, state domain.BlockState) domain.Block {
	t.Helper()

	block := domain.Block{
		ID: store.NewID(store.PrefixBlock), SessionID: sessionID, Origin: domain.OriginUser,
		Command: command, CWD: "/repo", Host: "thinkpad",
		State: domain.BlockRunning, StartedAt: startedAt,
	}
	if err := s.Create(t.Context(), block); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if state == domain.BlockRunning {
		return block
	}

	endedAt := startedAt.Add(time.Second)
	exitCode := 0
	block.State = state
	block.EndedAt = &endedAt
	block.ExitCode = &exitCode
	block.OutputBytes = int64(len(plain))
	if err := s.Finish(t.Context(), block, plain); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	return block
}

func TestListPagesNewestFirst(t *testing.T) {
	blocks, _, sessionID := newStore(t)

	base := time.UnixMilli(1_757_592_000_000).UTC()
	created := make([]domain.Block, 0, 7)
	for i := range 7 {
		created = append(created, seed(t, blocks, sessionID,
			fmt.Sprintf("command %d", i), "output", base.Add(time.Duration(i)*time.Second),
			domain.BlockFinished))
	}

	var seen []string
	cursor := domain.Cursor{}
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("paging did not terminate")
		}
		page, err := blocks.List(t.Context(), domain.BlockFilter{
			SessionID: sessionID, Limit: 3, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, block := range page.Items {
			seen = append(seen, block.Command)
		}
		if page.NextCursor.IsZero() {
			break
		}
		cursor = page.NextCursor
	}

	if len(seen) != len(created) {
		t.Fatalf("paged through %d blocks, want %d: %v", len(seen), len(created), seen)
	}
	// Newest first, and every block exactly once: a cursor that repeated or skipped a row
	// would show up here as a duplicate or a gap.
	for i, command := range seen {
		if want := fmt.Sprintf("command %d", len(created)-1-i); command != want {
			t.Errorf("position %d is %q, want %q (order: %v)", i, command, want, seen)
		}
	}
}

func TestListPagesThroughBlocksSharingAStartTime(t *testing.T) {
	blocks, _, sessionID := newStore(t)

	// A script running commands inside one millisecond gives them all the same start time.
	// Paging on the timestamp alone would repeat this group forever or skip past it.
	same := time.UnixMilli(1_757_592_000_000).UTC()
	for i := range 5 {
		seed(t, blocks, sessionID, fmt.Sprintf("twin %d", i), "output", same, domain.BlockFinished)
	}

	seen := map[string]bool{}
	cursor := domain.Cursor{}
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("paging did not terminate through a group sharing one start time")
		}
		page, err := blocks.List(t.Context(), domain.BlockFilter{
			SessionID: sessionID, Limit: 2, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, block := range page.Items {
			if seen[block.ID] {
				t.Fatalf("block %s came back twice", block.ID)
			}
			seen[block.ID] = true
		}
		if page.NextCursor.IsZero() {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 5 {
		t.Errorf("saw %d of 5 blocks", len(seen))
	}
}

func TestListFilters(t *testing.T) {
	blocks, db, sessionID := newStore(t)

	other := store.NewID(store.PrefixSession)
	if _, err := db.DB().ExecContext(t.Context(), `
		INSERT INTO sessions(id, shell, cwd, cols, rows, state, created_at)
		VALUES (?, '/bin/sh', '/', 80, 24, 'alive', 0)`, other); err != nil {
		t.Fatalf("seeding the second session: %v", err)
	}

	base := time.UnixMilli(1_757_592_000_000).UTC()
	seed(t, blocks, sessionID, "mine finished", "out", base, domain.BlockFinished)
	seed(t, blocks, sessionID, "mine running", "", base.Add(time.Second), domain.BlockRunning)
	seed(t, blocks, other, "theirs", "out", base.Add(2*time.Second), domain.BlockFinished)

	cases := []struct {
		name   string
		filter domain.BlockFilter
		want   []string
	}{
		{"by session", domain.BlockFilter{SessionID: sessionID}, []string{"mine running", "mine finished"}},
		{"by state", domain.BlockFilter{State: domain.BlockRunning}, []string{"mine running"}},
		{"by session and state", domain.BlockFilter{SessionID: sessionID, State: domain.BlockFinished}, []string{"mine finished"}},
		{"unfiltered", domain.BlockFilter{}, []string{"theirs", "mine running", "mine finished"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, err := blocks.List(t.Context(), tc.filter)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			var got []string
			for _, block := range page.Items {
				got = append(got, block.Command)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLastReturnsTheMostRecentClosedBlock(t *testing.T) {
	blocks, _, sessionID := newStore(t)

	base := time.UnixMilli(1_757_592_000_000).UTC()
	seed(t, blocks, sessionID, "older", "out", base, domain.BlockFinished)
	want := seed(t, blocks, sessionID, "newer", "out", base.Add(time.Second), domain.BlockFinished)
	// A block that is still running is deliberately newer than the one we want: it has no
	// exit code and an incomplete transcript, so answering with it would hand the caller a
	// half-written record of a command that has not finished.
	seed(t, blocks, sessionID, "still running", "", base.Add(2*time.Second), domain.BlockRunning)

	got, err := blocks.Last(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("Last: %v", err)
	}
	if got.ID != want.ID {
		t.Errorf("Last returned %q, want %q", got.Command, want.Command)
	}
}

func TestLastReportsNotFoundOnAnEmptyHistory(t *testing.T) {
	blocks, _, sessionID := newStore(t)

	_, err := blocks.Last(t.Context(), sessionID)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("error is %v, want ErrNotFound", err)
	}
}

func TestSearchFindsCommandsAndTranscripts_REQ_BLK_006(t *testing.T) {
	blocks, _, sessionID := newStore(t)

	base := time.UnixMilli(1_757_592_000_000).UTC()
	seed(t, blocks, sessionID, "go test ./...", "FAIL\tinternal/store\t0.2s", base, domain.BlockFinished)
	seed(t, blocks, sessionID, "git status", "nothing to commit", base.Add(time.Second), domain.BlockFinished)

	cases := []struct {
		query string
		want  string
	}{
		// FTS5 indexes both columns, so a search hits a command the user typed and output
		// they only saw go past.
		{"test", "go test ./..."},
		{"commit", "git status"},
		{"FAIL", "go test ./..."},
	}
	for _, tc := range cases {
		page, err := blocks.Search(t.Context(), domain.SearchQuery{Query: tc.query})
		if err != nil {
			t.Fatalf("Search(%q): %v", tc.query, err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("Search(%q) returned %d hits, want 1", tc.query, len(page.Items))
		}
		if got := page.Items[0].Block.Command; got != tc.want {
			t.Errorf("Search(%q) found %q, want %q", tc.query, got, tc.want)
		}
		if page.Items[0].Snippet == "" && tc.query != "test" {
			t.Errorf("Search(%q) returned no snippet", tc.query)
		}
	}
}

func TestSearchRejectsMalformedSyntax(t *testing.T) {
	blocks, _, sessionID := newStore(t)
	seed(t, blocks, sessionID, "echo hi", "hi", time.UnixMilli(0).UTC(), domain.BlockFinished)

	// A typo in a search box is the user's, not the daemon's. Answering INTERNAL_ERROR
	// would put a trace id in the log for every mistyped query. SQLite words its objection
	// differently for each mistake, so all four shapes are checked.
	for _, query := range []string{`"unbalanced`, `(unclosed`, `nosuch:term`, `*`} {
		_, err := blocks.Search(t.Context(), domain.SearchQuery{Query: query})
		if !errors.Is(err, domain.ErrValidation) {
			t.Errorf("Search(%q) gave %v, want a validation error", query, err)
		}
	}
}

func TestRawStopsAtTheLimit(t *testing.T) {
	blocks, _, sessionID := newStore(t)

	block := seed(t, blocks, sessionID, "cat big", "", time.UnixMilli(0).UTC(), domain.BlockRunning)
	chunk := []byte(strings.Repeat("q", 1000))
	for seq := range int64(5) {
		if err := blocks.AppendChunk(t.Context(), block.ID, seq, chunk); err != nil {
			t.Fatalf("AppendChunk: %v", err)
		}
	}

	raw, truncated, err := blocks.Raw(t.Context(), block.ID, 2500)
	if err != nil {
		t.Fatalf("Raw: %v", err)
	}
	if len(raw) != 2500 {
		t.Errorf("Raw returned %d bytes, want the 2500 it was allowed", len(raw))
	}
	if !truncated {
		t.Error("Raw did not report that it stopped early")
	}

	whole, truncated, err := blocks.Raw(t.Context(), block.ID, 1<<20)
	if err != nil {
		t.Fatalf("Raw: %v", err)
	}
	if len(whole) != 5000 || truncated {
		t.Errorf("Raw returned %d bytes (truncated %v), want all 5000", len(whole), truncated)
	}
}

func TestSearchPagesNewestFirst_REQ_BLK_006(t *testing.T) {
	blocks, _, sessionID := newStore(t)

	base := time.UnixMilli(1_757_592_000_000).UTC()
	for i := range 7 {
		seed(t, blocks, sessionID, fmt.Sprintf("go test run%d", i), "FAIL somewhere",
			base.Add(time.Duration(i)*time.Second), domain.BlockFinished)
	}
	// One block that must never appear: the query has to filter, not just page.
	seed(t, blocks, sessionID, "git status", "nothing to commit",
		base.Add(time.Hour), domain.BlockFinished)

	var seen []string
	cursor := domain.Cursor{}
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("paging did not terminate")
		}
		page, err := blocks.Search(t.Context(), domain.SearchQuery{
			Query: "FAIL", Limit: 3, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		for _, hit := range page.Items {
			seen = append(seen, hit.Block.Command)
		}
		if page.NextCursor.IsZero() {
			break
		}
		cursor = page.NextCursor
	}

	if len(seen) != 7 {
		t.Fatalf("paged through %d hits, want 7: %v", len(seen), seen)
	}
	for i, command := range seen {
		if want := fmt.Sprintf("go test run%d", 6-i); command != want {
			t.Errorf("position %d is %q, want %q", i, command, want)
		}
	}
}

func TestSearchRefusesAListCursor(t *testing.T) {
	blocks, _, sessionID := newStore(t)
	seed(t, blocks, sessionID, "echo hi", "hi", time.UnixMilli(0).UTC(), domain.BlockFinished)

	// The two methods are ordered differently, so a cursor from one names a position that
	// does not exist in the other. Refusing is what keeps it from silently paging wrong.
	listCursor := domain.Cursor{StartedAt: time.UnixMilli(0).UTC(), BlockID: "blk_x"}
	_, err := blocks.Search(t.Context(), domain.SearchQuery{Query: "hi", Cursor: listCursor})
	if !errors.Is(err, domain.ErrValidation) {
		t.Errorf("error is %v, want a validation error", err)
	}
}
