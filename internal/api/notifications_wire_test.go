package api

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	ntfdomain "github.com/ecrespo/umbral/internal/notify/domain"
)

// fakeNotifier stands in for the notify module: it builds the request as the module does and
// hands it straight to the server, so what the test sees is api's half — the method, the
// foreground client and the wire form.
type fakeNotifier struct {
	server   *Server
	requests []ntfdomain.Request
}

func (f *fakeNotifier) Show(_ context.Context, req ntfdomain.Request) (ntfdomain.Result, error) {
	f.requests = append(f.requests, req)
	n, err := req.Build()
	if err != nil {
		return ntfdomain.Result{}, err
	}
	if !f.server.DeliverForeground(n) {
		return ntfdomain.Result{Reason: ntfdomain.ReasonNoClient}, nil
	}
	return ntfdomain.Result{Shown: true, Reason: ntfdomain.ReasonShown}, nil
}

type notificationPosted struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	Source string `json:"source"`
	Sound  string `json:"sound"`
}

// TestNotificationShowReachesTheForegroundClient_REQ_NTF_001: `notification.show` is open
// to `umb`, answers §5.35's `{shown, reason}`, and delivers `notification.posted` to the
// foreground client only — the interactive connection that last sent a request — and to no
// CLI. With no interactive client it answers `no_client`; a bad request is VALIDATION_ERROR.
func TestNotificationShowReachesTheForegroundClient_REQ_NTF_001(t *testing.T) {
	t.Parallel()
	notifier := &fakeNotifier{}
	s := testServerWithConfig(t, Config{Notifications: notifier})
	notifier.server = s

	cli := dial(t, s)
	hello := cli.hello(s.Token(), ClientCLI)
	if hello.Error != nil {
		t.Fatalf("handshake: %+v", hello.Error)
	}
	if !slices.Contains(capabilitiesOf(t, hello.Result), "notifications") {
		t.Errorf("notifications is not advertised: %v", capabilitiesOf(t, hello.Result))
	}

	show := func(id int, params map[string]any) response {
		t.Helper()
		return cli.call(id, "notification.show", params)
	}

	// Only the CLI is connected: nobody to show it to.
	resp := show(2, map[string]any{"title": "build done", "source": "ci-hook"})
	if resp.Error != nil || string(ordered[showNotificationResult](t, resp.Result)) != `{"shown":false,"reason":"no_client"}` {
		t.Fatalf("no interactive client: %s %+v", resp.Result, resp.Error)
	}

	first := dial(t, s)
	if r := first.hello(s.Token(), ClientTUI); r.Error != nil {
		t.Fatalf("first tui: %+v", r.Error)
	}
	second := dial(t, s)
	if r := second.hello(s.Token(), ClientDesktop); r.Error != nil {
		t.Fatalf("second client: %+v", r.Error)
	}
	// The first TUI speaks last, so it is the foreground client.
	if r := first.call(2, "system.status", map[string]any{}); r.Error != nil {
		t.Fatalf("status: %+v", r.Error)
	}

	resp = show(3, map[string]any{"title": "build\tdone", "body": "all green", "source": "ci-hook", "sound": "done"})
	if resp.Error != nil || string(ordered[showNotificationResult](t, resp.Result)) != `{"shown":true,"reason":"shown"}` {
		t.Fatalf("show = %s %+v", resp.Result, resp.Error)
	}
	method, seq, params := first.readNotification(t)
	if method != "notification.posted" || seq == 0 {
		t.Fatalf("foreground client got %s seq %d", method, seq)
	}
	var got notificationPosted
	if err := json.Unmarshal(params, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != (notificationPosted{Title: "build done", Body: "all green", Source: "ci-hook", Sound: "done"}) {
		t.Errorf("posted %+v", got)
	}

	// The other client speaks; it becomes the foreground one and the first gets nothing.
	if r := second.call(2, "system.status", map[string]any{}); r.Error != nil {
		t.Fatalf("status: %+v", r.Error)
	}
	if resp := show(4, map[string]any{"title": "second", "source": "ci-hook"}); resp.Error != nil {
		t.Fatalf("show: %+v", resp.Error)
	}
	if method, _, params := second.readNotification(t); method != "notification.posted" || !json.Valid(params) {
		t.Fatalf("the new foreground client got %s", method)
	}
	// The first client's next message is the answer to its own call, not a notification.
	if r := first.call(3, "system.status", map[string]any{}); r.Error != nil || r.ID == nil {
		t.Fatalf("the background client received something before its own reply: %+v", r)
	}

	for name, params := range map[string]map[string]any{
		"empty title":     {"title": " ", "source": "ci-hook"},
		"reserved source": {"title": "t", "source": "umbral:agent"},
		"bad sound":       {"title": "t", "source": "ci-hook", "sound": "bell"},
		"no source":       {"title": "t"},
	} {
		resp := show(10, params)
		if resp.Error == nil || resp.Error.Data == nil || resp.Error.Data.DomainCode != domainValidationError {
			t.Errorf("%s: %+v, want VALIDATION_ERROR", name, resp.Error)
		}
	}
}

// TestNotificationShowIsNotImplementedWithoutTheModule: a daemon built without the sink
// answers NOT_IMPLEMENTED and does not advertise `notifications`.
func TestNotificationShowIsNotImplementedWithoutTheModule(t *testing.T) {
	t.Parallel()
	s := testServerWithConfig(t, Config{})
	c := dial(t, s)
	hello := c.hello(s.Token(), ClientCLI)
	if slices.Contains(capabilitiesOf(t, hello.Result), "notifications") {
		t.Errorf("notifications advertised without the module")
	}
	resp := c.call(2, "notification.show", map[string]any{"title": "t", "source": "x"})
	if resp.Error == nil || resp.Error.Data == nil || resp.Error.Data.DomainCode != "NOT_IMPLEMENTED" {
		t.Errorf("without the module: %+v", resp.Error)
	}
}

// TestAStalledForegroundClientDoesNotBlockTheSink_REQ_NTF_002: a foreground TUI that stops
// reading must not hold up whoever asked for a notification. Delivery goes through a small
// queue per connection; once it is full the client is treated as absent.
func TestAStalledForegroundClientDoesNotBlockTheSink_REQ_NTF_002(t *testing.T) {
	t.Parallel()
	s := testServerWithConfig(t, Config{Notifications: &fakeNotifier{}})
	tui := dial(t, s)
	if r := tui.hello(s.Token(), ClientTUI); r.Error != nil {
		t.Fatalf("hello: %+v", r.Error)
	}
	if r := tui.call(2, "system.status", map[string]any{}); r.Error != nil {
		t.Fatalf("status: %+v", r.Error)
	}
	// The TUI reads nothing from here on.
	body := strings.Repeat("x", ntfdomain.MaxBodyLen)
	done := make(chan int, 1)
	go func() {
		refused := 0
		for range 5000 {
			if !s.DeliverForeground(ntfdomain.Notification{Title: "t", Body: body, Source: "s", Sound: ntfdomain.SoundNone}) {
				refused++
			}
		}
		done <- refused
	}()
	select {
	case refused := <-done:
		if refused == 0 {
			t.Error("5000 notifications to a client that reads nothing were all accepted")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("delivering to a stalled client blocked")
	}
}
