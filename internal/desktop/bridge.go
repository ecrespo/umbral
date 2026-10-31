// Package desktop is the Wails client's core: what its window calls and what it is told,
// over the published protocol only (REQ-DSK-001, DD-001). It holds no state of its own beyond
// the screens it draws; the daemon owns everything else.
//
// The page calls Call for the protocol's methods, from a fixed list, and Attach, Input and
// Resize for terminals. It is told every notification as `daemon`, a terminal's screen as
// `frame` and the connection's end as `disconnected`. `cmd/umbral-desktop` binds this type
// to the page; nothing here knows Wails.
package desktop

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/ecrespo/umbral/internal/client"
	"github.com/ecrespo/umbral/internal/desktop/ports"
	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
)

// The events the page is told.
const (
	EventDaemon       = "daemon"
	EventFrame        = "frame"
	EventDisconnected = "disconnected"
)

// Methods is what the page may call through Call: the methods its views of REQ-DSK-001 use —
// workspaces, tabs, panes, blocks, threads, approvals, worktrees and their files — and nothing else, so
// nothing that manages the daemon's configuration, keys or extensions, which stay with `umb`
// (delta `2026-10-desktop-client`, decision 2). Terminals go through Attach, Input and Resize,
// and the threads view's list through Threads.
var Methods = []string{
	"system.status", "session.snapshot",
	"workspace.create", "workspace.focus",
	"tab.create", "tab.focus",
	"pane.split", "pane.focus", "pane.close",
	"block.list", "block.get",
	"thread.create", "thread.send", "thread.cancel", "thread.get", "thread.focus",
	"approval.list", "approval.respond",
	"worktree.list", "worktree.diff", "worktree.merge", "worktree.discard",
	// The review of REQ-DSK-002 and the file tree of REQ-DSK-003 (delta `2026-10-hunks-file-tree`).
	"worktree.reject_hunks", "worktree.files", "worktree.read_file", "worktree.write_file",
}

// paramSessionID is the parameter every session method takes (API §5).
const paramSessionID = "session_id"

// termSize is a page's terminal size, refused when it is not one the daemon takes: between
// sessdomain.MinCols×MinRows and MaxCols×MaxRows (API §5.9). The first check only keeps the
// conversion from wrapping.
func termSize(cols, rows int) (sessdomain.Size, error) {
	if cols < 0 || rows < 0 || cols > math.MaxUint16 || rows > math.MaxUint16 {
		return sessdomain.Size{}, fmt.Errorf("desktop: %dx%d is not a terminal size", cols, rows)
	}
	size := sessdomain.Size{Cols: uint16(cols), Rows: uint16(rows)}
	return size, size.Validate()
}

// ErrNotAllowed is Call's error for a method outside Methods.
var ErrNotAllowed = errors.New("desktop: the window does not call this method")

// callTimeout bounds one call from the page; a daemon that stops answering must not hang a
// button forever.
const callTimeout = 30 * time.Second

// frameInterval is how often a terminal with new output is drawn: output arrives in batches
// every 4 ms (API §8), and drawing each one would spend the page's time on frames nobody sees.
const frameInterval = 16 * time.Millisecond

// Frame is the `frame` event: one terminal's screen.
type Frame struct {
	SessionID string      `json:"session_id"`
	Frame     ports.Frame `json:"frame"`
}

// Notification is the `daemon` event: a notification as the daemon sent it.
type Notification struct {
	Method string          `json:"method"`
	Seq    uint64          `json:"seq"`
	Params json.RawMessage `json:"params"`
}

// Bridge is one window's connection to the daemon.
type Bridge struct {
	stream    *client.Stream
	emit      ports.Emitter
	newScreen ports.ScreenFactory

	mu      sync.Mutex
	screens map[string]*terminal
	wake    chan struct{}
	done    chan struct{}
	once    sync.Once
}

// terminal is an attached session: its screen, the output seq it is current as of, and
// whether it has output not yet drawn.
type terminal struct {
	screen ports.Screen
	seq    uint64
	dirty  bool
}

// New starts the bridge over a `desktop` connection. It owns the stream: Close ends it.
func New(stream *client.Stream, emit ports.Emitter, newScreen ports.ScreenFactory) *Bridge {
	b := &Bridge{
		stream: stream, emit: emit, newScreen: newScreen,
		screens: map[string]*terminal{}, wake: make(chan struct{}, 1), done: make(chan struct{}),
	}
	go b.pump()
	go b.draw()
	return b
}

// Close ends the connection and releases every screen.
func (b *Bridge) Close() error {
	b.once.Do(func() { close(b.done) })
	err := b.stream.Close()
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, t := range b.screens {
		_ = t.screen.Close()
		delete(b.screens, id)
	}
	return err
}

