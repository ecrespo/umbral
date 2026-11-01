package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/api"
	"github.com/ecrespo/umbral/internal/client"
	"github.com/ecrespo/umbral/internal/handoff"
)

// testHandoffFailEnv is the variable the test build of umbrald reads the step to fail at from
// (delta `2026-10-pty-handoff`, decision 8). buildDaemon links its name in; a release build
// has none.
const testHandoffFailEnv = "UMBRAL_TEST_HANDOFF_FAIL_AT"

// daemonProcess is one umbrald the test owns: it is stopped and reaped at the end whatever
// happened, and its exit can be waited for.
type daemonProcess struct {
	cmd  *exec.Cmd
	logs *lockedBuffer
	done chan struct{}
	err  error
}

func runDaemon(t *testing.T, bin string, args []string, env ...string) *daemonProcess {
	t.Helper()
	d := &daemonProcess{logs: &lockedBuffer{}, done: make(chan struct{})}
	// Not tied to the test's context: the cleanup below interrupts the daemon and waits, which
	// a killed process would skip.
	d.cmd = exec.CommandContext(context.WithoutCancel(t.Context()), bin, args...)
	d.cmd.Env = append(os.Environ(), env...)
	d.cmd.Stdout, d.cmd.Stderr = d.logs, d.logs
	if err := d.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		d.err = d.cmd.Wait()
		close(d.done)
	}()
	t.Cleanup(func() {
		_ = d.cmd.Process.Signal(os.Interrupt)
		select {
		case <-d.done:
		case <-time.After(15 * time.Second):
			_ = d.cmd.Process.Kill()
			<-d.done
		}
	})
	return d
}

// exit waits for the process to end and reports its status, failing after limit.
func (d *daemonProcess) exit(t *testing.T, limit time.Duration) int {
	t.Helper()
	select {
	case <-d.done:
	case <-time.After(limit):
		t.Fatalf("umbrald did not exit within %v; its log:\n%s", limit, d.logs.String())
	}
	var exitErr *exec.ExitError
	if errors.As(d.err, &exitErr) {
		return exitErr.ExitCode()
	}
	if d.err != nil {
		t.Fatal(d.err)
	}
	return 0
}

func (d *daemonProcess) running() bool {
	select {
	case <-d.done:
		return false
	default:
		return true
	}
}

