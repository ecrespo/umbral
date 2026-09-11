package api

import (
	"context"
	"log/slog"

	"github.com/ecrespo/umbral/internal/bus"
	sessports "github.com/ecrespo/umbral/internal/sessions/ports"
)

// fieldSessionID is the parameter name every session notification carries (API Spec §6).
const (
	fieldSessionID = "session_id"
	// fieldDataB64 is how every binary payload crosses the wire (API Spec §3).
	fieldDataB64 = "data_b64"
)

// dispatchBuffer is how many events the daemon's single dispatch goroutine may fall behind
// before the bus starts dropping. Output chunks are at most 32 KiB, so this bounds the
// dispatch backlog at a few tens of megabytes in the worst case while giving the writer
// goroutines room to drain.
const dispatchBuffer = 2048

// Notify forwards module events to connected clients as JSON-RPC notifications
// (API Spec §6). It returns when ctx is cancelled.
//
// This is the simple form: every authenticated connection receives every event. The
// per-session subscription that `session.subscribe` implies, the 4 ms / 32 KiB batching
// and the 8 MiB per-client queue all belong to T-F0-06. What is here already holds the
// property that matters most: it never blocks the publisher, because the bus drops for a
// subscriber that falls behind rather than waiting for it.
func (s *Server) Notify(ctx context.Context) {
	// A generous buffer: this is the daemon's single dispatch goroutine, and an event it
	// drops here is output no client will ever see. The per-client budget that API Spec §8
	// actually specifies lives in the subscription, where it can drop one slow client
	// rather than everyone.
	sub := s.cfg.Bus.SubscribeBuffered(dispatchBuffer,
		sessports.KindSessionOutput,
		sessports.KindSessionExited,
		sessports.KindSessionResized,
		sessports.KindSessionInputOwner,
	)
	defer sub.Close()

	for {
		select {
		case <-ctx.Done():
			s.logDrops(sub)
			return
		case event, open := <-sub.C():
			if !open {
				return
			}
			if output, ok := event.(sessports.SessionOutput); ok {
				s.dispatchOutput(output)
				continue
			}
			method, params := toNotification(event)
			if method == "" {
				continue
			}
			s.broadcast(method, params)
		}
	}
}

// toNotification converts a bus event into its wire method and payload. An event with no
// wire form yields an empty method and is skipped, which is what keeps session.output out
// of this path until T-F0-06 gives it somewhere to go.
func toNotification(event bus.Event) (string, any) {
	switch e := event.(type) {
	case sessports.SessionExited:
		return "session.exited", map[string]any{
			fieldSessionID: e.SessionID, "exit_code": e.ExitCode, "exited_at": e.ExitedAtMs,
		}
	case sessports.SessionResized:
		return "session.resized", map[string]any{
			fieldSessionID: e.SessionID, "cols": e.Size.Cols, "rows": e.Size.Rows,
		}
	case sessports.SessionInputOwner:
		return "session.input_owner", map[string]any{
			fieldSessionID: e.SessionID, "input_owner": string(e.InputOwner),
		}
	default:
		return "", nil
	}
}

// dispatchOutput hands a chunk to every connection subscribed to that session.
//
// Output goes only to subscribers, unlike the control notifications above: a client that
// did not ask for a session's bytes has no use for them, and sending them anyway would put
// every session's traffic through every connection's queue.
func (s *Server) dispatchOutput(output sessports.SessionOutput) {
	s.mu.Lock()
	conns := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()

	for _, c := range conns {
		c.deliver(output.SessionID, output.Seq, output.Data)
	}
}

// broadcast sends a notification to every connection that completed the handshake.
func (s *Server) broadcast(method string, params any) {
	s.mu.Lock()
	conns := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()

	for _, c := range conns {
		if !c.authenticated {
			continue
		}
		c.notify(method, params)
	}
}

// logDrops reports events lost because a subscriber fell behind. A silent drop counter is
// no better than no counter.
func (s *Server) logDrops(sub *bus.Subscription) {
	if dropped := sub.Dropped(); dropped > 0 {
		s.cfg.Logger.Warn("notifications were dropped while shutting down",
			slog.Int64("dropped", dropped))
	}
}
