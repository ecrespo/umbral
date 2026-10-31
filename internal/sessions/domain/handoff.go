package domain

// What a handoff carries of a live session (REQ-HND-001, delta `2026-10-pty-handoff`): every
// piece of state the daemon keeps in memory for it, so the next daemon resumes the session
// where this one paused it. The fields are exported for the handoff frame's JSON.

// Handed is one live session as the old daemon paused it.
type Handed struct {
	Session Session
	// Pid is the shell's, which the next daemon watches but did not start.
	Pid int
	// Screen is the emulator's snapshot: replayed into an empty emulator of the session's
	// size it reproduces the screen, the scrollback and the modes.
	Screen []byte
	// Seq is the last output chunk already in Screen; the next daemon's first is Seq+1.
	Seq uint64
	// ThreadCWD and Terminal are a thread's PTY's and an ACP terminal's marks.
	ThreadCWD string
	Terminal  bool
	// Scanner is the shell-integration scanner's state, opaque outside its adapter; nil for
	// a session that records no blocks.
	Scanner  []byte
	Recorder *RecorderState
	// ChunkSeq and ChunkBlockID continue the open block's stored chunks.
	ChunkSeq     int64
	ChunkBlockID string
	// TypeAtPrompt is a restored pane's command still waiting for its prompt (REQ-TERM-011).
	TypeAtPrompt []byte
	// IntegrationPending is a verdict still waiting for its first marker (REQ-BLK-003).
	IntegrationPending bool
	BlockOpen          bool
	PromptReady        bool
	Prompts            uint64
	Interrupted        bool
	InteractLine       string
	ProgramLine        string
	DiscardLine        bool
}

// RecorderState is a recorder's whole state.
type RecorderState struct {
	CWD            string
	PendingCommand string
	AltScreen      bool
	SawMarker      bool
	Current        *Block
	Plain          PlainTextState
	Stored         int64
	Claim          string
	ClaimVia       BlockVia
}

// PlainTextState is a transcript's whole state, a sequence cut in half included.
type PlainTextState struct {
	State     int
	Line      []byte
	Out       []byte
	Truncated bool
	PendingCR bool
}

// State is the recorder's state, for a handoff.
func (r *Recorder) State() RecorderState {
	st := RecorderState{
		CWD: r.cwd, PendingCommand: r.pendingCommand, AltScreen: r.altScreen, SawMarker: r.sawMarker,
		Plain: r.plain.State(), Stored: r.stored, Claim: r.claim, ClaimVia: r.claimVia,
	}
	if r.current != nil {
		block := *r.current
		st.Current = &block
	}
	return st
}

// RecorderFrom builds a recorder in the state another daemon's was in.
func RecorderFrom(cfg RecorderConfig, st RecorderState) *Recorder {
	r := NewRecorder(cfg)
	r.cwd, r.pendingCommand, r.altScreen, r.sawMarker = st.CWD, st.PendingCommand, st.AltScreen, st.SawMarker
	r.plain = PlainTextFrom(st.Plain)
	r.stored, r.claim, r.claimVia = st.Stored, st.Claim, st.ClaimVia
	if st.Current != nil {
		block := *st.Current
		r.current = &block
	}
	return r
}

// State is the transcript's state, for a handoff.
func (p *PlainText) State() PlainTextState {
	return PlainTextState{
		State: int(p.state), Line: append([]byte(nil), p.line...), Out: append([]byte(nil), p.out...),
		Truncated: p.truncated, PendingCR: p.pendingCR,
	}
}

// PlainTextFrom builds a transcript in the state another daemon's was in.
func PlainTextFrom(st PlainTextState) PlainText {
	return PlainText{
		state: plainState(st.State), line: append([]byte(nil), st.Line...), out: append([]byte(nil), st.Out...),
		truncated: st.Truncated, pendingCR: st.PendingCR,
	}
}
