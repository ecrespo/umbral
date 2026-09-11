package integration_test

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/sessions/adapters/blockstore"
	"github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/sessions/ports"
)

// storedBlock is one row of the blocks table, read back to prove the daemon persisted what
// it published. A notification a client believes and a row the agent later reads have to
// agree, and only reading the row proves they do.
type storedBlock struct {
	command    string
	cwd        string
	host       string
	state      string
	exitCode   sql.NullInt64
	durationMs sql.NullInt64
	plain      sql.NullString
	bytes      int64
	truncated  int
}

func readBlock(t *testing.T, h *harness, id string) storedBlock {
	t.Helper()

	var got storedBlock
	err := h.store.DB().QueryRowContext(t.Context(), `
		SELECT command, cwd, host, state, exit_code, duration_ms, output_plain,
		       output_bytes, output_truncated
		  FROM blocks WHERE id = ?`, id).
		Scan(&got.command, &got.cwd, &got.host, &got.state, &got.exitCode,
			&got.durationMs, &got.plain, &got.bytes, &got.truncated)
	if err != nil {
		t.Fatalf("reading block %s: %v", id, err)
	}
	return got
}

// rawOutput reassembles a block's stored chunks (Data Model §2.3).
func rawOutput(t *testing.T, h *harness, id string) string {
	t.Helper()

	rows, err := h.store.DB().QueryContext(t.Context(),
		"SELECT data_zstd FROM block_chunks WHERE block_id = ? ORDER BY seq", id)
	if err != nil {
		t.Fatalf("reading the chunks of %s: %v", id, err)
	}
	defer func() { _ = rows.Close() }()

	var out strings.Builder
	for rows.Next() {
		var compressed []byte
		if err := rows.Scan(&compressed); err != nil {
			t.Fatalf("scanning a chunk: %v", err)
		}
		raw, err := blockstore.Decompress(compressed)
		if err != nil {
			t.Fatalf("decompressing a chunk: %v", err)
		}
		out.Write(raw)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading the chunks of %s: %v", id, err)
	}
	return out.String()
}

// waitUntilArmed blocks until the shell is at a prompt with its integration hooks armed.
//
// Waiting for a prompt glyph does not work: the bootstrap loads the user's own bashrc, so
// on a machine with Starship or powerlevel10k the prompt has no $ in it at all. Waiting for
// the integration event is not enough either, since the first OSC 133;A is emitted while
// the script loads, before the DEBUG trap that opens blocks is installed. The only reliable
// signal is a block: when a probe command produces one, the hooks are running.
func waitUntilArmed(t *testing.T, h *harness, sessionID string) {
	t.Helper()

	closed := h.bus.SubscribeBuffered(256, ports.KindBlockClosed)
	defer closed.Close()

	// The probe is retried rather than sent once, because a shell that is not ready yet
	// discards what it is given: fish spends its first seconds asking the terminal
	// questions and treats anything else arriving in that window as an answer. Two seconds
	// between attempts keeps the shell from receiving a pile of queued lines it will then
	// take longer than this test to work through.
	const probe = "printf 'umbral-probe\\n'"
	typeCommand(t, h, sessionID, probe)

	retry := time.NewTicker(2 * time.Second)
	defer retry.Stop()
	deadline := time.After(30 * time.Second)

	for {
		select {
		case event := <-closed.C():
			if block, ok := event.(ports.BlockClosed); ok &&
				strings.Contains(block.Block.Command, "umbral-probe") {
				return
			}
		case <-retry.C:
			typeCommand(t, h, sessionID, probe)
		case <-deadline:
			t.Fatal("the shell never ran a command through the integration hooks")
		}
	}
}

// typeCommand sends a command line and the Enter that runs it.
func typeCommand(t *testing.T, h *harness, sessionID, command string) {
	t.Helper()

	if err := h.Input(t.Context(), sessionID, []byte(command+"\n"), domain.InputOwnerHuman); err != nil {
		t.Fatalf("Input: %v", err)
	}
}

