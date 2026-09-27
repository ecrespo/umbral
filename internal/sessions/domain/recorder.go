package domain

import "time"

// ActionKind is what the caller must do about a change the recorder made.
type ActionKind int

const (
	// ActionOpen is a new block (REQ-BLK-001): persist the row, publish block.started.
	ActionOpen ActionKind = iota
	// ActionOutput is raw output to store for the open block. Data aliases the bytes fed
	// in, so a caller that does not write it immediately must copy it.
	ActionOutput
	// ActionState is a state change on an open block, which today means the alternate
	// screen going in or out (REQ-BLK-004): update the row, publish block.updated.
	ActionState
	// ActionClose is a block reaching its end (REQ-BLK-002): update the row with the exit
	// code and the plain text, publish block.closed.
	ActionClose
)

// Action is one instruction produced by the recorder. Actions are returned in order and
// must be carried out in order: an ActionOutput before its ActionOpen would reference a
// row that does not exist.
type Action struct {
	Kind  ActionKind
	Block Block
	// Data carries the raw bytes of an ActionOutput.
	Data []byte
	// Plain carries the finished transcript of an ActionClose (REQ-BLK-007).
	Plain string
}

// RecorderConfig wires a recorder to its session.
type RecorderConfig struct {
	SessionID string
	// Host is what `hostname` reports, stored on every block so an exported history says
	// where it ran.
	Host string
	// NewID mints a block id. Injected because identifiers belong to the store layer and
	// domain may not import it (Art. 3).
	NewID func() string
	// Now is the clock, injected so a test can make durations exact.
	Now func() time.Time
}

// Recorder turns a stream of scanner events into blocks (API Spec §7).
//
// It is a pure state machine: no database, no bus, no PTY. Everything it decides comes
// back as Actions for the caller to carry out, which is what lets the whole of REQ-BLK-001
// through 004 be tested from a hand-written list of events.
//
// It is not safe for concurrent use. One session's recorder is fed by one goroutine, the
// one draining that session's PTY.
type Recorder struct {
	cfg RecorderConfig

	cwd            string
	pendingCommand string
	altScreen      bool
	sawMarker      bool

	current *Block
	plain   PlainText
	stored  int64
}

// NewRecorder builds a recorder. It starts with no open block, which is the state a shell
// is in between its prompt and the first command.
func NewRecorder(cfg RecorderConfig) *Recorder {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Recorder{cfg: cfg}
}

// CWD reports the working directory the shell last announced through OSC 7.
func (r *Recorder) CWD() string { return r.cwd }

// SawMarker reports whether any shell-integration sequence has arrived. REQ-BLK-003 turns
// a session that never sets this into `integration: none`.
func (r *Recorder) SawMarker() bool { return r.sawMarker }

// Open reports whether a block is currently accumulating.
func (r *Recorder) Open() bool { return r.current != nil }

// Feed processes events in order and returns what to do about them.
func (r *Recorder) Feed(events []Event) []Action {
	var actions []Action
	for _, event := range events {
		if event.IsMarker() {
			r.sawMarker = true
		}
		actions = r.apply(event, actions)
	}
	return actions
}

func (r *Recorder) apply(event Event, actions []Action) []Action {
	switch event.Kind {
	case EventCWD:
		r.cwd = event.Text
	case EventCommandLine:
		r.pendingCommand = event.Text
	case EventCommandStart:
		return r.start(actions)
	case EventCommandEnd:
		return r.close(BlockFinished, event.ExitCode, actions)
	case EventAltScreen:
		return r.altScreenChanged(event.On, actions)
	case EventOutput:
		return r.output(event.Data, actions)
	case EventPromptStart, EventPromptEnd:
		// The prompt itself is not part of any block's output. Seeing it is the whole
		// contribution: it proves the integration is alive.
	}
	return actions
}

// start opens a block for the command the shell just announced (REQ-BLK-001).
func (r *Recorder) start(actions []Action) []Action {
	if r.current != nil {
		// A second OSC 133;C with no OSC 133;D in between: the shell started another
		// command without telling us how the last one ended. The block is closed as
		// abandoned rather than guessed at, because inventing an exit code would put a
		// wrong number in the history and in the agent's context.
		actions = r.close(BlockAbandoned, nil, actions)
	}

	state := BlockRunning
	if r.altScreen {
		state = BlockInteractive
	}
	block := Block{
		ID:        r.cfg.NewID(),
		SessionID: r.cfg.SessionID,
		Origin:    OriginUser,
		Command:   r.pendingCommand,
		CWD:       r.cwd,
		Host:      r.cfg.Host,
		State:     state,
		StartedAt: r.cfg.Now(),
	}
	r.pendingCommand = ""
	r.current = &block
	r.plain.Reset()
	r.stored = 0

	return append(actions, Action{Kind: ActionOpen, Block: block})
}

// close ends the open block, if there is one.
func (r *Recorder) close(state BlockState, exitCode *int, actions []Action) []Action {
	if r.current == nil {
		return actions
	}

	endedAt := r.cfg.Now()
	r.current.State = state
	r.current.ExitCode = exitCode
	r.current.EndedAt = &endedAt
	// The flag covers both caps. A client that sees it false is promised the whole of what
	// the command said, and a transcript cut at 1 MiB breaks that promise just as a chunk
	// history cut at 16 MiB does.
	if r.plain.Truncated() {
		r.current.OutputTruncated = true
	}

	action := Action{Kind: ActionClose, Block: *r.current, Plain: r.plain.String()}
	r.current = nil
	return append(actions, action)
}

// Abandon closes the open block because the session died under it (API Spec §7).
//
// The block is kept rather than deleted: what a command printed before its shell went away
// is often the only record of why it went away.
func (r *Recorder) Abandon() []Action {
	return r.close(BlockAbandoned, nil, nil)
}

// altScreenChanged moves an open block in and out of `interactive` (REQ-BLK-004).
func (r *Recorder) altScreenChanged(on bool, actions []Action) []Action {
	if r.altScreen == on {
		return actions
	}
	r.altScreen = on

	if r.current == nil {
		return actions
	}
	if on {
		r.current.State = BlockInteractive
	} else {
		r.current.State = BlockRunning
	}
	return append(actions, Action{Kind: ActionState, Block: *r.current})
}

// output accounts for a run of ordinary bytes.
func (r *Recorder) output(data []byte, actions []Action) []Action {
	if r.current == nil || len(data) == 0 {
		return actions
	}

	// The count is what the command produced, not what was kept: a build that wrote 40 MiB
	// reports 40 MiB even though only the first 16 are on disk.
	r.current.OutputBytes += int64(len(data))

	if r.current.State == BlockInteractive {
		// REQ-BLK-004: what a full-screen program paints is a picture of a moment, not a
		// transcript, and storing it would fill the history with cursor addressing.
		return actions
	}

	r.plain.Write(data)

	room := MaxOutputRawBytes - r.stored
	if room <= 0 {
		r.current.OutputTruncated = true
		return actions
	}
	if int64(len(data)) > room {
		data = data[:room]
		r.current.OutputTruncated = true
	}
	r.stored += int64(len(data))

	return append(actions, Action{Kind: ActionOutput, Block: *r.current, Data: data})
}
