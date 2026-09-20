package integration_test

import (
	"os/exec"
	"path/filepath"
	"slices"
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
	"github.com/ecrespo/umbral/internal/store"
)

// testSize is a legal window size used throughout.
var testSize = domain.Size{Cols: 80, Rows: 24}

// harness is a service wired to the real PTY and the real emulator. These tests are
// integration tests on purpose: the requirements they cover are about a real shell in a
// real pseudo-terminal, and a fake PTY would prove none of them.
type harness struct {
	*sessions.Service
	bus   *bus.Bus
	store *store.Store
	shell string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessForShell(t, "bash")
}

// newHarnessForShell builds a harness around one shell, skipping when it is not installed.
//
// A missing shell is a skip rather than a failure because REQ-BLK-005 covers three shells
// and a developer's machine rarely has all three; CI installs them all, so the coverage is
// not lost where it counts.
func newHarnessForShell(t *testing.T, name string) *harness {
	t.Helper()

	shell, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not installed: %v", name, err)
	}

	db, err := store.Open(t.Context(), store.Options{Path: filepath.Join(t.TempDir(), "umbral.db")})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	eventBus := bus.New()
	t.Cleanup(eventBus.Close)

	blocks, err := blockstore.New(db)
	if err != nil {
		t.Fatalf("blockstore.New: %v", err)
	}
	t.Cleanup(func() { _ = blocks.Close() })

	service, err := sessions.New(sessions.Config{
		Store:      db,
		Bus:        eventBus,
		NewPTY:     pty.Open,
		NewEmu:     ghostty.NewEmulator,
		Bootstrap:  shellinteg.Adapter{},
		Blocks:     blocks,
		NewScanner: func() ports.Scanner { return shellinteg.NewScanner() },
		Host:       "test-host",
	})
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	t.Cleanup(service.Shutdown)

	return &harness{Service: service, bus: eventBus, store: db, shell: shell}
}

// create starts a session and makes sure it is closed when the test ends.
func (h *harness) create(t *testing.T, params domain.CreateParams) domain.Session {
	t.Helper()

	if params.Shell == "" {
		params.Shell = h.shell
	}
	if params.Size == (domain.Size{}) {
		params.Size = testSize
	}
	if params.CWD == "" {
		params.CWD = t.TempDir()
	}

	session, err := h.Create(t.Context(), params)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = h.Close(t.Context(), session.ID) })
	return session
}

// waitFor reads events until one satisfies match, or the deadline passes.
func waitFor[T bus.Event](t *testing.T, sub *bus.Subscription, match func(T) bool) T {
	t.Helper()

	deadline := time.After(20 * time.Second)
	for {
		select {
		case event, ok := <-sub.C():
			if !ok {
				t.Fatal("the bus closed before the expected event arrived")
			}
			if typed, is := event.(T); is && match(typed) {
				return typed
			}
		case <-deadline:
			var zero T
			t.Fatalf("timed out waiting for a %T event", zero)
			return zero
		}
	}
}

// TestCreateSession_REQ_TERM_001 covers the whole of the requirement: a real shell starts
// in a real PTY, the session is reported alive, and the call returns inside the 300 ms the
// NFR allows at p95.
func TestCreateSession_REQ_TERM_001(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	cwd := t.TempDir()

	const runs = 20
	durations := make([]time.Duration, 0, runs)
	var last domain.Session
	for range runs {
		start := time.Now()
		session, err := h.Create(t.Context(), domain.CreateParams{
			Shell: h.shell, CWD: cwd, Size: testSize, ShellIntegration: true,
		})
		durations = append(durations, time.Since(start))
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		t.Cleanup(func() { _ = h.Close(t.Context(), session.ID) })
		last = session
	}

	if last.ID == "" || last.ID[:4] != "ses_" {
		t.Errorf("session id = %q, want a ses_ prefixed ULID (Art. 6)", last.ID)
	}
	if last.State != domain.StateAlive {
		t.Errorf("state = %q, want alive", last.State)
	}
	if last.InputOwner != domain.InputOwnerHuman {
		t.Errorf("input owner = %q, want human by default", last.InputOwner)
	}
	if last.Integration != domain.IntegrationPending {
		t.Errorf("integration = %q, want pending until the first OSC arrives", last.Integration)
	}
	if last.Size != testSize {
		t.Errorf("size = %+v, want %+v", last.Size, testSize)
	}

	// p95 over 20 runs is the 19th, counting from one.
	slices.Sort(durations)
	p95 := durations[int(float64(len(durations))*0.95)-1]
	if p95 > 300*time.Millisecond {
		t.Errorf("session.create p95 = %v over %d runs, want under 300ms (REQ-TERM-001)", p95, runs)
	}
	t.Logf("session.create p95 = %v, max = %v over %d runs", p95, durations[len(durations)-1], runs)

	// The row exists, which is what makes the session findable after a restart.
	listed, err := h.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != runs {
		t.Errorf("List returned %d sessions, want %d", len(listed), runs)
	}
}

