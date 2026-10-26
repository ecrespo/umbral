package api

import (
	"context"
	"log/slog"

	aaiports "github.com/ecrespo/umbral/internal/activeai/ports"

	intports "github.com/ecrespo/umbral/internal/integrations/ports"

	agentsports "github.com/ecrespo/umbral/internal/agents/ports"
	ctxports "github.com/ecrespo/umbral/internal/context/ports"
	mcpports "github.com/ecrespo/umbral/internal/mcp/ports"
	secports "github.com/ecrespo/umbral/internal/security/ports"

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
	aaiports.KindBlockSuggestion,
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
	agentsports.KindThreadDelta,
	agentsports.KindThreadToolCall,
	agentsports.KindThreadTurnFinished,
	agentsports.KindContextCompacted,
	agentsports.KindApprovalRequested,
	agentsports.KindAttentionChanged,
	agentsports.KindThreadStalled,
	ctxports.KindSkillChanged,
	mcpports.KindServerState,
	intports.KindPaneStateChanged,
	secports.KindRuleUpdateRejected,
}

// Notify forwards module events to connected clients as JSON-RPC notifications
// (API Spec §6). Call it exactly once per server: it drains the one subscription `Listen`
// took, and two callers would split the events between them. It returns when ctx is
// cancelled or when `Close` closes that subscription.
//
// This is the simple form: every authenticated connection receives every event. The
// per-session subscription that `session.subscribe` implies, the 4 ms / 32 KiB batching
// and the 8 MiB per-client queue all belong to T-F0-06. What is here already holds the
// property that matters most: it never blocks the publisher, because the bus drops for a
// subscriber that falls behind rather than waiting for it.
func (s *Server) Notify(ctx context.Context) {
	// Taken by `Listen`, not here: see Server.events. Its buffer is generous because this
	// is the daemon's single dispatch goroutine, and an event it drops is output no client
	// will ever see. The per-client budget that API Spec §8 actually specifies lives in the
	// subscription, where it can drop one slow client rather than everyone.
	sub := s.events
	defer sub.Close()

	for {
		select {
		case <-ctx.Done():
			s.logDrops(sub)
			return
		case event, open := <-sub.C():
			if !open {
				s.logDrops(sub)
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
	case aaiports.BlockSuggestion:
		return "block.suggestion", blockSuggestionPayload{BlockID: e.BlockID, SessionID: e.SessionID, Text: e.Text, Model: e.Model}

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

	case agentsports.ThreadDelta:
		return "thread.delta", threadDeltaPayload{ThreadID: e.ThreadID, TurnID: e.TurnID, Kind: e.Kind, Text: e.Text}
	case agentsports.ThreadToolCall:
		return "thread.tool_call", toWireToolCall(e.Call)
	case agentsports.TurnFinished:
		return "thread.turn_finished", turnFinishedPayload{
			ThreadID: e.ThreadID, TurnID: e.TurnID, StopReason: string(e.StopReason),
			Usage: turnUsage{InTokens: e.Usage.InTokens, OutTokens: e.Usage.OutTokens, CostMicroUSD: e.Usage.CostMicroUSD},
		}
	case agentsports.ApprovalRequested:
		return "approval.requested", toWireApproval(e.Approval)
	case agentsports.ThreadAttentionChanged:
		return "thread.attention_changed", attentionChangedPayload{ThreadID: e.ThreadID, AttentionState: e.AttentionState}
	case ctxports.SkillChanged:
		return "skill.changed", skillChangedPayload{Name: e.Name, Change: e.Change}
	case agentsports.ThreadStalled:
		return "thread.stalled", threadStalledPayload{ThreadID: e.ThreadID, TurnID: e.TurnID, IdleMS: e.IdleMS, LastEvent: e.LastEvent}
	case agentsports.ContextCompacted:
		return "context.compacted", contextCompactedPayload{ThreadID: e.ThreadID, BeforeTokens: e.BeforeTokens, AfterTokens: e.AfterTokens}
	case mcpports.ServerState:
		return "mcp.server_state", mcpServerStatePayload{Name: e.Name, State: string(e.State), LastError: emptyAsNull(e.LastError)}
	case intports.PaneStateChanged:
		return "pane.state_changed", paneStateChangedPayload{
			PaneID: e.PaneID, AttentionState: string(e.AttentionState), StateSource: emptyAsNull(e.StateSource),
		}
	case secports.RuleUpdateRejected:
		return "rules.update_rejected", ruleUpdateRejectedPayload{Reason: string(e.Reason), Version: e.Version, Source: e.Source}

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

// broadcast queues a notification for every connection that completed the handshake. It
// never writes a socket itself: each connection's writer does, so a client that stops reading
// holds up only itself, and is closed once it is a queue limit behind (REQ-API-006).
func (s *Server) broadcast(method string, params any, seq uint64) {
	s.mu.Lock()
	conns := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()

	for _, c := range conns {
		if !c.notifiable.Load() {
			continue
		}
		c.enqueue(method, params, seq)
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
