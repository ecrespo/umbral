package daemon

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/ecrespo/umbral/internal/client"
	"github.com/ecrespo/umbral/internal/tui/ports"
)

// This file exists because this package is the only place in the client that knows method
// names, parameter shapes and notification names — and the model's tests all use a fake
// Daemon, so they walk straight past it. That is exactly the blind spot the `client_kind`
// bug lived in: every fake answered the handshake without reading it.

// TestToEventMapsTheNotificationsTheSpecDefines_REQ_TERM_004 feeds literal frames of the
// shape API Spec §6 documents and checks what the model would be told.
//
// The `session.unsubscribed` row is the one that matters most: it is one word apart in the
// notification table from an event that is fatal, and mapping it wrong strands a working
// connection on a frozen screen.
func TestToEventMapsTheNotificationsTheSpecDefines_REQ_TERM_004(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		method string
		params string
		want   ports.EventKind
		wantID string
		data   string
		seq    uint64
		drop   bool
	}{
		{
			name: "output", method: "session.output",
			params: `{"session_id":"ses_x","seq":9,"data_b64":"aGk="}`,
			want:   ports.EventOutput, wantID: "ses_x", data: "hi", seq: 9,
		},
		{
			name: "block closed", method: "block.closed",
			params: `{"id":"blk_x","session_id":"ses_x","state":"finished"}`,
			want:   ports.EventBlockClosed, wantID: "ses_x",
		},
		{
			name: "session exited", method: "session.exited",
			params: `{"session_id":"ses_x","exit_code":0,"exited_at":1757592000000}`,
			want:   ports.EventSessionExited, wantID: "ses_x",
		},
		{
			// API Spec §6: the connection is still up and the client re-subscribes.
			name: "subscription dropped", method: "session.unsubscribed",
			params: `{"session_id":"ses_x","reason":"slow_client"}`,
			want:   ports.EventUnsubscribed, wantID: "ses_x",
		},
		{
			// §9 makes a new notification an additive change, so one this client does
			// not know must be ignored rather than treated as an error.
			name: "unknown method", method: "thread.stalled",
			params: `{"thread_id":"thr_x"}`, drop: true,
		},
		{
			name: "malformed params", method: "session.output",
			params: `{"session_id":"ses_x","data_b64":"not base64!!"}`, drop: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ev, ok := toEvent(client.Notification{
				Method: tc.method, Params: json.RawMessage(tc.params),
			})
			if tc.drop {
				if ok {
					t.Fatalf("%s produced an event; it should be ignored", tc.method)
				}
				return
			}
			if !ok {
				t.Fatalf("%s produced no event", tc.method)
			}
			if ev.Kind != tc.want {
				t.Errorf("kind = %v, want %v", ev.Kind, tc.want)
			}
			if ev.SessionID != tc.wantID {
				t.Errorf("session_id = %q, want %q", ev.SessionID, tc.wantID)
			}
			if tc.data != "" && string(ev.Data) != tc.data {
				t.Errorf("data = %q, want %q", ev.Data, tc.data)
			}
			if tc.seq != 0 && ev.Seq != tc.seq {
				t.Errorf("seq = %d, want %d", ev.Seq, tc.seq)
			}
		})
	}
}

// TestUnsubscribedIsNotADisconnection pins the distinction on its own, because the two
// map to different recoveries and the compiler cannot tell them apart.
func TestUnsubscribedIsNotADisconnection(t *testing.T) {
	t.Parallel()

	ev, ok := toEvent(client.Notification{
		Method: "session.unsubscribed",
		Params: json.RawMessage(`{"session_id":"ses_x","reason":"slow_client"}`),
	})
	if !ok {
		t.Fatal("session.unsubscribed produced no event")
	}
	if ev.Kind == ports.EventDisconnected {
		t.Fatal("a dropped subscription was reported as losing the daemon; API Spec §6 says re-subscribe")
	}
	if ev.Err == nil {
		t.Error("the event carries no reason; the status line would say nothing useful")
	}
}

// TestSubscribeRefusesASnapshotItCannotReplay: the daemon promises `format: "vt"`
// (API Spec §5.11). Feeding something else to a VT parser would draw garbage rather than
// report a mismatch, so the format is checked instead of assumed.
func TestSubscribeRefusesASnapshotItCannotReplay(t *testing.T) {
	t.Parallel()

	err := checkSnapshotFormat("png")
	if err == nil {
		t.Fatal("a snapshot in an unknown format was accepted")
	}
	if !errors.Is(err, errUnsupportedSnapshot) {
		t.Errorf("error = %v, want one that names the format mismatch", err)
	}
	if err := checkSnapshotFormat("vt"); err != nil {
		t.Errorf("the documented format was refused: %v", err)
	}
}
