package main

import (
	"context"

	"github.com/ecrespo/umbral/internal/bus"
	sessports "github.com/ecrespo/umbral/internal/sessions/ports"
)

// outputNoter is the agent runtime's side of REQ-AUT-008: output from a thread's own pane is
// activity for its running turn.
type outputNoter interface {
	NoteOutput(threadID string)
}

// followThreadOutput tells the runtime about output from the sessions threads run commands
// in. A session belongs to the thread whose block it last started (delta
// `2026-10-wait-monitoring`, decision 6); sessions and agents never import each other, so the
// two meet here. blocks carries block.started and output session.output: two subscriptions,
// so a flood of output can never push out the block that says whose session it is. A lost
// output event costs nothing, since a pane that keeps writing sends another. It returns when
// ctx ends or a subscription closes.
func followThreadOutput(ctx context.Context, blocks, output *bus.Subscription, rt outputNoter) {
	owner := map[string]string{} // session id → thread id
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-blocks.C():
			if !ok {
				return
			}
			if e, isBlock := ev.(sessports.BlockStarted); isBlock {
				if e.Block.ThreadID != "" {
					owner[e.Block.SessionID] = e.Block.ThreadID
				} else {
					delete(owner, e.Block.SessionID)
				}
			}
		case ev, ok := <-output.C():
			if !ok {
				return
			}
			if e, isOutput := ev.(sessports.SessionOutput); isOutput {
				if threadID, owned := owner[e.SessionID]; owned {
					rt.NoteOutput(threadID)
				}
			}
		}
	}
}
