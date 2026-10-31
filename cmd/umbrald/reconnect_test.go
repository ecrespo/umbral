package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/client"
	"github.com/ecrespo/umbral/internal/desktop"
	dskports "github.com/ecrespo/umbral/internal/desktop/ports"
	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
	tuidaemon "github.com/ecrespo/umbral/internal/tui/adapters/daemon"
	tuiports "github.com/ecrespo/umbral/internal/tui/ports"
)

// redialer dials socket as kind, without starting a daemon, as the TUI and the desktop do.
func redialer(socket, kind string, unbounded bool) func(context.Context) (*client.Stream, error) {
	return func(ctx context.Context) (*client.Stream, error) {
		c, err := client.Connect(ctx, client.Options{SocketPath: socket, NoAutostart: true, ClientKind: kind})
		if err != nil {
			return nil, err
		}
		if unbounded {
			return client.NewUnboundedStream(c), nil
		}
		return client.NewStream(c), nil
	}
}

// TestTheTUIFollowsItsDaemonAcrossAHandoff_REQ_HND_001: the TUI's adapter, attached to a pane
// printing a counter, is closed by the old daemon, dials the new one and says so. Attached
// again, its snapshot is current as of a seq no lower than the last chunk it had, the output
// after it only moves forward — a batch carries its last chunk's seq (API §8), so numbers may
// be skipped but never repeated — and the snapshot and that output hold every line of the
// counter.
func TestTheTUIFollowsItsDaemonAcrossAHandoff_REQ_HND_001(t *testing.T) {
	bin, socket, old := handoffDaemon(t)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	command, _ := counter(60)
	s := interactive(ctx, t, socket)
	sessionID := paneRunning(ctx, t, s, command)
	midCounter(ctx, t, s, sessionID)

	dial := redialer(socket, client.ClientKindTUI, false)
	first, err := dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d := tuidaemon.NewReconnecting(first, dial)
	defer func() { _ = d.Close() }()
	_, seq, err := d.Subscribe(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}

	next := runDaemon(t, bin, []string{"--takeover"})
	var reconnecting bool
	for !func() bool {
		select {
		case ev, ok := <-d.Events():
			if !ok {
				t.Fatalf("the TUI's events ended; the new daemon's log:\n%s", next.logs.String())
			}
			switch ev.Kind {
			case tuiports.EventOutput:
				if ev.SessionID == sessionID && ev.Seq > seq {
					seq = ev.Seq
				}
			case tuiports.EventReconnecting:
				reconnecting = true
			case tuiports.EventDisconnected:
				t.Fatalf("the TUI gave up on its daemon: %v; the new one's log:\n%s", ev.Err, next.logs.String())
			case tuiports.EventReconnected:
				return true
			}
		case <-ctx.Done():
			t.Fatalf("the TUI never reconnected; the new daemon's log:\n%s", next.logs.String())
		}
		return false
	}() {
	}
	if !reconnecting {
		t.Error("the TUI was not told the daemon went away before it reconnected")
	}
	if code := old.exit(t, 30*time.Second); code != 0 {
		t.Fatalf("the old daemon exited %d", code)
	}

	screen, resumed, err := d.Subscribe(ctx, sessionID)
	if err != nil {
		t.Fatalf("the pane could not attach on the new daemon: %v", err)
	}
	if resumed < seq {
		t.Fatalf("the new daemon's snapshot is current as of seq %d, before the %d the TUI had", resumed, seq)
	}
	if !strings.Contains(string(screen), "c1\r\n") {
		t.Errorf("the fresh snapshot lost what was drawn before the handoff: %q", screen)
	}
	drawn := string(screen)
	last := resumed
	for !holdsLine(drawn, "c60") {
		select {
		case ev := <-d.Events():
			if ev.Kind == tuiports.EventDisconnected {
				t.Fatalf("the TUI lost the new daemon: %v", ev.Err)
			}
			if ev.Kind != tuiports.EventOutput || ev.SessionID != sessionID {
				continue
			}
			if ev.Seq <= last {
				t.Fatalf("after seq %d the output is seq %d: a chunk came twice", last, ev.Seq)
			}
			last = ev.Seq
			drawn += string(ev.Data)
		case <-ctx.Done():
			t.Fatal("the counter never ended on the new daemon")
		}
	}
	if missing := missingLines(drawn, 60); len(missing) > 0 {
		t.Fatalf("the fresh snapshot and the output after it lack the counter's lines %v", missing)
	}
}