func TestRealShellOpensAndClosesABlock_REQ_BLK_001_REQ_BLK_002(t *testing.T) {
	h := newHarness(t)
	started := h.bus.SubscribeBuffered(64, ports.KindBlockStarted)
	defer started.Close()
	closed := h.bus.SubscribeBuffered(64, ports.KindBlockClosed)
	defer closed.Close()

	session := h.create(t, domain.CreateParams{ShellIntegration: true})
	waitUntilArmed(t, h, session.ID)
	// The exit code has to come from a command that is not the shell itself: a bare
	// `exit 3` would end the shell before its prompt hook could emit OSC 133;D, and the
	// block would be abandoned rather than finished.
	typeCommand(t, h, session.ID, `sh -c 'printf "hello block\n"; exit 3'`)

	start := waitFor(t, started, func(e ports.BlockStarted) bool {
		return strings.Contains(e.Block.Command, "hello block")
	})
	// REQ-BLK-001: the command, the cwd and the start time come from the OSC sequences.
	switch {
	case start.Block.State != domain.BlockRunning:
		t.Errorf("the new block is %q, want running", start.Block.State)
	case start.Block.Origin != domain.OriginUser:
		t.Errorf("origin is %q, want user", start.Block.Origin)
	case start.Block.CWD == "":
		t.Error("the block has no cwd: OSC 7 never arrived")
	case start.Block.StartedAt.IsZero():
		t.Error("the block has no start time")
	}

	end := waitFor(t, closed, func(e ports.BlockClosed) bool {
		return e.Block.ID == start.Block.ID
	})
	// REQ-BLK-002: the exit code and the duration come from OSC 133;D.
	if end.Block.State != domain.BlockFinished {
		t.Errorf("the closed block is %q, want finished", end.Block.State)
	}
	if end.Block.ExitCode == nil {
		t.Fatal("the block has no exit code")
	}
	if *end.Block.ExitCode != 3 {
		t.Fatalf("exit code is %d, want the 3 the command exited with", *end.Block.ExitCode)
	}

	row := readBlock(t, h, start.Block.ID)
	switch {
	case row.state != "finished":
		t.Errorf("the row says %q, want finished", row.state)
	case !row.exitCode.Valid || row.exitCode.Int64 != 3:
		t.Errorf("the row's exit code is %v, want 3", row.exitCode)
	case !row.durationMs.Valid:
		t.Error("the row has no duration")
	case row.host != "test-host":
		t.Errorf("the row's host is %q", row.host)
	case !strings.Contains(row.plain.String, "hello block"):
		t.Errorf("the transcript is %q, want it to contain the output", row.plain.String)
	case row.bytes == 0:
		t.Error("the row counted no output")
	}

	if raw := rawOutput(t, h, start.Block.ID); !strings.Contains(raw, "hello block") {
		t.Errorf("the stored chunks are %q, want the raw output", raw)
	}
}

func TestShellIntegrationIsDetected_REQ_BLK_003(t *testing.T) {
	h := newHarness(t)
	sub := h.bus.SubscribeBuffered(16, ports.KindSessionIntegration)
	defer sub.Close()

	session := h.create(t, domain.CreateParams{ShellIntegration: true})

	event := waitFor(t, sub, func(e ports.SessionIntegration) bool {
		return e.SessionID == session.ID
	})
	if event.Integration != domain.IntegrationOSC133 {
		t.Fatalf("integration is %q, want osc133", event.Integration)
	}
	if got, err := h.Get(t.Context(), session.ID); err != nil || got.Integration != domain.IntegrationOSC133 {
		t.Errorf("the session reports %q (err %v), want osc133", got.Integration, err)
	}

	// The five-second timer still fires for this session, and must find nothing to do. A
	// session that has already spoken is not silent, and downgrading it would leave a
	// terminal with working blocks advertising that it has none.
	time.Sleep(domain.IntegrationWindow + time.Second)
	if got, _ := h.Get(t.Context(), session.ID); got.Integration != domain.IntegrationOSC133 {
		t.Errorf("the window downgraded a session that had already announced itself: %q",
			got.Integration)
	}
}

