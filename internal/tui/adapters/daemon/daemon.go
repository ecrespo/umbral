// Package daemon adapts the JSON-RPC client to the narrow view the TUI model needs.
//
// It is the only place in the client that knows method names and parameter shapes, which
// is what lets the model be tested against a fake instead of a socket.
package daemon

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/ecrespo/umbral/internal/client"
	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/tui/ports"
)

// paramSessionID is the parameter name every session-scoped method takes (API Spec §5).
const paramSessionID = "session_id"

// snapshotFormat is the only format this client can replay (API Spec §5.11).
const snapshotFormat = "vt"

// errUnsupportedSnapshot is returned when the daemon sends a snapshot in a format this
// client cannot replay.
var errUnsupportedSnapshot = errors.New("tui: unsupported snapshot format")

// checkSnapshotFormat guards the replay. It is separate so that it can be tested without
// a socket: feeding an unknown format to a VT parser would draw garbage instead of
// reporting the mismatch, which is a failure nobody would recognise as one.
func checkSnapshotFormat(format string) error {
	if format != snapshotFormat {
		return fmt.Errorf("%w: the daemon sent %q and this client replays %q",
			errUnsupportedSnapshot, format, snapshotFormat)
	}
	return nil
}

// Daemon wraps a streaming client.
type Daemon struct {
	stream *client.Stream
	events chan ports.Event
}

// New starts translating the stream's notifications into events. It takes ownership: Close
// ends both.
//
// The buffer is one larger than the stream's, so the terminal event always has a slot.
// Reporting "you fell behind" on a channel that is full because the reader fell behind
// would park this goroutine forever, and the overflow the code detects would never reach
// the user.
func New(stream *client.Stream) *Daemon {
	d := &Daemon{
		stream: stream,
		events: make(chan ports.Event, client.NotificationBuffer+1),
	}
	go d.translate()
	return d
}

// Events is the channel the model listens on.
func (d *Daemon) Events() <-chan ports.Event { return d.events }

// Close ends the connection.
func (d *Daemon) Close() error { return d.stream.Close() }

// CreateSession starts a shell (API Spec §5.9).
func (d *Daemon) CreateSession(ctx context.Context, size sessdomain.Size) (sessdomain.Session, error) {
	var out struct {
		ID          string `json:"id"`
		Shell       string `json:"shell"`
		CWD         string `json:"cwd"`
		Cols        uint16 `json:"cols"`
		Rows        uint16 `json:"rows"`
		State       string `json:"state"`
		Integration string `json:"integration"`
		InputOwner  string `json:"input_owner"`
		CreatedAt   int64  `json:"created_at"`
	}
	params := map[string]any{"cols": size.Cols, "rows": size.Rows}
	if err := d.stream.Call(ctx, "session.create", params, &out); err != nil {
		return sessdomain.Session{}, err
	}
	return sessdomain.Session{
		ID:          out.ID,
		Shell:       out.Shell,
		CWD:         out.CWD,
		Size:        sessdomain.Size{Cols: out.Cols, Rows: out.Rows},
		State:       sessdomain.State(out.State),
		Integration: sessdomain.Integration(out.Integration),
		InputOwner:  sessdomain.InputOwner(out.InputOwner),
		CreatedAt:   epochMS(out.CreatedAt),
	}, nil
}

// Subscribe attaches and returns the snapshot plus the seq it is current as of
// (API Spec §5.11, REQ-TERM-004).
func (d *Daemon) Subscribe(ctx context.Context, sessionID string) ([]byte, uint64, error) {
	var out struct {
		Snapshot struct {
			Format  string `json:"format"`
			DataB64 string `json:"data_b64"`
		} `json:"snapshot"`
		Seq uint64 `json:"seq"`
	}
	params := map[string]any{paramSessionID: sessionID}
	if err := d.stream.Call(ctx, "session.subscribe", params, &out); err != nil {
		return nil, 0, err
	}
	if err := checkSnapshotFormat(out.Snapshot.Format); err != nil {
		return nil, 0, err
	}
	raw, err := base64.StdEncoding.DecodeString(out.Snapshot.DataB64)
	if err != nil {
		return nil, 0, fmt.Errorf("tui: the snapshot is not base64: %w", err)
	}
	return raw, out.Seq, nil
}

// Input sends keystrokes (API Spec §5.13).
func (d *Daemon) Input(ctx context.Context, sessionID string, data []byte) error {
	params := map[string]any{
		paramSessionID: sessionID,
		"data_b64":     base64.StdEncoding.EncodeToString(data),
	}
	return d.stream.Call(ctx, "session.input", params, nil)
}

// Resize tells the daemon a pane changed size (API Spec §5.14, REQ-TERM-007).
func (d *Daemon) Resize(ctx context.Context, sessionID string, size sessdomain.Size) error {
	params := map[string]any{
		paramSessionID: sessionID,
		"cols":         size.Cols,
		"rows":         size.Rows,
	}
	return d.stream.Call(ctx, "session.resize", params, nil)
}