// midCounter waits until the counter is under way, so a handoff lands in the middle of it.
func midCounter(ctx context.Context, t *testing.T, s *client.Stream, sessionID string) {
	t.Helper()
	for !strings.Contains(screenSoFar(ctx, t, s, sessionID), "c10\r\n") {
		select {
		case <-ctx.Done():
			t.Fatal("the counter never printed")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// missingLines are the numbers of the counter's lines c1 … c<n> that drawn does not hold. A
// line is its number not followed by another digit: a snapshot ends the cursor's row with a
// cursor move rather than a newline.
func missingLines(drawn string, n int) []int {
	var missing []int
	for i := 1; i <= n; i++ {
		if !holdsLine(drawn, fmt.Sprintf("c%d", i)) {
			missing = append(missing, i)
		}
	}
	return missing
}

func holdsLine(drawn, line string) bool {
	for rest := drawn; ; {
		j := strings.Index(rest, line)
		if j < 0 {
			return false
		}
		rest = rest[j+len(line):]
		if rest == "" || rest[0] < '0' || rest[0] > '9' {
			return true
		}
	}
}

// recorder is a desktop page: the events it was told, in order.
type recorder struct {
	mu       sync.Mutex
	names    []string
	lost     []string
	attached []string
	signal   chan string
}

func (r *recorder) Emit(name string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.names = append(r.names, name)
	if m, ok := data.(map[string][]string); ok && name == desktop.EventReconnected {
		r.lost, r.attached = m["lost"], m["attached"]
	}
	if name != desktop.EventFrame && name != desktop.EventDaemon {
		select {
		case r.signal <- name:
		default:
		}
	}
}

// fedScreen records what a terminal was fed.
type fedScreen struct {
	mu  sync.Mutex
	fed strings.Builder
}

func (f *fedScreen) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fed.Write(p)
}
func (f *fedScreen) Resize(sessdomain.Size) error   { return nil }
func (f *fedScreen) Frame() (dskports.Frame, error) { return dskports.Frame{}, nil }
func (f *fedScreen) Reset() error                   { return nil }
func (f *fedScreen) Close() error                   { return nil }
func (f *fedScreen) String() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fed.String()
}

// TestTheDesktopFollowsItsDaemonAcrossAHandoff_REQ_HND_001: the desktop bridge, with a
// terminal attached, follows its daemon to the new one, attaches the terminal again before it
// tells the page `reconnected`, and that terminal goes on drawing the pane's output.
func TestTheDesktopFollowsItsDaemonAcrossAHandoff_REQ_HND_001(t *testing.T) {
	bin, socket, old := handoffDaemon(t)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	command, _ := counter(60)
	s := interactive(ctx, t, socket)
	sessionID := paneRunning(ctx, t, s, command)
	midCounter(ctx, t, s, sessionID)

	dial := redialer(socket, client.ClientKindDesktop, true)
	first, err := dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var (
		screensMu sync.Mutex
		screens   []*fedScreen
	)
	page := &recorder{signal: make(chan string, 8)}
	b := desktop.NewReconnecting(first, dial, page, func(sessdomain.Size) (dskports.Screen, error) {
		s := &fedScreen{}
		screensMu.Lock()
		screens = append(screens, s)
		screensMu.Unlock()
		return s, nil
	})
	defer func() { _ = b.Close() }()
	if err := b.Attach(sessionID, 80, 24); err != nil {
		t.Fatal(err)
	}

	next := runDaemon(t, bin, []string{"--takeover"})
	for told := ""; told != desktop.EventReconnected; {
		select {
		case told = <-page.signal:
			if told == desktop.EventDisconnected {
				t.Fatalf("the desktop gave up on its daemon; the new one's log:\n%s", next.logs.String())
			}
		case <-ctx.Done():
			t.Fatalf("the desktop never reconnected; the new daemon's log:\n%s", next.logs.String())
		}
	}
	if code := old.exit(t, 30*time.Second); code != 0 {
		t.Fatalf("the old daemon exited %d", code)
	}
	page.mu.Lock()
	names, lost, attached := append([]string(nil), page.names...), page.lost, page.attached
	page.mu.Unlock()
	if len(lost) != 0 || len(attached) != 1 || attached[0] != sessionID {
		t.Fatalf("across the handoff %v were attached again and %v lost, want %s attached", attached, lost, sessionID)
	}
	if i := strings.Index(strings.Join(names, " "), desktop.EventReconnecting); i < 0 {
		t.Error("the page was not told the daemon went away")
	}

	screensMu.Lock()
	if len(screens) != 2 {
		screensMu.Unlock()
		t.Fatalf("%d screens were made, want the first and the one attached again", len(screens))
	}
	again := screens[1]
	screensMu.Unlock()
	for !strings.Contains(again.String(), "c60") {
		select {
		case <-ctx.Done():
			t.Fatalf("the terminal attached again never drew the counter's end: %q", again.String())
		case <-time.After(50 * time.Millisecond):
		}
	}
	if missing := missingLines(again.String(), 60); len(missing) > 0 {
		t.Errorf("the terminal attached again lacks the counter's lines %v", missing)
	}
}