func TestIntegrationNoneAfter5s_REQ_BLK_003(t *testing.T) {
	h := newHarness(t)
	sub := h.bus.SubscribeBuffered(16, ports.KindSessionIntegration)
	defer sub.Close()

	// A session started with the bootstrap suppressed is the degraded mode DD-002
	// describes: output still flows, but no shell ever announces a prompt.
	session := h.create(t, domain.CreateParams{ShellIntegration: false})

	deadline := time.Now().Add(domain.IntegrationWindow + 10*time.Second)
	var event ports.SessionIntegration
	for time.Now().Before(deadline) {
		event = waitFor(t, sub, func(e ports.SessionIntegration) bool {
			return e.SessionID == session.ID
		})
		break
	}
	if event.Integration != domain.IntegrationNone {
		t.Fatalf("integration is %q, want none", event.Integration)
	}

	// Output still reaches the client; only the blocks are missing.
	typeCommand(t, h, session.ID, "printf 'still visible\\n'")
	if !eventuallyContains(t, h, session.ID, "still visible") {
		t.Error("output stopped flowing in a session with no integration")
	}

	var blocks int
	if err := h.store.DB().QueryRowContext(t.Context(),
		"SELECT count(*) FROM blocks WHERE session_id = ?", session.ID).Scan(&blocks); err != nil {
		t.Fatalf("counting blocks: %v", err)
	}
	if blocks != 0 {
		t.Errorf("recorded %d blocks in a session with no integration, want none", blocks)
	}
}

func TestBlockIsAbandonedWhenTheSessionDies(t *testing.T) {
	h := newHarness(t)
	started := h.bus.SubscribeBuffered(64, ports.KindBlockStarted)
	defer started.Close()
	closed := h.bus.SubscribeBuffered(64, ports.KindBlockClosed)
	defer closed.Close()

	session := h.create(t, domain.CreateParams{ShellIntegration: true})
	waitUntilArmed(t, h, session.ID)

	// A command that kills its own shell: the block opens and never reports an exit code.
	typeCommand(t, h, session.ID, "kill -9 $$")

	start := waitFor(t, started, func(e ports.BlockStarted) bool {
		return strings.Contains(e.Block.Command, "kill -9")
	})
	end := waitFor(t, closed, func(e ports.BlockClosed) bool {
		return e.Block.ID == start.Block.ID
	})

	if end.Block.State != domain.BlockAbandoned {
		t.Errorf("the block is %q, want abandoned", end.Block.State)
	}
	if end.Block.ExitCode != nil {
		t.Errorf("exit code is %v, want nil: the command never reported one", end.Block.ExitCode)
	}
	if row := readBlock(t, h, start.Block.ID); row.state != "abandoned" {
		t.Errorf("the row says %q, want abandoned", row.state)
	}
}

// TestBlocksInEveryShell_REQ_BLK_005 runs the same command cycle under each supported
// shell. The three bootstrap scripts express the same protocol in three different hook
// systems, and only running them proves they agree: the bash and zsh scripts both reported
// exit code 0 for every command until this test was written, because a prompt framework's
// own hook had already overwritten $? by the time they read it.
func TestBlocksInEveryShell_REQ_BLK_005(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			h := newHarnessForShell(t, shell)
			started := h.bus.SubscribeBuffered(64, ports.KindBlockStarted)
			defer started.Close()
			closed := h.bus.SubscribeBuffered(64, ports.KindBlockClosed)
			defer closed.Close()

			session := h.create(t, domain.CreateParams{ShellIntegration: true})
			waitUntilArmed(t, h, session.ID)

			// The work is done by an external `sh` rather than by the shell under test.
			// A subshell would be the obvious way to produce an exit code without ending
			// the session, but `(...)` is a subshell in bash and zsh and command
			// substitution in fish, where it ends the session instead.
			typeCommand(t, h, session.ID, `sh -c 'printf "marker-text\n"; exit 3'`)

			start := waitFor(t, started, func(e ports.BlockStarted) bool {
				return strings.Contains(e.Block.Command, "marker-text")
			})
			end := waitFor(t, closed, func(e ports.BlockClosed) bool {
				return e.Block.ID == start.Block.ID
			})

			if end.Block.ExitCode == nil {
				t.Fatalf("%s: the block has no exit code", shell)
			}
			if *end.Block.ExitCode != 3 {
				t.Errorf("%s: exit code is %d, want 3", shell, *end.Block.ExitCode)
			}
			if end.Block.CWD == "" {
				t.Errorf("%s: the block has no cwd", shell)
			}
			if row := readBlock(t, h, start.Block.ID); !strings.Contains(row.plain.String, "marker-text") {
				t.Errorf("%s: the transcript is %q", shell, row.plain.String)
			}
		})
	}
}

