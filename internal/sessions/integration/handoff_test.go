package integration_test

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/sessions"
	"github.com/ecrespo/umbral/internal/sessions/adapters/blockstore"
	"github.com/ecrespo/umbral/internal/sessions/adapters/ghostty"
	"github.com/ecrespo/umbral/internal/sessions/adapters/pty"
	"github.com/ecrespo/umbral/internal/sessions/adapters/shellinteg"
	"github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/sessions/ports"
)

// counting is a command that prints n1 … n40, a line every 50 ms: long enough to be cut in the
// middle, and every line accounted for afterwards.
const counting = "for i in $(seq 1 40); do echo n$i; sleep 0.05; done\n"

// wantCount is the transcript of counting.
func wantCount() string {
	var b strings.Builder
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&b, "n%d\n", i)
	}
	return b.String()
}

// successor is a second sessions service on the first one's database, as the next daemon is.
func successor(t *testing.T, h *harness) (*sessions.Service, *bus.Bus) {
	t.Helper()
	eventBus := bus.New()
	t.Cleanup(eventBus.Close)
	blocks := h.blocks(t)
	service, err := sessions.New(sessions.Config{
		Store: h.store, Bus: eventBus, NewPTY: pty.Open, NewEmu: ghostty.NewEmulator,
		Bootstrap: shellinteg.Adapter{Dir: t.TempDir()}, Blocks: blocks,
		NewScanner:    func() ports.Scanner { return shellinteg.NewScanner() },
		AdoptPTY:      pty.Adopt,
		ResumeScanner: func(state []byte) (ports.Scanner, error) { return shellinteg.ResumeScanner(state) },
		Host:          "test-host",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Shutdown)
	return service, eventBus
}

// dup is another descriptor for each master, as SCM_RIGHTS delivers them.
func dup(t *testing.T, files []*os.File) []*os.File {
	t.Helper()
	out := make([]*os.File, len(files))
	for i, f := range files {
		fd, err := syscall.Dup(int(f.Fd()))
		if err != nil {
			t.Fatal(err)
		}
		out[i] = os.NewFile(uintptr(fd), f.Name())
	}
	return out
}

// TestAnAdoptedSessionContinuesItsBlock_REQ_HND_001: a session frozen in the middle of a
// command and adopted by a second service goes on as if one daemon had run it all. The block
// opened before the handoff closes after it with the command's exit code, its transcript holds
// every line — those the first service read and those the second did — the second service's
// first output follows the first's last sequence number, its screen has what was drawn before,
// and the shell's end is recorded with no exit code, which went to the first.
func TestAnAdoptedSessionContinuesItsBlock_REQ_HND_001(t *testing.T) {
	h := newHarness(t)
	session := h.create(t, domain.CreateParams{ShellIntegration: true})
	waitIntegrated(t, h, session.ID)

	if err := h.Input(t.Context(), session.ID, []byte(counting), domain.InputOwnerHuman); err != nil {
		t.Fatal(err)
	}
	if !waitForScreen(t, h, session.ID, "n5") {
		t.Fatal("the command never printed")
	}

	handed, files, err := h.Freeze(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(handed) != 1 || handed[0].Session.ID != session.ID || handed[0].Recorder == nil || handed[0].Recorder.Current == nil {
		t.Fatalf("the freeze handed %+v, want the session with its open block", handed)
	}
	blockID := handed[0].Recorder.Current.ID
	// The first service's own count, read while it is frozen.
	before, err := h.Snapshot(t.Context(), session.ID)
	if err != nil {
		t.Fatal(err)
	}

	next, nextBus := successor(t, h)
	sub := nextBus.Subscribe(ports.KindSessionOutput, ports.KindBlockClosed, ports.KindSessionExited)
	if err := next.Adopt(t.Context(), handed, dup(t, files)); err != nil {
		t.Fatal(err)
	}
	h.ReleaseHanded()
	next.StartAdopted()

	first := waitFor(t, sub, func(e ports.SessionOutput) bool { return e.SessionID == session.ID })
	if first.Seq != before.Seq+1 {
		t.Errorf("the successor's first output is seq %d, want %d", first.Seq, before.Seq+1)
	}
	closed := waitFor(t, sub, func(e ports.BlockClosed) bool { return e.Block.ID == blockID })
	if closed.Block.ExitCode == nil || *closed.Block.ExitCode != 0 {
		t.Errorf("the block closed with %v, want 0", closed.Block.ExitCode)
	}
	plain, err := h.blocks(t).Plain(t.Context(), blockID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain, wantCount()) {
		t.Errorf("the transcript across the handoff is %q, want every line n1 … n40", plain)
	}
	// The raw output too: the chunk the first service had not stored yet is stored, and the
	// second's chunks follow it.
	raw, _, err := h.blocks(t).Raw(t.Context(), blockID, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var lines strings.Builder
	for _, line := range strings.Split(string(raw), "\r\n") {
		if strings.HasPrefix(line, "n") {
			lines.WriteString(line + "\n")
		}
	}
	if lines.String() != wantCount() {
		t.Errorf("the stored output across the handoff is %q, want every line n1 … n40", lines.String())
	}
	snap, err := next.Snapshot(t.Context(), session.ID)
	if err != nil || !strings.Contains(string(snap.Data), "n1\r\n") {
		t.Errorf("the successor's screen lost what was drawn before: %v", err)
	}

	if err := next.Close(t.Context(), session.ID); err != nil {
		t.Fatal(err)
	}
	exited := waitFor(t, sub, func(e ports.SessionExited) bool { return e.SessionID == session.ID })
	if exited.ExitCode != nil {
		t.Errorf("the adopted session exited with code %d; the code went to the first service", *exited.ExitCode)
	}
	var state string
	var code any
	if err := h.store.DB().QueryRowContext(t.Context(),
		"SELECT state, exit_code FROM sessions WHERE id = ?", session.ID).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != "exited" || code != nil {
		t.Errorf("the row says %s with code %v, want exited with NULL", state, code)
	}
}

// TestAThawedSessionLosesNothing_REQ_HND_002: a session frozen and then thawed, as after a
// handoff that failed, records the whole command and publishes its output with no gap in the
// sequence numbers.
func TestAThawedSessionLosesNothing_REQ_HND_002(t *testing.T) {
	h := newHarness(t)
	session := h.create(t, domain.CreateParams{ShellIntegration: true})
	waitIntegrated(t, h, session.ID)
	sub := h.bus.Subscribe(ports.KindSessionOutput, ports.KindBlockClosed)

	if err := h.Input(t.Context(), session.ID, []byte(counting), domain.InputOwnerHuman); err != nil {
		t.Fatal(err)
	}
	if !waitForScreen(t, h, session.ID, "n5") {
		t.Fatal("the command never printed")
	}
	handed, _, err := h.Freeze(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	blockID := handed[0].Recorder.Current.ID
	// Frozen across several lines: nothing is read, published or recorded meanwhile.
	time.Sleep(400 * time.Millisecond)
	h.Thaw()

	var seqs []uint64
	deadline := time.After(20 * time.Second)
	for {
		select {
		case event := <-sub.C():
			switch e := event.(type) {
			case ports.SessionOutput:
				if e.SessionID == session.ID {
					seqs = append(seqs, e.Seq)
				}
			case ports.BlockClosed:
				if e.Block.ID != blockID {
					continue
				}
				for i := 1; i < len(seqs); i++ {
					if seqs[i] != seqs[i-1]+1 {
						t.Fatalf("the output's sequence jumped from %d to %d", seqs[i-1], seqs[i])
					}
				}
				plain, err := h.blocks(t).Plain(t.Context(), blockID)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(plain, wantCount()) {
					t.Fatalf("the transcript after a thaw is %q, want every line", plain)
				}
				return
			}
		case <-deadline:
			t.Fatal("the block never closed after the thaw")
		}
	}
}

// TestAFreezeWaitsForNoAgentCommand_REQ_HND_002: a freeze is refused while the agent waits on
// a command in a session, and leaves the session running.
func TestAFreezeWaitsForNoAgentCommand_REQ_HND_002(t *testing.T) {
	h := newHarness(t)
	cwd := t.TempDir()
	id := thread(t, h, cwd)
	ran := make(chan error, 1)
	go func() {
		_, err := h.RunForThread(t.Context(), id, cwd, "sleep 2")
		ran <- err
	}()

	deadline := time.Now().Add(15 * time.Second)
	for {
		_, _, err := h.Freeze(t.Context())
		if errors.Is(err, sessions.ErrBusy) {
			break
		}
		if err == nil {
			h.Thaw()
		}
		if time.Now().After(deadline) {
			select {
			case rerr := <-ran:
				t.Fatalf("the freeze was never refused while the command ran: %v (the command: %v)", err, rerr)
			default:
			}
			t.Fatalf("the freeze was never refused while the command ran: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := <-ran; err != nil {
		t.Fatalf("the agent's command did not finish after the refused freeze: %v", err)
	}
}

// waitIntegrated waits for the shell's first prompt marker, after which a command is a block.
func waitIntegrated(t *testing.T, h *harness, id string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		s, err := h.Get(t.Context(), id)
		if err == nil && s.Integration == domain.IntegrationOSC133 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the shell never reported its integration")
}

// blocks is a block store over the harness's database.
func (h *harness) blocks(t *testing.T) *blockstore.Store {
	t.Helper()
	b, err := blockstore.New(h.store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}