// handoffDaemon starts the first daemon of a handoff test in an isolated runtime directory and
// waits for its socket.
func handoffDaemon(t *testing.T) (bin, socket string, old *daemonProcess) {
	t.Helper()
	bin = buildDaemon(t)
	_, daemonDir := isolatedRuntime(t)
	socket = filepath.Join(daemonDir, "umbral.sock")
	old = runDaemon(t, bin, nil)
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(daemonDir, "handoff.sock")); err == nil {
			return bin, socket, old
		}
		if time.Now().After(deadline) {
			t.Fatalf("umbrald did not open its sockets; its log:\n%s", old.logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// interactive connects as a TUI, the kind that may type into a session.
func interactive(ctx context.Context, t *testing.T, socket string) *client.Stream {
	t.Helper()
	c, err := client.Connect(ctx, client.Options{SocketPath: socket, NoAutostart: true, ClientKind: client.ClientKindTUI})
	if err != nil {
		t.Fatal(err)
	}
	s := client.NewStream(c)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// paneRunning opens a workspace and types command into its pane's shell.
func paneRunning(ctx context.Context, t *testing.T, s *client.Stream, command string) string {
	t.Helper()
	var ws struct {
		RootPane struct {
			SessionID string `json:"session_id"`
		} `json:"root_pane"`
	}
	if err := s.Call(ctx, "workspace.create", map[string]any{"cwd": t.TempDir()}, &ws); err != nil {
		t.Fatal(err)
	}
	id := ws.RootPane.SessionID
	if err := s.Call(ctx, "session.input", map[string]any{
		"session_id": id, "data_b64": base64.StdEncoding.EncodeToString([]byte(command)),
	}, nil); err != nil {
		t.Fatal(err)
	}
	return id
}

// counter is a command printing c1 … c<n>, one line every 50 ms, and its transcript.
func counter(n int) (command, transcript string) {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "c%d\n", i)
	}
	return fmt.Sprintf("for i in $(seq 1 %d); do echo c$i; sleep 0.05; done\n", n), b.String()
}

// counted is the counter's own lines in a block's output. The shell may print something of
// its own between them — bash on macOS reports a `setpgid` race of its job control as a line
// — which says nothing about whether a line was lost.
func counted(output string) string {
	var b strings.Builder
	for _, line := range strings.Split(output, "\n") {
		if len(line) > 1 && line[0] == 'c' && strings.Trim(line[1:], "0123456789") == "" {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

// sessionRow is a session as `session.list` reports it.
type sessionRow struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	ExitCode *int   `json:"exit_code"`
}

// sessionNamed reads one session from `session.list`.
func sessionNamed(ctx context.Context, t *testing.T, s *client.Stream, id string) sessionRow {
	t.Helper()
	var list struct{ Items []sessionRow }
	if err := s.Call(ctx, "session.list", nil, &list); err != nil {
		t.Fatal(err)
	}
	for _, row := range list.Items {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("session %s is not listed", id)
	return sessionRow{}
}

type closedBlock struct {
	Command     string `json:"command"`
	State       string `json:"state"`
	ExitCode    *int   `json:"exit_code"`
	OutputPlain string `json:"output_plain"`
}

// blockClosed waits for the session's last closed block to be the counter's.
func blockClosed(ctx context.Context, t *testing.T, socket, sessionID string, limit time.Duration) closedBlock {
	t.Helper()
	deadline := time.Now().Add(limit)
	for {
		var b closedBlock
		s := interactive(ctx, t, socket)
		err := s.Call(ctx, "block.get", map[string]any{"block_id": "last", "session_id": sessionID, "include": "plain"}, &b)
		_ = s.Close()
		if err == nil && strings.Contains(b.Command, "seq 1") {
			return b
		}
		if time.Now().After(deadline) {
			t.Fatalf("the counter's block never closed: %+v, %v", b, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// screenSoFar is the session's snapshot, decoded.
func screenSoFar(ctx context.Context, t *testing.T, s *client.Stream, sessionID string) string {
	t.Helper()
	var sub struct {
		Snapshot struct {
			Data string `json:"data_b64"`
		} `json:"snapshot"`
	}
	if err := s.Call(ctx, "session.subscribe", map[string]any{"session_id": sessionID}, &sub); err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(sub.Snapshot.Data)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestHandoffKeepsLivePanes_REQ_HND_001: a daemon serving a pane whose command is half done is
// taken over by `umbrald --takeover`. The old daemon exits 0; the pane is the same session,
// alive, with the screen it had; the command, never interrupted, closes its block on the new
// daemon with every line it printed on either; and the session's end is recorded there with
// no exit code (decision 6).
func TestHandoffKeepsLivePanes_REQ_HND_001(t *testing.T) {
	bin, socket, old := handoffDaemon(t)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()

	command, transcript := counter(80)
	s := interactive(ctx, t, socket)
	sessionID := paneRunning(ctx, t, s, command)
	deadline := time.Now().Add(15 * time.Second)
	for !strings.Contains(screenSoFar(ctx, t, s, sessionID), "c10\r\n") {
		if time.Now().After(deadline) {
			t.Fatal("the counter never printed")
		}
		time.Sleep(50 * time.Millisecond)
	}

	next := runDaemon(t, bin, []string{"--takeover"})
	if code := old.exit(t, 30*time.Second); code != 0 {
		t.Fatalf("the old daemon exited %d; its log:\n%s\nthe new one's:\n%s", code, old.logs.String(), next.logs.String())
	}
	if !next.running() {
		t.Fatalf("the new daemon is gone; its log:\n%s", next.logs.String())
	}

	s = interactive(ctx, t, socket)
	if session := sessionNamed(ctx, t, s, sessionID); session.State != "alive" {
		t.Fatalf("after the handoff the session is %+v; want it alive", session)
	}
	if screen := screenSoFar(ctx, t, s, sessionID); !strings.Contains(screen, "c1\r\n") {
		t.Errorf("the new daemon's screen lost what the old one drew: %q", screen)
	}

	b := blockClosed(ctx, t, socket, sessionID, 30*time.Second)
	if b.State != "finished" || b.ExitCode == nil || *b.ExitCode != 0 {
		t.Errorf("the counter's block is %s with %v, want finished with 0", b.State, b.ExitCode)
	}
	if counted(b.OutputPlain) != transcript {
		t.Errorf("the counter's transcript across the handoff is %q, want every line", b.OutputPlain)
	}

	s = interactive(ctx, t, socket)
	if err := s.Call(ctx, "session.close", map[string]any{"session_id": sessionID}, nil); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(15 * time.Second)
	for {
		exited := sessionNamed(ctx, t, s, sessionID)
		if exited.State == "exited" {
			if exited.ExitCode != nil {
				t.Errorf("the adopted session exited with %d; its code went to the old daemon", *exited.ExitCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the adopted session never exited")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestHandoffFailureKeepsTheOldDaemon_REQ_HND_002: a new daemon made to fail at each step
// exits 75 naming that step, and the old daemon keeps serving the same session: across every
// one of those failures, its command's block closes with every line it printed.
func TestHandoffFailureKeepsTheOldDaemon_REQ_HND_002(t *testing.T) {
	bin, socket, old := handoffDaemon(t)
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()

	command, transcript := counter(300)
	s := interactive(ctx, t, socket)
	sessionID := paneRunning(ctx, t, s, command)

	for _, step := range []string{stepConnect, stepHello, stepQuiesce, stepTransfer, stepAdopt, stepStart, stepConfirm} {
		next := runDaemon(t, bin, []string{"--takeover"}, testHandoffFailEnv+"="+step)
		if code := next.exit(t, 45*time.Second); code != exitTempFail {
			t.Fatalf("failing at %s, the new daemon exited %d, want %d; its log:\n%s", step, code, exitTempFail, next.logs.String())
		}
		if !strings.Contains(next.logs.String(), `"step":"`+step+`"`) {
			t.Fatalf("failing at %s, the new daemon did not name the step:\n%s\nthe old one's log:\n%s", step, next.logs.String(), old.logs.String())
		}
		if !old.running() {
			t.Fatalf("failing at %s, the old daemon exited; its log:\n%s", step, old.logs.String())
		}
		s = interactive(ctx, t, socket)
		if session := sessionNamed(ctx, t, s, sessionID); session.State != "alive" {
			t.Fatalf("failing at %s, the old daemon's session is %+v", step, session)
		}
	}

	b := blockClosed(ctx, t, socket, sessionID, 60*time.Second)
	if b.State != "finished" || b.ExitCode == nil || *b.ExitCode != 0 {
		t.Errorf("the counter's block is %s with %v, want finished with 0", b.State, b.ExitCode)
	}
	if counted(b.OutputPlain) != transcript {
		t.Errorf("across seven failed handoffs the transcript is %q, want every line", b.OutputPlain)
	}
}

// TestHandoffRefusedWhileATurnRuns_REQ_HND_002: while a turn is running the old daemon refuses
// at quiesce, says why, and goes on serving; the new daemon exits 75.
func TestHandoffRefusedWhileATurnRuns_REQ_HND_002(t *testing.T) {
	ollama := &scriptedOllama{replies: [][]string{
		{`{"model":"m","message":{"role":"assistant","content":"thinking"},"done":false}`, scriptedHold},
	}}
	srv := ollama.server(t)
	logs := &lockedBuffer{}
	stream, ctx, _ := startAgentDaemon(t, srv.URL, daemonOpts{logs: logs})
	var thread struct{ ID string }
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": t.TempDir()}, &thread); err != nil {
		t.Fatal(err)
	}
	if err := stream.Call(ctx, "thread.send", map[string]any{"thread_id": thread.ID, "text": "go"}, nil); err != nil {
		t.Fatal(err)
	}
	for {
		var got struct{ State string }
		if err := stream.Call(ctx, "thread.get", map[string]any{"thread_id": thread.ID}, &got); err != nil {
			t.Fatal(err)
		}
		if got.State == "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	next := runDaemon(t, buildDaemon(t), []string{"--takeover"})
	if code := next.exit(t, 45*time.Second); code != exitTempFail {
		t.Fatalf("the new daemon exited %d during a turn, want %d; its log:\n%s", code, exitTempFail, next.logs.String())
	}
	if !strings.Contains(next.logs.String(), "busy") || !strings.Contains(logs.String(), `"step":"quiesce"`) {
		t.Errorf("the refusal does not say a turn is running:\nnew: %s\nold: %s", next.logs.String(), logs.String())
	}
	// The refusal comes before anything is paused: the client that was connected is still
	// connected, and the turn still runs.
	var got struct{ State string }
	if err := stream.Call(ctx, "thread.get", map[string]any{"thread_id": thread.ID}, &got); err != nil || got.State != "running" {
		t.Fatalf("after the refusal the turn is %q, %v; want it still running on the same connection", got.State, err)
	}
}

// TestHandoffDowntime_NFR_F2_05: a client connecting over and over through a handoff is never
// without a daemon answering for a second or more (PRD F2 §7, NFR-F2-05: < 1 s).
func TestHandoffDowntime_NFR_F2_05(t *testing.T) {
	bin, socket, old := handoffDaemon(t)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	s := interactive(ctx, t, socket)
	paneRunning(ctx, t, s, "echo ready\n")
	_ = s.Close()

	var (
		mu      sync.Mutex
		last    = time.Now()
		longest time.Duration
	)
	stop := make(chan struct{})
	polled := make(chan struct{})
	go func() {
		defer close(polled)
		for {
			select {
			case <-stop:
				return
			default:
			}
			call, cancelCall := context.WithTimeout(ctx, 3*time.Second)
			c, err := client.Connect(call, client.Options{SocketPath: socket, NoAutostart: true, ClientKind: client.ClientKindCLI})
			if err == nil {
				err = c.Call(call, "system.status", nil, nil)
				_ = c.Close()
			}
			cancelCall()
			if err == nil {
				mu.Lock()
				if gap := time.Since(last); gap > longest {
					longest = gap
				}
				last = time.Now()
				mu.Unlock()
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	time.Sleep(200 * time.Millisecond)
	runDaemon(t, bin, []string{"--takeover"})
	if code := old.exit(t, 30*time.Second); code != 0 {
		t.Fatalf("the old daemon exited %d; its log:\n%s", code, old.logs.String())
	}
	time.Sleep(500 * time.Millisecond)
	close(stop)
	<-polled

	mu.Lock()
	defer mu.Unlock()
	t.Logf("longest time without an answer across the handoff: %v", longest)
	if longest >= time.Second {
		t.Errorf("clients went %v without a daemon, want < 1 s (NFR-F2-05)", longest)
	}
}

// TestATakeoverForgetsScreensTheSettingRefuses_REQ_TERM_010: a takeover is a start, so with
// `pane_history` off it deletes the stored screens as any start does (Data Model §2.4d). An
// upgrade through --takeover must not keep what the user turned the setting off to remove.
func TestATakeoverForgetsScreensTheSettingRefuses_REQ_TERM_010(t *testing.T) {
	bin, socket, old := handoffDaemon(t)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	paneRunning(ctx, t, interactive(ctx, t, socket), "echo ready\n")

	db, err := sql.Open("sqlite", "file:"+filepath.Join(os.Getenv("XDG_DATA_HOME"), "umbral", "umbral.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	// A screen stored while the setting was on, left by an earlier run.
	if _, err := db.ExecContext(ctx, `INSERT INTO pane_history (pane_id, screen_zst, rows, captured_at)
		SELECT id, x'00', 1, 1 FROM panes LIMIT 1`); err != nil {
		t.Fatal(err)
	}

	next := runDaemon(t, bin, []string{"--takeover"})
	if code := old.exit(t, 30*time.Second); code != 0 {
		t.Fatalf("the old daemon exited %d; the new one's log:\n%s", code, next.logs.String())
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pane_history`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d stored screens survived a takeover with pane_history off", n)
	}
}

// TestAHandoffFromAnAgentsTerminalIsRefused_REQ_HND_002: a process inside an agent's terminal
// is refused at hello, before anything pauses, as the API refuses it what lands the agent's
// work: the handoff would hand it every session's master (delta decision 2).
func TestAHandoffFromAnAgentsTerminalIsRefused_REQ_HND_002(t *testing.T) {
	path := filepath.Join(socketDir(t), handoff.SocketName)
	ln, err := handoff.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	var asked []int
	g := &giver{
		logger:    slog.New(slog.DiscardHandler),
		agentPeer: func(pid int) bool { asked = append(asked, pid); return true },
	}
	gave := make(chan bool, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			gave <- true
			return
		}
		defer func() { _ = c.Close() }()
		// No server, sessions or locks: anything past the refusal would panic.
		gave <- g.give(t.Context(), c)
	}()

	c, err := handoff.Dial(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.Send(handoffFrame{Op: opHello, Protocol: api.ProtocolVersion})
	var got handoffFrame
	if _, err := c.Recv(&got); err != nil {
		t.Fatal(err)
	}
	if got.Op != opError || got.Step != stepHello || !strings.Contains(got.Error, "agent") {
		t.Fatalf("an agent's terminal got %+v, want a refusal at hello", got)
	}
	if <-gave {
		t.Fatal("the handoff was given to an agent's terminal")
	}
	if len(asked) != 1 || asked[0] != os.Getpid() {
		t.Fatalf("the check was asked about %v, want this process", asked)
	}
}

// TestAHandoffUnderAServiceManagerIsRefused_REQ_HND_002: a daemon a service manager runs is
// refused a handoff at hello. Its exit would end the unit, and a stop kills what is left in the
// unit's processes: every shell the new daemon just adopted. The user is told to restart the
// unit instead (delta `2026-10-service-units`).
func TestAHandoffUnderAServiceManagerIsRefused_REQ_HND_002(t *testing.T) {
	path := filepath.Join(socketDir(t), handoff.SocketName)
	ln, err := handoff.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	g := &giver{logger: slog.New(slog.DiscardHandler), manager: "systemd"}
	gave := make(chan bool, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			gave <- true
			return
		}
		defer func() { _ = c.Close() }()
		// No server, sessions or locks: anything past the refusal would panic.
		gave <- g.give(t.Context(), c)
	}()

	c, err := handoff.Dial(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.Send(handoffFrame{Op: opHello, Protocol: api.ProtocolVersion})
	var got handoffFrame
	if _, err := c.Recv(&got); err != nil {
		t.Fatal(err)
	}
	if got.Op != opError || got.Step != stepHello || !strings.Contains(got.Error, "systemctl --user restart umbrald") {
		t.Fatalf("a daemon under systemd answered %+v, want a refusal at hello naming the restart", got)
	}
	if <-gave {
		t.Fatal("a daemon under systemd handed its sessions over")
	}
}

// TestTheServiceManagerIsReadFromTheEnvironment_REQ_HND_002: systemd sets SYSTEMD_EXEC_PID to the pid it
// executed for a unit, so only that process is the unit's daemon. A daemon `umb` autostarted
// from a terminal some unit or scope started inherits the variable, and INVOCATION_ID, with
// another pid, and is not.
func TestTheServiceManagerIsReadFromTheEnvironment_REQ_HND_002(t *testing.T) {
	t.Setenv("INVOCATION_ID", "5c1e0d9f2a7b4e3c8d6f1a2b3c4d5e6f")
	for _, tc := range []struct {
		execPID, want string
	}{
		{"", ""},
		{strconv.Itoa(os.Getppid()), ""},
		{"not a pid", ""},
		{strconv.Itoa(os.Getpid()), "systemd"},
	} {
		t.Setenv("SYSTEMD_EXEC_PID", tc.execPID)
		if got := serviceManager(); got != tc.want {
			t.Errorf("SYSTEMD_EXEC_PID=%q: the manager is %q, want %q", tc.execPID, got, tc.want)
		}
	}
}
