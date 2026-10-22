package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/bus"
	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
	sessports "github.com/ecrespo/umbral/internal/sessions/ports"
)

type outputNotes struct {
	mu   sync.Mutex
	seen []string
}

func (o *outputNotes) NoteOutput(threadID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.seen = append(o.seen, threadID)
}

func (o *outputNotes) count(threadID string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := 0
	for _, id := range o.seen {
		if id == threadID {
			n++
		}
	}
	return n
}

// TestThreadPaneOutputIsActivity_REQ_AUT_008: output from the session a thread runs its
// commands in counts as that thread's activity; output from a user's own session does not.
func TestThreadPaneOutputIsActivity_REQ_AUT_008(t *testing.T) {
	b := bus.New()
	defer b.Close()
	notes := &outputNotes{}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	blocks, output := b.Subscribe(sessports.KindBlockStarted), b.Subscribe(sessports.KindSessionOutput)
	go func() { defer close(done); followThreadOutput(ctx, blocks, output, notes) }()

	b.Publish(sessports.BlockStarted{Block: sessdomain.Block{ID: "blk_1", SessionID: "ses_agent", ThreadID: "thr_1"}})
	b.Publish(sessports.BlockStarted{Block: sessdomain.Block{ID: "blk_2", SessionID: "ses_user"}})
	time.Sleep(50 * time.Millisecond) // the two subscriptions are read in no fixed order
	b.Publish(sessports.SessionOutput{SessionID: "ses_user", Data: []byte("ls\n")})
	b.Publish(sessports.SessionOutput{SessionID: "ses_agent", Data: []byte("ok\n")})

	deadline := time.Now().Add(2 * time.Second)
	for notes.count("thr_1") == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := notes.count("thr_1"); n != 1 {
		t.Fatalf("the thread's pane output noted %d times", n)
	}
	notes.mu.Lock()
	if len(notes.seen) != 1 {
		t.Errorf("output noted for %v; only thr_1's session counts", notes.seen)
	}
	notes.mu.Unlock()
	cancel()
	<-done
}
