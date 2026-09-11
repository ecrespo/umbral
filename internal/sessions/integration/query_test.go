package integration_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ecrespo/umbral/internal/sessions"
	"github.com/ecrespo/umbral/internal/sessions/adapters/blockstore"
	"github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/sessions/ports"
)

// reader builds the query service over the harness's database.
func reader(t *testing.T, h *harness) *sessions.Reader {
	t.Helper()

	blocks, err := blockstore.New(h.store)
	if err != nil {
		t.Fatalf("blockstore.New: %v", err)
	}
	t.Cleanup(func() { _ = blocks.Close() })

	r, err := sessions.NewReader(sessions.ReaderConfig{Blocks: blocks})
	if err != nil {
		t.Fatalf("sessions.NewReader: %v", err)
	}
	return r
}

// runAndWait types a command and returns the block it produced, once it has closed.
func runAndWait(t *testing.T, h *harness, sessionID, command, match string) domain.Block {
	t.Helper()

	closed := h.bus.SubscribeBuffered(64, ports.KindBlockClosed)
	defer closed.Close()

	typeCommand(t, h, sessionID, command)
	return waitFor(t, closed, func(e ports.BlockClosed) bool {
		return strings.Contains(e.Block.Command, match)
	}).Block
}

// TestBlockGetLast_REQ_CLI_002 is the resolution half of REQ-CLI-002, against a real shell:
// what `umb block last` is asked for is the last closed block of its own session, with the
// transcript that makes the answer useful.
//
// The wire shape the requirement also names is checked in `internal/api`, which is the only
// package that may see it. The architecture keeps the two apart on purpose, so the
// requirement is covered by two tests rather than one.
func TestBlockGetLast_REQ_CLI_002(t *testing.T) {
	h := newHarness(t)
	r := reader(t, h)

	session := h.create(t, domain.CreateParams{ShellIntegration: true})
	waitUntilArmed(t, h, session.ID)

	runAndWait(t, h, session.ID, `sh -c 'printf "first output\n"'`, "first output")
	second := runAndWait(t, h, session.ID,
		`sh -c 'printf "second output\n"; exit 4'`, "second output")

	block, output, err := r.Get(t.Context(), domain.BlockLast, session.ID, domain.IncludePlain)
	if err != nil {
		t.Fatalf("Get last: %v", err)
	}

	switch {
	case block.ID != second.ID:
		t.Errorf("last is %q, want the most recent closed block %q", block.Command, second.Command)
	case block.State != domain.BlockFinished:
		t.Errorf("last is %q, want finished", block.State)
	case block.ExitCode == nil || *block.ExitCode != 4:
		t.Errorf("exit code is %v, want 4", block.ExitCode)
	case !strings.Contains(output.Plain, "second output"):
		t.Errorf("the transcript is %q", output.Plain)
	case strings.ContainsRune(output.Plain, 0x1B):
		t.Errorf("the transcript carries an escape sequence: %q", output.Plain)
	}
}

func TestBlockGetLastIsScopedToItsSession_REQ_CLI_002(t *testing.T) {
	h := newHarness(t)
	r := reader(t, h)

	first := h.create(t, domain.CreateParams{ShellIntegration: true})
	waitUntilArmed(t, h, first.ID)
	mine := runAndWait(t, h, first.ID, `sh -c 'printf "from the first session\n"'`, "first session")

	second := h.create(t, domain.CreateParams{ShellIntegration: true})
	waitUntilArmed(t, h, second.ID)
	theirs := runAndWait(t, h, second.ID, `sh -c 'printf "from the second session\n"'`, "second session")

	// Each terminal's `umb block last` answers about that terminal, not about whichever
	// window happened to run something most recently.
	block, _, err := r.Get(t.Context(), domain.BlockLast, first.ID, domain.IncludeNone)
	if err != nil {
		t.Fatalf("Get last for the first session: %v", err)
	}
	if block.ID != mine.ID {
		t.Errorf("the first session's last block is %q, want %q", block.Command, mine.Command)
	}

	// With no session named, the answer is the last thing that ran anywhere.
	block, _, err = r.Get(t.Context(), domain.BlockLast, "", domain.IncludeNone)
	if err != nil {
		t.Fatalf("Get last with no session: %v", err)
	}
	if block.ID != theirs.ID {
		t.Errorf("the global last block is %q, want %q", block.Command, theirs.Command)
	}
}

func TestBlockGetRefusesABlockFromAnotherSession(t *testing.T) {
	h := newHarness(t)
	r := reader(t, h)

	first := h.create(t, domain.CreateParams{ShellIntegration: true})
	waitUntilArmed(t, h, first.ID)
	mine := runAndWait(t, h, first.ID, `sh -c 'printf "secret\n"'`, "secret")

	second := h.create(t, domain.CreateParams{ShellIntegration: true})

	// Naming another session's block while claiming this one must not read across: the
	// session parameter narrows the query, it is not a hint the daemon may ignore.
	_, _, err := r.Get(t.Context(), mine.ID, second.ID, domain.IncludePlain)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("error is %v, want ErrNotFound", err)
	}
}

func TestBlockSearchFindsARealCommand_REQ_BLK_006(t *testing.T) {
	h := newHarness(t)
	r := reader(t, h)

	session := h.create(t, domain.CreateParams{ShellIntegration: true})
	waitUntilArmed(t, h, session.ID)
	want := runAndWait(t, h, session.ID,
		`sh -c 'printf "unmistakable-needle in the output\n"'`, "unmistakable-needle")

	// Both columns FTS5 indexes, through the whole pipeline: a real shell wrote the
	// transcript, the recorder stripped its escapes and the triggers indexed it.
	for _, query := range []string{"needle", `"unmistakable-needle"`} {
		page, err := r.Search(t.Context(), domain.SearchQuery{Query: query, SessionID: session.ID})
		if err != nil {
			t.Fatalf("Search(%q): %v", query, err)
		}
		if len(page.Items) == 0 {
			t.Fatalf("Search(%q) found nothing", query)
		}
		if got := page.Items[0].Block.ID; got != want.ID {
			t.Errorf("Search(%q) found %q, want the block that printed it", query, got)
		}
		if page.Items[0].Snippet == "" {
			t.Errorf("Search(%q) returned no snippet", query)
		}
	}
}

func TestBlockListReturnsARealSessionsHistory(t *testing.T) {
	h := newHarness(t)
	r := reader(t, h)

	session := h.create(t, domain.CreateParams{ShellIntegration: true})
	waitUntilArmed(t, h, session.ID)
	runAndWait(t, h, session.ID, `sh -c 'printf "alpha\n"'`, "alpha")
	runAndWait(t, h, session.ID, `sh -c 'printf "beta\n"'`, "beta")

	page, err := r.List(t.Context(), domain.BlockFilter{
		SessionID: session.ID, State: domain.BlockFinished,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Items) < 2 {
		t.Fatalf("the session's history has %d blocks, want at least the two commands", len(page.Items))
	}
	// Newest first: the history pane shows what just happened at the top.
	if !strings.Contains(page.Items[0].Command, "beta") {
		t.Errorf("the newest block is %q, want the beta command", page.Items[0].Command)
	}
}