// Call calls one of Methods with params, a JSON object or empty, and answers its result as
// the daemon sent it.
func (b *Bridge) Call(method string, params json.RawMessage) (json.RawMessage, error) {
	if !slices.Contains(Methods, method) {
		return nil, fmt.Errorf("%w: %s", ErrNotAllowed, method)
	}
	var p any = map[string]any{}
	if len(params) > 0 && string(params) != "null" {
		p = params
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	var out json.RawMessage
	if err := b.stream.Call(ctx, method, p, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Attach subscribes to a session and draws it at cols×rows: the snapshot is replayed into an
// empty screen and output after its seq is applied as it arrives (API §5.11). Attaching again
// starts over from a fresh snapshot.
func (b *Bridge) Attach(sessionID string, cols, rows int) error {
	size, err := termSize(cols, rows)
	if err != nil {
		return err
	}
	screen, err := b.newScreen(size)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	if err := b.stream.Call(ctx, "session.resize", map[string]any{paramSessionID: sessionID, "cols": cols, "rows": rows}, nil); err != nil {
		_ = screen.Close()
		return err
	}
	var sub struct {
		Snapshot struct {
			Format string `json:"format"`
			Data   string `json:"data_b64"`
		} `json:"snapshot"`
		Seq uint64 `json:"seq"`
	}
	// The lock is held across the subscribe so output that arrives before the reply waits for
	// the screen it belongs to; the daemon holds that output until the reply is written.
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.stream.Call(ctx, "session.subscribe", map[string]any{paramSessionID: sessionID}, &sub); err != nil {
		_ = screen.Close()
		return err
	}
	if sub.Snapshot.Format != "vt" {
		_ = screen.Close()
		return fmt.Errorf("desktop: the daemon sent a %q snapshot and this client replays vt", sub.Snapshot.Format)
	}
	data, err := base64.StdEncoding.DecodeString(sub.Snapshot.Data)
	if err != nil {
		_ = screen.Close()
		return fmt.Errorf("desktop: the snapshot: %w", err)
	}
	if _, err := screen.Write(data); err != nil {
		_ = screen.Close()
		return err
	}
	if old := b.screens[sessionID]; old != nil {
		_ = old.screen.Close()
	}
	b.screens[sessionID] = &terminal{screen: screen, seq: sub.Seq, dirty: true}
	b.kick()
	return nil
}

// Detach stops drawing a session and unsubscribes from it.
func (b *Bridge) Detach(sessionID string) error {
	b.mu.Lock()
	if t := b.screens[sessionID]; t != nil {
		_ = t.screen.Close()
		delete(b.screens, sessionID)
	}
	b.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return b.stream.Call(ctx, "session.unsubscribe", map[string]any{paramSessionID: sessionID}, nil)
}

// Input sends what the user typed to a session.
func (b *Bridge) Input(sessionID, data string) error {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return b.stream.Call(ctx, "session.input", map[string]any{
		paramSessionID: sessionID, "data_b64": base64.StdEncoding.EncodeToString([]byte(data)),
	}, nil)
}

// Resize resizes a session's screen and tells the daemon (REQ-TERM-007).
func (b *Bridge) Resize(sessionID string, cols, rows int) error {
	size, err := termSize(cols, rows)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	if err := b.stream.Call(ctx, "session.resize", map[string]any{paramSessionID: sessionID, "cols": cols, "rows": rows}, nil); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if t := b.screens[sessionID]; t != nil {
		if err := t.screen.Resize(size); err != nil {
			return err
		}
		t.dirty = true
		b.kick()
	}
	return nil
}

// pump reads the stream: output goes into its screen, everything else to the page as it came.
func (b *Bridge) pump() {
	for n := range b.stream.Notifications() {
		if n.Method == "session.output" {
			b.output(n)
			continue
		}
		// A `session.unsubscribed` among them tells the page to attach again, which brings a
		// fresh snapshot (API §8).
		b.emit.Emit(EventDaemon, Notification{Method: n.Method, Seq: n.Seq, Params: n.Params})
	}
	msg := ""
	if err := b.stream.Err(); err != nil {
		msg = err.Error()
	}
	b.emit.Emit(EventDisconnected, map[string]string{"error": msg})
}

// output applies one `session.output` to its screen, dropping what the snapshot already held.
func (b *Bridge) output(n client.Notification) {
	var out struct {
		SessionID string `json:"session_id"`
		Seq       uint64 `json:"seq"`
		Data      string `json:"data_b64"`
	}
	if n.Decode(&out) != nil {
		return
	}
	data, err := base64.StdEncoding.DecodeString(out.Data)
	if err != nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	t := b.screens[out.SessionID]
	if t == nil || out.Seq <= t.seq {
		return
	}
	if _, err := t.screen.Write(data); err == nil {
		t.seq, t.dirty = out.Seq, true
		b.kick()
	}
}

func (b *Bridge) kick() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// draw renders the screens with new output at most once a frameInterval.
func (b *Bridge) draw() {
	for {
		select {
		case <-b.done:
			return
		case <-b.wake:
		}
		var frames []Frame
		b.mu.Lock()
		for id, t := range b.screens {
			if !t.dirty {
				continue
			}
			if f, err := t.screen.Frame(); err == nil {
				frames = append(frames, Frame{SessionID: id, Frame: f})
			}
			t.dirty = false
		}
		b.mu.Unlock()
		for _, f := range frames {
			b.emit.Emit(EventFrame, f)
		}
		select {
		case <-b.done:
			return
		case <-time.After(frameInterval):
		}
	}
}
