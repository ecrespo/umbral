package api

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
)

// helloWith is the handshake with the client's own capabilities (API Spec §5.1).
func (c *client) helloWith(token string, kind ClientKind, capabilities []string) response {
	return c.call(1, "system.hello", map[string]any{
		"token": token, "client_kind": string(kind), "client_version": "0.1.0-test",
		"protocol_version": ProtocolVersion, "capabilities": capabilities,
	})
}

// outputText collects session.output until its data holds n bytes, or five seconds pass, and
// returns the data joined.
func outputText(t *testing.T, c *client, n int) string {
	t.Helper()
	join := func(got []map[string]any) string {
		var b strings.Builder
		for _, p := range got {
			data, err := base64.StdEncoding.DecodeString(p["data_b64"].(string))
			if err != nil {
				t.Fatal(err)
			}
			b.Write(data)
		}
		return b.String()
	}
	return join(c.collectNotificationsUntil("session.output", 5*time.Second, func(got []map[string]any) bool {
		return len(join(got)) >= n
	}))
}

// TestPaneGraphicsReachOnlyAClientThatAdvertisesThem_REQ_PLG_003: a pane's Kitty graphics
// escapes reach a client whose hello lists `graphics` as they came, and a client without it
// sees a placeholder where an image is displayed, in its snapshot as in the live stream; the
// daemon advertises `graphics` (delta `2026-10-pane-graphics`).
func TestPaneGraphicsReachOnlyAClientThatAdvertisesThem_REQ_PLG_003(t *testing.T) {
	t.Parallel()
	const image = "\x1b_Ga=T,f=100,m=1;iVBORw0K\x1b\\\x1b_Gm=0;GgoA\x1b\\"
	sessions := newFakeSessions()
	sessions.setSnapshot([]byte("before "+image+" shown"), 1, 0, 0)
	s := testServerWithSessions(t, sessions)

	with, without := dial(t, s), dial(t, s)
	resp := with.helloWith(s.Token(), ClientTUI, []string{"graphics", "something-later"})
	if resp.Error != nil {
		t.Fatalf("hello with graphics: %+v", resp.Error)
	}
	if !slices.Contains(capabilitiesOf(t, resp.Result), "graphics") {
		t.Errorf("the daemon does not advertise graphics: %v", capabilitiesOf(t, resp.Result))
	}
	if resp := without.hello(s.Token(), ClientTUI); resp.Error != nil {
		t.Fatalf("hello: %+v", resp.Error)
	}

	snapshots := map[*client]string{}
	for i, c := range []*client{with, without} {
		resp := c.call(2+i, "session.subscribe", map[string]any{"session_id": fakeSessionID})
		var result struct {
			Snapshot struct {
				DataB64 string `json:"data_b64"`
			} `json:"snapshot"`
		}
		decodeResult(t, resp, &result)
		data, err := base64.StdEncoding.DecodeString(result.Snapshot.DataB64)
		if err != nil {
			t.Fatal(err)
		}
		snapshots[c] = string(data)
	}
	if want := "before " + image + " shown"; snapshots[with] != want {
		t.Errorf("the graphics client's snapshot = %q, want it whole", snapshots[with])
	}
	if want := "before " + sessdomain.GraphicsPlaceholder + " shown"; snapshots[without] != want {
		t.Errorf("the other client's snapshot = %q, want %q", snapshots[without], want)
	}

	// The image arrives cut in the middle of an escape, across two chunks. Then a chunk that is
	// only a transmission leaves nothing for the other client, so it sends no notification;
	// the next chunk arrives with its own, higher seq.
	live := "ls\r\n" + image + "\r\n$ "
	cut := strings.Index(live, "iVBOR")
	const transmit = "\x1b_Gf=100,i=7;iVBORw0K\x1b\\"
	s.dispatchTestOutput(fakeSessionID, 2, []byte(live[:cut]))
	s.dispatchTestOutput(fakeSessionID, 3, []byte(live[cut:]))
	// Let the writer take seq 4 as a batch of its own rather than coalesce it with its
	// neighbours; were it coalesced, the check below would only be weaker.
	time.Sleep(200 * time.Millisecond)
	s.dispatchTestOutput(fakeSessionID, 4, []byte(transmit))
	time.Sleep(200 * time.Millisecond)
	s.dispatchTestOutput(fakeSessionID, 5, []byte("ok"))
	if got, want := outputText(t, with, len(live+transmit+"ok")), live+transmit+"ok"; got != want {
		t.Errorf("the graphics client got %q, want %q", got, want)
	}
	want := "ls\r\n" + sessdomain.GraphicsPlaceholder + "\r\n$ ok"
	got := without.collectNotificationsUntil("session.output", 5*time.Second, func(got []map[string]any) bool {
		return len(got) > 0 && got[len(got)-1]["seq"] == float64(5)
	})
	var text strings.Builder
	for _, p := range got {
		if p["data_b64"] == "" {
			t.Errorf("a chunk of graphics only reached the other client: %v", p)
		}
		data, err := base64.StdEncoding.DecodeString(p["data_b64"].(string))
		if err != nil {
			t.Fatal(err)
		}
		text.Write(data)
	}
	if text.String() != want {
		t.Errorf("the other client got %q, want %q", text.String(), want)
	}
}

// TestTheRelayAsksAHostForWhatItsClientAsked_REQ_PLG_003: a host's panes reach a client
// through this daemon's relay, so the relay's hello carries the client's `graphics` and the
// host filters exactly as this daemon would; a client without it asks for none.
func TestTheRelayAsksAHostForWhatItsClientAsked_REQ_PLG_003(t *testing.T) {
	t.Parallel()
	for _, graphics := range []bool{true, false} {
		ours, theirs := net.Pipe()
		t.Cleanup(func() { _ = ours.Close(); _ = theirs.Close() })
		l := &relayLink{conn: &conn{clientKind: ClientTUI, graphics: graphics}, nc: ours}
		got := make(chan helloParams, 1)
		go func() {
			r := bufio.NewReader(theirs)
			line, err := r.ReadBytes('\n')
			if err != nil {
				close(got)
				return
			}
			var req struct {
				Params helloParams `json:"params"`
			}
			_ = json.Unmarshal(line, &req)
			got <- req.Params
			_, _ = theirs.Write([]byte(`{"jsonrpc":"2.0","id":0,"result":{}}` + "\n"))
		}()
		if err := l.hello(t.Context(), "tok", bufio.NewReader(ours)); err != nil {
			t.Fatalf("hello: %v", err)
		}
		p := <-got
		if has := slices.Contains(p.Capabilities, "graphics"); has != graphics {
			t.Errorf("a client with graphics=%v: the relay asked the host for %v", graphics, p.Capabilities)
		}
	}
}
