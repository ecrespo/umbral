package sdk

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/coder/acp-go-sdk"

	"github.com/ecrespo/umbral/internal/acp/domain"
	"github.com/ecrespo/umbral/internal/acp/ports"
)

// Prompt runs one turn of the thread's agent, launching it first when it is not running
// (REQ-ACP-002, REQ-ACP-006), and returns Umbral's stop reason for it.
//
// The turn ends with domain.ErrAgent — and the process killed, so the next prompt relaunches
// it — when the agent exits, sends a line that is not ACP, or sends nothing for the silence
// timeout while nothing it asked of Umbral is unanswered and no approval of its thread is
// pending. A cancelled ctx kills the commands of the thread's terminals, sends
// `session/cancel`, and kills the process if the turn has not ended KillAfter later
// (REQ-ACP-008).
func (h *Host) Prompt(ctx context.Context, req Request, text string, sink ports.Sink) (string, error) {
	p, err := h.ensure(ctx, req)
	if err != nil {
		return "", err
	}
	p.turn.Lock()
	defer p.turn.Unlock()
	p.client.begin(sink)
	defer p.client.end()
	p.client.touch()
	p.stopKilling()

	type result struct {
		resp acp.PromptResponse
		err  error
	}
	done := make(chan result, 1)
	// The prompt's own context is not the turn's: a cancel is `session/cancel`, which lets the
	// agent end the turn itself, not a dropped request.
	pctx, stop := context.WithCancel(context.WithoutCancel(ctx))
	defer stop()
	go func() {
		resp, err := p.conn.Prompt(pctx, acp.PromptRequest{SessionId: p.session, Prompt: []acp.ContentBlock{acp.TextBlock(text)}})
		done <- result{resp, err}
	}()

	tick := time.NewTicker(h.tick())
	defer tick.Stop()
	cancelled := ctx.Done()
	exited := p.gone
	var killAt <-chan time.Time
	for {
		select {
		case r := <-done:
			if err := p.client.failure(); err != nil {
				h.drop(p)
				return "", err
			}
			if r.err != nil {
				// A connection that ended with the process answers before its exit is seen; the
				// exit is the better reason.
				select {
				case <-p.gone:
				case <-time.After(exitGrace):
				}
				return "", h.agentError(ctx, p, replyError("session/prompt", r.err).Error())
			}
			if ctx.Err() != nil {
				return string(acp.StopReasonCancelled), ctx.Err()
			}
			reason := domain.StopReason(string(r.resp.StopReason))
			if reason == "agent_error" {
				return "", h.agentError(ctx, p, fmt.Sprintf("unknown stop reason %q", r.resp.StopReason))
			}
			return reason, nil
		case <-p.client.failedCh:
			// A write failed: the turn cannot record what the agent does next.
			err := p.client.failure()
			h.drop(p)
			return "", err
		case <-exited:
			// An answer written just before the exit may still be in the pipe: the turn ended
			// before the process did, and is not the agent's error.
			select {
			case r := <-done:
				done <- r
				exited = nil
				continue
			case <-time.After(exitGrace):
			}
			return "", h.agentError(ctx, p, "")
		case <-p.invalid:
			return "", h.agentError(ctx, p, "")
		case <-cancelled:
			cancelled = nil
			// The terminals' commands stop first, within REQ-ACP-008's 500 ms, whatever the agent
			// does with the cancel; their sessions stay the agent's until it releases them.
			p.killTerminals()
			_ = p.conn.Cancel(context.WithoutCancel(ctx), acp.CancelNotification{SessionId: p.session})
			killAt = time.After(h.cfg.KillAfter)
		case <-killAt:
			h.cfg.Logger.WarnContext(ctx, "acp agent killed: the cancelled turn did not end", slog.String("thread", p.thread),
				slog.Duration("after", h.cfg.KillAfter))
			h.drop(p)
			return string(acp.StopReasonCancelled), ctx.Err()
		case <-tick.C:
			if p.client.waiting() || h.cfg.Pending(p.thread) {
				// Waiting on Umbral or on the user is not silence; the window starts again when
				// the wait ends.
				p.client.touch()
				continue
			}
			if killAt == nil && p.client.since() > h.cfg.SilenceTimeout {
				return "", h.agentError(ctx, p, fmt.Sprintf("it sent nothing for %s", h.cfg.SilenceTimeout))
			}
		}
	}
}

// replyError is the agent's error answer to method without the agent's own text: its message
// and data are the agent's to write, and could carry the credentials its env references hold,
// so only the JSON-RPC code reaches a log or a client (Art. 7).
func replyError(method string, err error) error {
	var reqErr *acp.RequestError
	if errors.As(err, &reqErr) {
		return fmt.Errorf("%s: the agent answered error %d", method, reqErr.Code)
	}
	return fmt.Errorf("%s: %w", method, err)
}

// exitGrace is how long an exited agent's answer is waited for before its exit ends the turn.
const exitGrace = 200 * time.Millisecond

// tick is how often a turn checks its agent's silence.
func (h *Host) tick() time.Duration {
	return min(max(h.cfg.SilenceTimeout/20, 10*time.Millisecond), time.Second)
}

// agentError kills the process and says why the turn ended (REQ-ACP-006).
func (h *Host) agentError(ctx context.Context, p *process, why string) error {
	if f := p.failure(); f != "" {
		why = f
	}
	h.drop(p)
	h.cfg.Logger.WarnContext(ctx, "acp agent failed", slog.String("thread", p.thread), slog.String("agent", p.agent),
		slog.String("reason", why))
	return fmt.Errorf("%w: %s", domain.ErrAgent, why)
}

// drop kills the process and forgets it, unless the thread has another by now.
func (h *Host) drop(p *process) {
	p.kill()
	h.mu.Lock()
	if h.agents[p.thread] == p {
		delete(h.agents, p.thread)
	}
	h.mu.Unlock()
}
