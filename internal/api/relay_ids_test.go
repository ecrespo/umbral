package api

import (
	"encoding/json"
	"errors"
	"testing"

	remotedomain "github.com/ecrespo/umbral/internal/remote/domain"
)

func knownHosts(names ...string) func(string) bool {
	return func(n string) bool {
		for _, k := range names {
			if k == n {
				return true
			}
		}
		return false
	}
}

func sameJSON(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatalf("got %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatal(err)
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Fatalf("got %s, want %s", ja, jb)
	}
}

func TestARequestWithAHostsIDsIsRelayedToIt_REQ_RMT_003(t *testing.T) {
	known := knownHosts("nas", "box")

	host, out, routed, err := routeParams(json.RawMessage(`{"pane_id":"nas/w1:p2","direction":"right","size":0.5}`), known)
	if err != nil || !routed || host != "nas" {
		t.Fatalf("route = %q, %v, %v", host, routed, err)
	}
	sameJSON(t, out, `{"pane_id":"w1:p2","direction":"right","size":0.5}`)

	// A `host` member routes a request that carries none of the host's ids, and is taken off.
	host, out, routed, err = routeParams(json.RawMessage(`{"host":"box","title":"t","workspace":"box/w3"}`), known)
	if err != nil || !routed || host != "box" {
		t.Fatalf("route by host = %q, %v, %v", host, routed, err)
	}
	sameJSON(t, out, `{"title":"t","workspace":"w3"}`)

	// Lists and nested members are ids too; the connection's and the catalog's are not.
	host, out, _, err = routeParams(json.RawMessage(`{"hunk_ids":["nas/h1","nas/h2"],"wait":{"session_id":"nas/ses_1"},"client_msg_id":"nas/c","model_id":"nas/m"}`), known)
	if err != nil || host != "nas" {
		t.Fatalf("route lists = %q, %v", host, err)
	}
	sameJSON(t, out, `{"hunk_ids":["h1","h2"],"wait":{"session_id":"ses_1"},"client_msg_id":"nas/c","model_id":"nas/m"}`)

	// This daemon's own ids, and a prefix that names no host, stay here.
	for _, raw := range []string{`{"pane_id":"w1:p2"}`, `{"pane_id":"ghost/w1"}`, `{}`, `[]`, ``} {
		if _, _, routed, err := routeParams(json.RawMessage(raw), known); routed || err != nil {
			t.Errorf("%s routed=%v err=%v, want local", raw, routed, err)
		}
	}

	if _, _, _, err := routeParams(json.RawMessage(`{"pane_id":"nas/w1","target_id":"box/w2"}`), known); err == nil || ErrorDetails(err) == nil {
		t.Errorf("two hosts' ids = %v, want VALIDATION_ERROR", err)
	}
	if _, _, _, err := routeParams(json.RawMessage(`{"host":"nas","pane_id":"box/w2"}`), known); err == nil {
		t.Errorf("host and another host's id = %v, want VALIDATION_ERROR", err)
	}
	if _, _, _, err := routeParams(json.RawMessage(`{"host":"ghost"}`), known); !errors.Is(err, remotedomain.ErrNotFound) {
		t.Errorf("an unknown host = %v, want its NOT_FOUND", err)
	}
	if _, _, _, err := routeParams(json.RawMessage(`{"host":7}`), known); ErrorDetails(err) == nil {
		t.Errorf("a host that is not a name = %v, want VALIDATION_ERROR", err)
	}
}

func TestAHostsAnswersCarryItsName_REQ_RMT_001(t *testing.T) {
	out, err := prefixIDs(json.RawMessage(`{"workspace":{"id":"w1","label":"api","tab_ids":["w1:t1"],"order_index":0},
		"tabs":[{"id":"w1:t1","workspace_id":"w1","pane_ids":["w1:p1"]}],
		"pane":{"id":"w1:p1","session_id":"ses_9","label":"shell","trace_id":"x","size":1.5}}`), "nas")
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, out, `{"workspace":{"id":"nas/w1","label":"nas: api","tab_ids":["nas/w1:t1"],"order_index":0},
		"tabs":[{"id":"nas/w1:t1","workspace_id":"nas/w1","pane_ids":["nas/w1:p1"]}],
		"pane":{"id":"nas/w1:p1","session_id":"nas/ses_9","label":"shell","trace_id":"x","size":1.5}}`)

	// What the client sends back with those ids reaches the host as the host wrote them.
	host, back, routed, err := routeParams(json.RawMessage(`{"pane_id":"nas/w1:p1","session_id":"nas/ses_9"}`), knownHosts("nas"))
	if err != nil || !routed || host != "nas" {
		t.Fatal(host, routed, err)
	}
	sameJSON(t, back, `{"pane_id":"w1:p1","session_id":"ses_9"}`)
}

func TestAHostsIDBesideALocalOneIsRefused_REQ_RMT_003(t *testing.T) {
	known := knownHosts("nas")
	// w2:t1 names a tab on every daemon: beside nas's pane it would reach nas's tab, not the
	// local one the client holds.
	if _, _, _, err := routeParams(json.RawMessage(`{"pane_id":"nas/w1:p1","tab_id":"w2:t1"}`), known); ErrorDetails(err) == nil {
		t.Errorf("a host's id beside a local one = %v, want VALIDATION_ERROR", err)
	}
	// Under a `host` member every id is the host's, as written.
	host, out, routed, err := routeParams(json.RawMessage(`{"host":"nas","pane_id":"w1:p1"}`), known)
	if err != nil || !routed || host != "nas" {
		t.Fatalf("route by host = %q, %v, %v", host, routed, err)
	}
	sameJSON(t, out, `{"pane_id":"w1:p1"}`)
	// A `host` that cannot be a name is VALIDATION_ERROR, not NOT_FOUND.
	if _, _, _, err := routeParams(json.RawMessage(`{"host":"NAS!"}`), known); ErrorDetails(err) == nil {
		t.Errorf("a host that is not a name = %v, want VALIDATION_ERROR", err)
	}
}

func TestAHostsOwnLinkAnnouncementsAreNotRelayed_REQ_RMT_001(t *testing.T) {
	for method, want := range map[string]bool{
		"session.output": true, "pane.updated": true, "thread.updated": true,
		"remote.state_changed": false, "system.anything": false,
	} {
		if got := relayableNotification(method); got != want {
			t.Errorf("relayableNotification(%s) = %v, want %v", method, got, want)
		}
	}
}
