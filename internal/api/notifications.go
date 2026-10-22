package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	ntfdomain "github.com/ecrespo/umbral/internal/notify/domain"
)

// API Spec §5.35: a component asks for a notification, the notify module normalizes and
// rate-limits it, and the daemon hands it to the foreground client as `notification.posted`
// (§6). The foreground client is the interactive connection that last sent a request — the
// one the user is most likely looking at — and only it is told, so a user with two TUIs
// open is not notified twice (delta `2026-10-notifications`).

const capabilityNotifications = "notifications"

func hasNotifications(cfg Config) bool { return cfg.Notifications != nil }

// notificationMethods carries no kinds: a hook reports through `umb`, which is a CLI.
func notificationMethods() map[string]method {
	return map[string]method{
		"notification.show": {
			handle: handleNotificationShow, params: showNotificationParams{}, result: showNotificationResult{},
		},
	}
}

type showNotificationParams struct {
	Title  string `json:"title"`
	Body   string `json:"body" api:"optional"`
	Source string `json:"source"`
	Sound  string `json:"sound" api:"optional"`
}

type showNotificationResult struct {
	Shown  bool   `json:"shown"`
	Reason string `json:"reason"`
}

// notificationPostedPayload is §6's `notification.posted`.
type notificationPostedPayload struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	Source string `json:"source"`
	Sound  string `json:"sound"`
}

func handleNotificationShow(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc := c.server.cfg.Notifications
	if svc == nil {
		return nil, fmt.Errorf("%w: the notification sink is not wired in", ErrNotImplemented)
	}
	var p showNotificationParams
	if err := decode(raw, &p, "notification.show"); err != nil {
		return nil, err
	}
	res, err := svc.Show(ctx, ntfdomain.Request{
		Title: p.Title, Body: p.Body, Source: p.Source, Sound: ntfdomain.Sound(p.Sound),
	})
	if err != nil {
		return nil, err
	}
	return showNotificationResult{Shown: res.Shown, Reason: string(res.Reason)}, nil
}

// notifyDomainError maps the sink's one sentinel onto §3.
func notifyDomainError(err error) (int, string, bool) {
	if errors.Is(err, ntfdomain.ErrValidation) {
		return codeValidationError, domainValidationError, true
	}
	return 0, "", false
}

// DeliverForeground implements the notify module's Deliverer: it sends the notification to
// the authenticated interactive connection with the most recent request, and reports false
// when there is none.
func (s *Server) DeliverForeground(n ntfdomain.Notification) bool {
	s.mu.Lock()
	var target *conn
	var latest uint64
	for c := range s.conns {
		if at := c.lastRequest.Load(); at > latest && c.interactive.Load() {
			target, latest = c, at
		}
	}
	s.mu.Unlock()
	if target == nil {
		return false
	}
	select {
	case target.posted <- postedNotification{seq: s.nextSeq(), payload: notificationPostedPayload{
		Title: n.Title, Body: n.Body, Source: n.Source, Sound: string(n.Sound),
	}}:
		return true
	default:
		// The client has not read the last postedQueue notifications: it is not there for
		// the user in any sense that matters, and waiting on it would stall the sink.
		s.cfg.Logger.Warn("the foreground client is not reading; a notification was not delivered",
			slog.String("connection_id", target.connectionID))
		return false
	}
}

// postedQueue bounds the notifications waiting for one client. The sink already holds each
// source to 5 a minute, so a full queue means a client that stopped reading.
const postedQueue = 16

type postedNotification struct {
	seq     uint64
	payload notificationPostedPayload
}

// writePosted writes this connection's queued notifications until it closes.
func (c *conn) writePosted() {
	for {
		select {
		case <-c.closed:
			return
		case p := <-c.posted:
			c.notify("notification.posted", p.payload, p.seq)
		}
	}
}

func isInteractive(kind ClientKind) bool {
	return kind == ClientTUI || kind == ClientDesktop
}
