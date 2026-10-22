package agents

import (
	"context"

	"github.com/ecrespo/umbral/internal/agents/domain"
)

// Cancel is thread.cancel (API §5.21, REQ-AGT-007). It cancels the running turn's context —
// a model call ends, a pending approval expires, and run_command signals what the thread's
// shell launched, SIGTERM then SIGKILL 300 ms later (T-F1-10) — and returns once the turn has
// recorded its end, with the moment it did. The thread is left `stopped`. A thread with no turn
// running is left as it is and nothing is reported stopped: the cancel lost the race with the
// turn's own end. So did one that found the turn already recording another end, and one whose
// end could not be written reports nothing either.
func (r *Runtime) Cancel(ctx context.Context, threadID string) (*int64, error) {
	if _, err := r.cfg.Store.Thread(ctx, threadID); err != nil {
		return nil, err
	}
	// Under the send lock, a cancel issued after thread.send returned always finds its turn.
	lock := r.threadLock(threadID)
	lock.Lock()
	r.mu.Lock()
	t := r.turns[threadID]
	r.mu.Unlock()
	lock.Unlock()
	if t == nil {
		return nil, nil
	}
	t.cancel()
	select {
	case <-t.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if t.stop != domain.StopCancelled || t.ended == 0 {
		return nil, nil
	}
	return &t.ended, nil
}
