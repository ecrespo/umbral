package agents

import (
	"context"
	"sync"

	"github.com/ecrespo/umbral/internal/agents/domain"
	"github.com/ecrespo/umbral/internal/agents/ports"
)

// A thread's attention state (REQ-AGT-016, API §7) changes in four places: a turn begins
// (`working`), asks for an approval (`blocked`) and is answered (`working`), and ends (`done`,
// or `idle` while a client views the thread or after a cancel); and a client focuses a `done`
// thread (`idle`). Each change is written, then announced as thread.attention_changed, under
// the thread's attention lock: two changes of one thread are never announced out of the order
// they were written in, and a focus can never fall between a turn's check for viewers and its
// write, which would leave a viewed thread `done`.

// view is what one viewer views, and when it started to, in the runtime's own order.
type view struct {
	threadID string
	at       uint64
}

// viewers is who views which thread. It is guarded by Runtime.mu.
type viewers struct {
	by  map[string]view
	seq uint64
}

func (v *viewers) set(viewer, threadID string) {
	if v.by == nil {
		v.by = map[string]view{}
	}
	v.seq++
	v.by[viewer] = view{threadID: threadID, at: v.seq}
}

func (v *viewers) viewed(threadID string) bool {
	for _, w := range v.by {
		if w.threadID == threadID {
			return true
		}
	}
	return false
}

func (v *viewers) latest() string {
	var best view
	for _, w := range v.by {
		if w.at > best.at {
			best = w
		}
	}
	return best.threadID
}

// attentionLock is the lock every attention change of a thread is written and announced
// under.
func (r *Runtime) attentionLock(threadID string) *sync.Mutex {
	lock, _ := r.attnMu.LoadOrStore(threadID, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

// Focus is thread.focus: viewer now views threadID, or nothing when it is "". The view is
// recorded before the thread is marked seen, so a turn ending meanwhile either sees the viewer
// or has written `done` before the mark moves it to `idle`.
func (r *Runtime) Focus(ctx context.Context, viewer, threadID string) error {
	if threadID == "" {
		r.Unfocus(viewer)
		return nil
	}
	if _, err := r.cfg.Store.Thread(ctx, threadID); err != nil {
		return err
	}
	r.mu.Lock()
	r.views.set(viewer, threadID)
	r.mu.Unlock()

	lock := r.attentionLock(threadID)
	lock.Lock()
	defer lock.Unlock()
	changed, err := r.cfg.Store.MarkSeen(ctx, threadID, r.now())
	if err != nil {
		return err
	}
	if changed {
		r.publish(ports.ThreadAttentionChanged{ThreadID: threadID, AttentionState: "idle"})
	}
	return nil
}

// Unfocus forgets what viewer viewed.
func (r *Runtime) Unfocus(viewer string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.views.by, viewer)
}

// Focused is the thread a viewer still viewing one focused last.
func (r *Runtime) Focused() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.views.latest()
}

func (r *Runtime) viewed(threadID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.views.viewed(threadID)
}

// finishTurn writes a turn's end, retried once — a thread left `running` takes no message
// until the daemon restarts — and announces the attention state it wrote. It returns the
// moment of the write, 0 when it failed, and whether a client viewed the thread.
func (r *Runtime) finishTurn(ctx context.Context, threadID string, state domain.State, usage domain.Usage) (int64, bool, error) {
	lock := r.attentionLock(threadID)
	lock.Lock()
	defer lock.Unlock()
	viewed := r.viewed(threadID)
	ended := r.now()
	err := r.cfg.Store.FinishTurn(ctx, threadID, state, viewed, usage, ended)
	if err != nil {
		ended = r.now()
		err = r.cfg.Store.FinishTurn(ctx, threadID, state, viewed, usage, ended)
	}
	if err != nil {
		return 0, viewed, err
	}
	r.publish(ports.ThreadAttentionChanged{ThreadID: threadID, AttentionState: domain.AttentionAfter(state, viewed)})
	return ended, viewed, nil
}