// TestAltScreenExcludedFromStoredOutput_REQ_BLK_004 drives the alternate screen from a
// real shell.
//
// The sequences are printed directly rather than by running vim, so the test needs nothing
// installed beyond the shell and says exactly which bytes it is testing.
func TestAltScreenExcludedFromStoredOutput_REQ_BLK_004(t *testing.T) {
	h := newHarness(t)
	started := h.bus.SubscribeBuffered(64, ports.KindBlockStarted)
	defer started.Close()
	updated := h.bus.SubscribeBuffered(64, ports.KindBlockUpdated)
	defer updated.Close()
	closed := h.bus.SubscribeBuffered(64, ports.KindBlockClosed)
	defer closed.Close()

	session := h.create(t, domain.CreateParams{ShellIntegration: true})
	waitUntilArmed(t, h, session.ID)

	typeCommand(t, h, session.ID,
		`sh -c 'printf "primary-before\n\033[?1049hALT-ONLY\033[?1049lprimary-after\n"'`)

	start := waitFor(t, started, func(e ports.BlockStarted) bool {
		return strings.Contains(e.Block.Command, "primary-before")
	})
	state := waitFor(t, updated, func(e ports.BlockUpdated) bool {
		return e.BlockID == start.Block.ID
	})
	if state.State != domain.BlockInteractive {
		t.Errorf("the block was updated to %q, want interactive", state.State)
	}
	waitFor(t, closed, func(e ports.BlockClosed) bool { return e.Block.ID == start.Block.ID })

	row := readBlock(t, h, start.Block.ID)
	if strings.Contains(row.plain.String, "ALT-ONLY") {
		t.Errorf("the transcript kept alternate-screen content: %q", row.plain.String)
	}
	if !strings.Contains(row.plain.String, "primary-before") ||
		!strings.Contains(row.plain.String, "primary-after") {
		t.Errorf("the transcript lost primary-screen output: %q", row.plain.String)
	}
	if raw := rawOutput(t, h, start.Block.ID); strings.Contains(raw, "ALT-ONLY") {
		t.Errorf("the stored chunks kept alternate-screen content: %q", raw)
	}
	// The block goes back to `running` when the program leaves the alternate screen, and
	// ends `finished` like any other.
	if row.state != "finished" {
		t.Errorf("the row says %q, want finished", row.state)
	}
}

// TestLateIntegrationPromotesTheSession_REQ_BLK_003 covers the case the five-second window
// gets wrong.
//
// A shell whose first marker arrives after the window has integration; the window is a
// heuristic about silence, not a verdict on the shell. Leaving the session advertising
// `none` while its blocks are being recorded would make the two answers disagree.
func TestLateIntegrationPromotesTheSession_REQ_BLK_003(t *testing.T) {
	h := newHarness(t)
	sub := h.bus.SubscribeBuffered(16, ports.KindSessionIntegration)
	defer sub.Close()

	// No bootstrap, so nothing announces a prompt and the window closes on silence.
	session := h.create(t, domain.CreateParams{ShellIntegration: false})

	first := waitFor(t, sub, func(e ports.SessionIntegration) bool {
		return e.SessionID == session.ID
	})
	if first.Integration != domain.IntegrationNone {
		t.Fatalf("integration is %q, want none after the window", first.Integration)
	}

	// A marker arrives late, printed by hand rather than by a bootstrap.
	typeCommand(t, h, session.ID, `printf '\033]133;A\a'`)

	second := waitFor(t, sub, func(e ports.SessionIntegration) bool {
		return e.SessionID == session.ID && e.Integration == domain.IntegrationOSC133
	})
	if second.Integration != domain.IntegrationOSC133 {
		t.Fatalf("integration is %q, want osc133 once a marker arrived", second.Integration)
	}
	if got, err := h.Get(t.Context(), session.ID); err != nil || got.Integration != domain.IntegrationOSC133 {
		t.Errorf("the session reports %q (err %v), want osc133", got.Integration, err)
	}
}