// TestExitedEmitsCode_REQ_TERM_005 covers the unwanted-behaviour branch: when the shell
// exits, the daemon announces it with the real exit code and keeps the session's history.
func TestExitedEmitsCode_REQ_TERM_005(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	sub := h.bus.Subscribe(ports.KindSessionExited)
	defer sub.Close()

	session := h.create(t, domain.CreateParams{ShellIntegration: false})

	// 42 rather than 0: a zero would also be the value of an uninitialised field.
	if err := h.Input(t.Context(), session.ID, []byte("exit 42\n"), domain.InputOwnerHuman); err != nil {
		t.Fatalf("Input: %v", err)
	}

	event := waitFor(t, sub, func(e ports.SessionExited) bool { return e.SessionID == session.ID })
	if event.ExitCode != 42 {
		t.Errorf("session.exited carried exit code %d, want 42", event.ExitCode)
	}
	if event.ExitedAtMs == 0 {
		t.Error("session.exited carried no exit time")
	}

	// The row must agree, and it must agree already: persist before notifying (DD-007).
	stored, err := h.Get(t.Context(), session.ID)
	if err != nil {
		t.Fatalf("Get after exit: %v", err)
	}
	if stored.State != domain.StateExited {
		t.Errorf("state after exit = %q, want exited", stored.State)
	}
	if stored.ExitCode == nil || *stored.ExitCode != 42 {
		t.Errorf("stored exit code = %v, want 42", stored.ExitCode)
	}

	// REQ-TERM-005 also promises the blocks survive. There is no block writer until
	// T-F0-09, so what is checked here is that the session row itself is kept rather than
	// deleted, which is the same rule applied to the only row that exists yet.
	listed, err := h.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 {
		t.Errorf("List returned %d sessions after the shell exited, want 1: history is kept", len(listed))
	}
}

