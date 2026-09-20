package api

import (
	"context"
	"log/slog"

	"github.com/ecrespo/umbral/internal/bus"
	sessports "github.com/ecrespo/umbral/internal/sessions/ports"
	wsports "github.com/ecrespo/umbral/internal/workspaces/ports"
)

// fieldDataB64 is how every binary payload crosses the wire (API Spec §3). It survives the
// move to typed payloads because it also names a field in a validation error, where the
// string is the value rather than a struct tag.
const fieldDataB64 = "data_b64"

// dispatchBuffer is how many events the daemon's single dispatch goroutine may fall behind
// before the bus starts dropping. Output chunks are at most 32 KiB, so this bounds the
// dispatch backlog at a few tens of megabytes in the worst case while giving the writer
// goroutines room to drain.
const dispatchBuffer = 2048

// dispatchedKinds is every bus event that has a wire form (API Spec §6).
//
// It is a variable rather than an argument list inside Notify so a test can check it
// against the kinds the modules publish. A kind with a `toNotification` case but no entry
// here is invisible: the translation exists, the event never arrives, and the only symptom
// is a client whose tree slowly drifts from the daemon's.
var dispatchedKinds = []bus.Kind{
	sessports.KindSessionOutput,
	sessports.KindSessionExited,
	sessports.KindSessionResized,
	sessports.KindSessionInputOwner,
	sessports.KindSessionIntegration,
	sessports.KindBlockStarted,
	sessports.KindBlockUpdated,
	sessports.KindBlockClosed,
	wsports.KindWorkspaceCreated,
	wsports.KindWorkspaceUpdated,
	wsports.KindWorkspaceClosed,
	wsports.KindWorkspaceFocused,
	wsports.KindTabCreated,
	wsports.KindTabClosed,
	wsports.KindTabFocused,
	wsports.KindPaneCreated,
	wsports.KindPaneUpdated,
	wsports.KindPaneClosed,
	wsports.KindPaneFocused,
	wsports.KindPaneMoved,
	wsports.KindLayoutUpdated,
}

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
	sub := s.cfg.Bus.SubscribeBuffered(dispatchBuffer, dispatchedKinds...)
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
			s.broadcast(method, params, s.nextSeq())
		}
	}
}

// toNotification converts a bus event into its wire method and payload. An event with no
// wire form yields an empty method and is skipped, which is what keeps session.output out
// of this path until T-F0-06 gives it somewhere to go.
func toNotification(event bus.Event) (string, any) {
	switch e := event.(type) {
	case sessports.SessionExited:
		return "session.exited", sessionExitedPayload{
			SessionID: e.SessionID, ExitCode: e.ExitCode, ExitedAt: e.ExitedAtMs,
		}
	case sessports.SessionResized:
		return "session.resized", sessionResizedPayload{
			SessionID: e.SessionID, Cols: e.Size.Cols, Rows: e.Size.Rows,
		}
	case sessports.SessionInputOwner:
		return "session.input_owner", sessionInputOwnerPayload{
			SessionID: e.SessionID, InputOwner: string(e.InputOwner),
		}
	case sessports.SessionIntegration:
		return "session.integration", sessionIntegrationPayload{
			SessionID: e.SessionID, Integration: string(e.Integration),
		}
	case sessports.BlockStarted:
		return "block.started", toWireBlock(e.Block)
	case sessports.BlockUpdated:
		return "block.updated", blockUpdatedPayload{
			BlockID: e.BlockID, State: string(e.State),
		}
	case sessports.BlockClosed:
		return "block.closed", toWireBlock(e.Block)

	// The workspace tree's kinds are already the §6 method names, so the event carries its
	// own method and only the payload has to be built.
	// §6 types these payloads as the object itself — `Workspace`, `Tab`, `Pane` — exactly
	// as `block.started` carries a `Block`. An envelope would leave a client written
	// against §6 looking for an `id` field that is one level down.
	case wsports.WorkspaceEvent:
		return string(e.Kind), toWireWorkspace(e.Workspace)
	case wsports.TabEvent:
		return string(e.Kind), toWireTab(e.Tab)
	case wsports.PaneEvent:
		return string(e.Kind), toWirePane(e.Pane)
	case wsports.LayoutUpdated:
		// §6 types this payload as a `Layout`, like the three above it.
		return "layout.updated", toWireLayout(e.Layout)
	case wsports.PaneMoved:
		// REQ-WS-007's payload, and the reason this one is not a PaneEvent: §6 gives it
		// the previous identifiers so a client can follow the terminal across the move
		// instead of seeing an unrelated pane appear.
		return "pane.moved", paneMovedPayload{
			Pane:                toWirePane(e.Pane),
			PreviousPaneID:      e.PreviousPaneID,
			PreviousWorkspaceID: e.PreviousWorkspaceID,
			Layout:              toWireLayout(e.Layout),
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

	// One envelope number per bus event, assigned before the fan-out so every connection
	// sees the same one. Assigning it per connection would break §6's "shared by all
	// subscribers" without anything failing.
	envelope := s.nextSeq()
	for _, c := range conns {
		c.deliver(output.SessionID, output.Seq, envelope, output.Data)
	}
}

// broadcast sends a notification to every connection that completed the handshake.
func (s *Server) broadcast(method string, params any, seq uint64) {
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
		c.notify(method, params, seq)
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
