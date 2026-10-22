// Package ports is the waits module's boundary: what it reads from the agents and sessions
// modules, and what it offers the API. It may name those modules' domain types but never
// their services (Tech §5.2: waits uses the ports of agents and sessions, plus the bus).
package ports

import (
	"context"

	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/waits/domain"
)

// ThreadStatus is one consistent reading of a thread: its state, its attention state and
// its latest turn ("" when it never had one).
type ThreadStatus struct {
	State     string
	Attention string
	TurnID    string
}

// Screen is a pane's screen at one moment.
type Screen struct {
	Text string
	Seq  uint64
	// OpenLine is the line still being written at the cursor, trailing blanks kept, when
	// the cursor sits past the start of the last line written; "" otherwise.
	OpenLine string
}

// Threads is what a thread wait reads; the agents runtime implements it.
type Threads interface {
	// Status reads the thread; an unknown thread is the agents module's ErrNotFound.
	Status(ctx context.Context, threadID string) (ThreadStatus, error)
	// TurnEnd is how a turn of this daemon run ended — `stopped`, or the attention state its
	// end wrote — and false when the runtime does not remember it.
	TurnEnd(ctx context.Context, threadID, turnID string) (string, bool, error)
	// Stalled reports whether the thread's running turn is marked stalled (REQ-AUT-008).
	Stalled(threadID string) bool
}

// Terminal is what an output wait reads; the sessions module implements it.
type Terminal interface {
	// ScreenText is the session's screen as plain text, the output sequence number it is
	// current as of and the line still open at the cursor, read together so that output after it can be
	// followed without a gap and a line still being written can be continued.
	ScreenText(ctx context.Context, sessionID string) (Screen, error)
	// Block reads one block.
	Block(ctx context.Context, blockID string) (sessdomain.Block, error)
	// BlockText is a closed block's output as plain text.
	BlockText(ctx context.Context, blockID string) (string, error)
	// LatestBlock is the session's most recent block, false when it has none.
	LatestBlock(ctx context.Context, sessionID string) (sessdomain.Block, bool, error)
}

// ThreadResult is thread.wait's answer (API §5.29).
type ThreadResult struct {
	ThreadID string
	TurnID   string
	State    domain.State
	WaitedMS int64
}

// OutputResult is block.wait_output's answer (API §5.30).
type OutputResult struct {
	BlockID     string
	MatchedLine string
	LineNumber  int
}

// ThreadWait is a thread wait between its start and its answer. Starting it subscribes, so
// nothing published after the start is missed; pinning chooses the turn it observes.
type ThreadWait interface {
	// PinCurrent pins the thread's turn in progress, or its latest when none is (thread.wait).
	PinCurrent(ctx context.Context) error
	// Pin pins the turn a thread.send just started, or found by its client_msg_id.
	Pin(turnID string)
	// Wait blocks until the pinned turn settles the wait, the deadline passes (a
	// *domain.TimeoutError), or ctx ends.
	Wait(ctx context.Context) (ThreadResult, error)
	// Close releases the subscription; Wait calls it, and so must a caller that never waits.
	Close()
}

// OutputWait is an output wait between its start and its answer.
type OutputWait interface {
	Wait(ctx context.Context) (OutputResult, error)
	Close()
}

// Waits is the module's service as the API sees it.
type Waits interface {
	// Thread starts a wait on threadID for the states in until, bounded by timeoutMS, held by
	// the connection connID. It validates, counts it against the connection's limit and
	// subscribes; the caller pins.
	Thread(ctx context.Context, connID, threadID string, until []string, timeoutMS int64) (ThreadWait, error)
	// Output starts block.wait_output for connID: it validates, counts, subscribes and reads
	// the screen.
	Output(ctx context.Context, connID string, p domain.OutputParams) (OutputWait, error)
	// List is every open wait, oldest first (wait.list).
	List() []domain.Info
	// Cancel ends a wait with domain.ErrCancelled for whoever waits on it (wait.cancel); an
	// id that names no open wait is domain.ErrNotFound.
	Cancel(waitID string) error
}