// TestResizeNotifies_REQ_TERM_007 checks that a resize reaches the PTY, the emulator and
// every subscriber.
func TestResizeNotifies_REQ_TERM_007(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	sub := h.bus.Subscribe(ports.KindSessionResized)
	defer sub.Close()

	session := h.create(t, domain.CreateParams{ShellIntegration: false})
	want := domain.Size{Cols: 120, Rows: 40}

	if err := h.Resize(t.Context(), session.ID, want); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	event := waitFor(t, sub, func(e ports.SessionResized) bool { return e.SessionID == session.ID })
	if event.Size != want {
		t.Errorf("session.resized carried %+v, want %+v", event.Size, want)
	}

	after, err := h.Get(t.Context(), session.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if after.Size != want {
		t.Errorf("session size after resize = %+v, want %+v", after.Size, want)
	}

	// The shell itself must see the new width, or the resize only changed bookkeeping.
	// `tput cols` asks the kernel, which is the thing SIGWINCH updates.
	if err := h.Input(t.Context(), session.ID, []byte("tput cols\n"), domain.InputOwnerHuman); err != nil {
		t.Fatalf("Input: %v", err)
	}
	if !eventuallyContains(t, h, session.ID, "120") {
		t.Error("the shell never reported the new width; the resize did not reach the PTY")
	}
}

// TestInputLockedRejected_REQ_TERM_008 is the whole of the requirement: input from the
// wrong owner is refused *without reaching the PTY*. Checking only the error would pass an
// implementation that wrote the bytes and then complained.
func TestInputLockedRejected_REQ_TERM_008(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	session := h.create(t, domain.CreateParams{ShellIntegration: false})

	if err := h.SetInputOwner(t.Context(), session.ID, domain.InputOwnerAgent); err != nil {
		t.Fatalf("SetInputOwner: %v", err)
	}

	const canary = "umbral-canary-must-not-appear"
	err := h.Input(t.Context(), session.ID, []byte("echo "+canary+"\n"), domain.InputOwnerHuman)
	if err == nil {
		t.Fatal("human input was accepted while the agent held the lock")
	}
	if !isInputLocked(err) {
		t.Errorf("Input error = %v, want domain.ErrInputLocked", err)
	}

	// Give the shell time to echo it if the bytes had leaked through.
	time.Sleep(500 * time.Millisecond)
	screen, err := h.Snapshot(t.Context(), session.ID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if containsBytes(screen.Data, canary) {
		t.Error("the refused input reached the PTY anyway; REQ-TERM-008 requires it never be written")
	}

	// The agent, who holds the lock, is still served.
	if err := h.Input(t.Context(), session.ID, []byte("true\n"), domain.InputOwnerAgent); err != nil {
		t.Errorf("the lock holder was refused: %v", err)
	}

	// Handing the lock back restores human input.
	if err := h.SetInputOwner(t.Context(), session.ID, domain.InputOwnerHuman); err != nil {
		t.Fatalf("SetInputOwner back to human: %v", err)
	}
	if err := h.Input(t.Context(), session.ID, []byte("true\n"), domain.InputOwnerHuman); err != nil {
		t.Errorf("human input still refused after the lock was released: %v", err)
	}
}

// TestSessionSurvivesWithNoSubscribers_REQ_TERM_003 checks the promise that makes durable
// sessions worth building: the PTY and the screen keep going with nobody attached.
func TestSessionSurvivesWithNoSubscribers_REQ_TERM_003(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	session := h.create(t, domain.CreateParams{ShellIntegration: false})

	const marker = "umbral-survived-alone"
	if err := h.Input(t.Context(), session.ID, []byte("echo "+marker+"\n"), domain.InputOwnerHuman); err != nil {
		t.Fatalf("Input: %v", err)
	}
	if !eventuallyContains(t, h, session.ID, marker) {
		t.Fatal("the session produced no output with no subscriber attached")
	}

	after, err := h.Get(t.Context(), session.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if after.State != domain.StateAlive {
		t.Errorf("state = %q, want alive: no client was ever attached, which must not matter", after.State)
	}
}

func TestCreateRejectsAnIllegalSize(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	for _, size := range []domain.Size{
		{Cols: 10, Rows: 24},
		{Cols: 80, Rows: 2},
		{Cols: 2000, Rows: 24},
		{Cols: 80, Rows: 900},
	} {
		if _, err := h.Create(t.Context(), domain.CreateParams{
			Shell: h.shell, CWD: t.TempDir(), Size: size,
		}); err == nil {
			t.Errorf("Create accepted the illegal size %+v", size)
		}
	}
}

func TestCreateRejectsAnUnusableShell(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	if _, err := h.Create(t.Context(), domain.CreateParams{
		Shell: filepath.Join(t.TempDir(), "no-such-shell"), CWD: t.TempDir(), Size: testSize,
	}); err == nil {
		t.Error("Create accepted a shell that does not exist")
	}
	if _, err := h.Create(t.Context(), domain.CreateParams{
		Shell: h.shell, CWD: filepath.Join(t.TempDir(), "no-such-dir"), Size: testSize,
	}); err == nil {
		t.Error("Create accepted a working directory that does not exist")
	}
}

func TestOperationsOnAnUnknownSession(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	const missing = "ses_00000000000000000000000000"

	if err := h.Input(t.Context(), missing, []byte("x"), domain.InputOwnerHuman); err == nil {
		t.Error("Input on an unknown session succeeded")
	}
	if err := h.Resize(t.Context(), missing, testSize); err == nil {
		t.Error("Resize on an unknown session succeeded")
	}
	if err := h.Close(t.Context(), missing); err == nil {
		t.Error("Close on an unknown session succeeded")
	}
	if _, err := h.Get(t.Context(), missing); err == nil {
		t.Error("Get on an unknown session succeeded")
	}
}