// Blocks lists a session's blocks, newest first (API Spec §5.16).
func (d *Daemon) Blocks(ctx context.Context, sessionID string, limit int) ([]sessdomain.Block, error) {
	var out struct {
		Items []struct {
			ID         string `json:"id"`
			SessionID  string `json:"session_id"`
			Origin     string `json:"origin"`
			Command    string `json:"command"`
			CWD        string `json:"cwd"`
			Host       string `json:"host"`
			State      string `json:"state"`
			ExitCode   *int   `json:"exit_code"`
			StartedAt  int64  `json:"started_at"`
			EndedAt    *int64 `json:"ended_at"`
			DurationMS *int64 `json:"duration_ms"`
		} `json:"items"`
	}
	params := map[string]any{paramSessionID: sessionID, "limit": limit}
	if err := d.stream.Call(ctx, "block.list", params, &out); err != nil {
		return nil, err
	}

	blocks := make([]sessdomain.Block, 0, len(out.Items))
	for _, it := range out.Items {
		b := sessdomain.Block{
			ID:        it.ID,
			SessionID: it.SessionID,
			Origin:    sessdomain.BlockOrigin(it.Origin),
			Command:   it.Command,
			CWD:       it.CWD,
			Host:      it.Host,
			State:     sessdomain.BlockState(it.State),
			ExitCode:  it.ExitCode,
			StartedAt: epochMS(it.StartedAt),
		}
		if it.EndedAt != nil {
			t := epochMS(*it.EndedAt)
			b.EndedAt = &t
		}
		blocks = append(blocks, b)
	}
	return blocks, nil
}

// translate turns notifications into the events the model reacts to.
//
// Unknown methods are dropped rather than reported: API Spec §9 makes a new notification
// an additive change, so a client that failed on one it did not recognise would break on
// every daemon upgrade.
func (d *Daemon) translate() {
	defer close(d.events)

	for n := range d.stream.Notifications() {
		ev, ok := toEvent(n)
		if !ok {
			continue
		}
		select {
		case d.events <- ev:
		default:
			// The model is not draining. Saying so is better than growing: the recovery
			// is a fresh snapshot, and a silent drop would leave the screen missing bytes.
			// The reserved slot is what makes this send safe on a full channel.
			d.report(ports.Event{Kind: ports.EventDisconnected, Err: client.ErrStreamOverflow})
			return
		}
	}
	d.report(ports.Event{Kind: ports.EventDisconnected, Err: d.stream.Err()})
}

// report delivers a terminal event without ever blocking. The buffer reserves a slot for
// exactly one of these; if it has somehow gone too, the event is dropped rather than
// parking this goroutine, because a parked goroutine would also stop `close(d.events)`
// from running and the model would wait on a channel that never ends.
func (d *Daemon) report(ev ports.Event) {
	select {
	case d.events <- ev:
	default:
	}
}

func toEvent(n client.Notification) (ports.Event, bool) {
	switch n.Method {
	case "session.output":
		var p struct {
			SessionID string `json:"session_id"`
			Seq       uint64 `json:"seq"`
			DataB64   string `json:"data_b64"`
		}
		if err := n.Decode(&p); err != nil {
			return ports.Event{}, false
		}
		raw, err := base64.StdEncoding.DecodeString(p.DataB64)
		if err != nil {
			return ports.Event{}, false
		}
		return ports.Event{
			Kind: ports.EventOutput, SessionID: p.SessionID, Seq: p.Seq, Data: raw,
		}, true

	case "block.closed":
		var p struct {
			SessionID string `json:"session_id"`
		}
		if err := n.Decode(&p); err != nil {
			return ports.Event{}, false
		}
		return ports.Event{Kind: ports.EventBlockClosed, SessionID: p.SessionID}, true

	case "session.exited":
		var p struct {
			SessionID string `json:"session_id"`
		}
		if err := n.Decode(&p); err != nil {
			return ports.Event{}, false
		}
		return ports.Event{Kind: ports.EventSessionExited, SessionID: p.SessionID}, true

	case "session.unsubscribed":
		// The daemon dropped this subscription for falling behind (API Spec §6, §8). The
		// connection is still up, and §6 says what to do: subscribe again and take the
		// fresh snapshot. Reporting it as a disconnection would strand a working
		// connection on a frozen screen.
		var p struct {
			SessionID string `json:"session_id"`
			Reason    string `json:"reason"`
		}
		if err := n.Decode(&p); err != nil {
			return ports.Event{}, false
		}
		return ports.Event{
			Kind:      ports.EventUnsubscribed,
			SessionID: p.SessionID,
			Err:       fmt.Errorf("the daemon dropped the subscription: %s", p.Reason),
		}, true
	}
	return ports.Event{}, false
}

// epochMS converts the API's UTC epoch milliseconds (Art. 6) into a time.
func epochMS(ms int64) time.Time { return time.UnixMilli(ms).UTC() }
