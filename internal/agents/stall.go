package agents

import (
	"context"
	"time"

	"github.com/ecrespo/umbral/internal/agents/domain"
	"github.com/ecrespo/umbral/internal/agents/ports"
	"github.com/ecrespo/umbral/internal/bus"
)

// The stall windows of REQ-AUT-008 and how often running turns are checked against them
// (delta `2026-10-wait-monitoring`, decisions 6 and 8).
const (
	DefaultStallLocal  = 120 * time.Second
	DefaultStallRemote = 30 * time.Second
	DefaultStallTick   = time.Second
)

// The activity a stall reports as the last one seen (API §6 `thread.stalled`).
const (
	activityTurnStarted = "turn_started"
	activityToolCall    = "tool_call"
	activityToolResult  = "tool_result"
	activityOutput      = "output"
	activityApproval    = "approval"
)

// liveness is a running turn's activity clock, guarded by Runtime.mu.
type liveness struct {
	window    time.Duration
	lastAt    time.Time
	lastEvent string
	// awaiting is a turn paused on an approval: it waits for the user and does not stall.
	awaiting bool
	// queued is a turn held for a slot (REQ-ORC-002): it has not started, so it does not stall.
	queued bool
	// providerWait is a turn whose model call waits behind others for its provider
	// (REQ-ORC-005): the wait is the queue's, not the model's, so it does not stall.
	providerWait bool
	stalled      bool
}

// stallWindow is the window a turn of thread gets: the local one when any candidate of its
// model or class runs on this machine, since a local model may take minutes to answer.
func (r *Runtime) stallWindow(ctx context.Context, thread domain.Thread) time.Duration {
	if r.cfg.Models.Local(ctx, thread.ModelClass, thread.Model) {
		return r.cfg.StallLocal
	}
	return r.cfg.StallRemote
}

// observe updates the turn's clock from an event the runtime publishes. The runtime's own
// events are seen here, before the bus, so none is lost to its backpressure.
func (r *Runtime) observe(e bus.Event) {
	switch e := e.(type) {
	case ports.ThreadDelta:
		r.touch(e.ThreadID, e.Kind, false)
	case ports.ThreadToolCall:
		what := activityToolResult
		if e.Call.Status == domain.ToolPending {
			what = activityToolCall
		}
		r.touch(e.Call.ThreadID, what, false)
	case ports.ApprovalRequested:
		r.mu.Lock()
		if t := r.turns[e.Approval.ThreadID]; t != nil {
			t.live.awaiting, t.live.stalled = true, false
		}
		r.mu.Unlock()
	case ports.ThreadAttentionChanged:
		// `working` again while awaiting is the approval answered: the clock restarts.
		if e.AttentionState == "working" {
			r.touch(e.ThreadID, activityApproval, true)
		}
	}
}

// touch records activity on the thread's running turn and clears a stall. onlyAwaiting
// touches only a turn paused on an approval. Only the answer ends the pause: output from
// the pane while the user decides moves the clock but leaves the turn awaiting.
func (r *Runtime) touch(threadID, what string, onlyAwaiting bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.turns[threadID]
	if t == nil || (onlyAwaiting && !t.live.awaiting) {
		return
	}
	t.live.lastAt, t.live.lastEvent, t.live.stalled = r.cfg.Now(), what, false
	if what == activityApproval {
		t.live.awaiting = false
	}
}

// waitForProvider marks the turn's model call as waiting for its provider, or no longer; the
// stall clock starts again when the wait ends.
func (r *Runtime) waitForProvider(threadID, turnID string, waiting bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.turns[threadID]
	if t == nil || t.id != turnID {
		return
	}
	t.live.providerWait = waiting
	if waiting {
		t.live.stalled = false
	} else {
		t.live.lastAt = r.cfg.Now()
	}
}

// NoteOutput records output from a session the thread owns: activity for its running turn.
// `cmd/umbrald` calls it, following `session.output` for the thread's PTY.
func (r *Runtime) NoteOutput(threadID string) { r.touch(threadID, activityOutput, false) }

// Stalled reports whether the thread's running turn is marked stalled.
func (r *Runtime) Stalled(threadID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.turns[threadID]
	return t != nil && t.live.stalled
}

// watchStalls checks the running turns every tick until the runtime closes. A turn idle for
// its window is marked, announced once and counted; it is never cancelled (DD-017).
func (r *Runtime) watchStalls() {
	defer r.wg.Done()
	tick := time.NewTicker(r.cfg.StallTick)
	defer tick.Stop()
	for {
		select {
		case <-r.base.Done():
			return
		case <-tick.C:
			for _, s := range r.newStalls() {
				// A turn that ended, or woke up, since it was marked is not announced.
				if !r.Stalled(s.ThreadID) {
					continue
				}
				r.cfg.Bus.Publish(s)
				r.cfg.Metrics.WaitStalled()
			}
		}
	}
}

func (r *Runtime) newStalls() []ports.ThreadStalled {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.cfg.Now()
	var out []ports.ThreadStalled
	for threadID, t := range r.turns {
		l := &t.live
		idle := now.Sub(l.lastAt)
		if l.stalled || l.awaiting || l.queued || l.providerWait || idle < l.window {
			continue
		}
		l.stalled = true
		out = append(out, ports.ThreadStalled{ThreadID: threadID, TurnID: t.id, IdleMS: idle.Milliseconds(), LastEvent: l.lastEvent})
	}
	return out
}

// withStall sets the thread's Stalled from the running turn.
func (r *Runtime) withStall(t domain.Thread) domain.Thread {
	t.Stalled = r.Stalled(t.ID)
	return t
}
