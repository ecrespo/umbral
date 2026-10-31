package sessions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/store"
)

// A handoff moves every live session to a newer daemon without stopping its shell
// (REQ-HND-001, delta `2026-10-pty-handoff`). The old daemon Freezes: each drain parks between
// two reads, so the screen, the sequence number and the block being recorded hold still while
// the shell's output waits in the kernel. Then it either Releases, after the new daemon has
// Adopted the sessions, or Thaws, after a failure, and carries on as if nothing had happened
// (REQ-HND-002).

// ErrBusy is a freeze refused because an agent command is running in a session: its turn
// waits on a block only this daemon would see close.
var ErrBusy = errors.New("sessions: an agent command is running")

// ErrUnsupported is a freeze refused because a PTY cannot be paused.
var ErrUnsupported = errors.New("sessions: a PTY cannot be paused here")

// parkWait bounds how long a freeze waits for one drain to stop between two reads: a read
// takes microseconds once its deadline has passed, and one that does not return means the PTY
// ignores deadlines.
const parkWait = 5 * time.Second

// gate is a session's drain's pause: frozen holds the drain at the top of its loop, parked is
// closed when it gets there, and release when it may go on, or end without finishing the
// session if released.
type gate struct {
	frozen   bool
	parked   chan struct{}
	release  chan struct{}
	released bool
}

// park is the drain's check at the top of each read: it returns false when the session was
// released to another daemon and the drain must end without recording an exit.
func (l *liveSession) park() bool {
	l.mu.Lock()
	g := l.gate
	if g == nil || !g.frozen {
		l.mu.Unlock()
		return true
	}
	l.mu.Unlock()

	close(g.parked)
	<-g.release

	// Thaw clears the gate, and ReleaseHanded marks it before closing release.
	l.mu.Lock()
	defer l.mu.Unlock()
	return !g.released
}

// Freeze parks every live session's drain and describes each, with its PTY's master, for the
// next daemon. It refuses, leaving everything running, while an agent command is in a session
// or a PTY cannot be paused. Until Thaw or Release, no output is read and no timer of a frozen
// session writes anything.
func (s *Service) Freeze(ctx context.Context) ([]domain.Handed, []*os.File, error) {
	s.mu.RLock()
	lives := make([]*liveSession, 0, len(s.live))
	for _, live := range s.live {
		if live.snapshotState().State == domain.StateAlive {
			lives = append(lives, live)
		}
	}
	s.mu.RUnlock()

	for _, live := range lives {
		live.mu.RLock()
		busy := live.agent != nil
		live.mu.RUnlock()
		if busy {
			return nil, nil, fmt.Errorf("%w in %s", ErrBusy, live.session.ID)
		}
	}

	var frozen []*liveSession
	for _, live := range lives {
		if err := freeze(live); err != nil {
			thaw(s, frozen)
			return nil, nil, fmt.Errorf("%w: %s: %w", ErrUnsupported, live.session.ID, err)
		}
		frozen = append(frozen, live)
	}

	// Each drain parks between two reads, or ends because its shell did; an ended one is not
	// handed over, and its exit is recorded here as on any day.
	var handed []domain.Handed
	var files []*os.File
	for _, live := range frozen {
		select {
		case <-live.gate.parked:
		case <-live.done:
			continue
		case <-ctx.Done():
			thaw(s, frozen)
			return nil, nil, ctx.Err()
		case <-time.After(parkWait):
			thaw(s, frozen)
			return nil, nil, fmt.Errorf("%w: %s did not stop reading within %v", ErrUnsupported, live.session.ID, parkWait)
		}
		h, err := s.describe(live)
		if err != nil {
			thaw(s, frozen)
			return nil, nil, err
		}
		handed = append(handed, h)
		files = append(files, live.pty.File())
	}
	s.mu.Lock()
	s.frozen = frozen
	s.mu.Unlock()
	return handed, files, nil
}

// freeze stops one session's reads and its timers.
func freeze(live *liveSession) error {
	live.mu.Lock()
	live.gate = &gate{frozen: true, parked: make(chan struct{}), release: make(chan struct{})}
	if live.integrationTimer != nil {
		live.integrationTimer.Stop()
	}
	live.mu.Unlock()
	return live.pty.Pause()
}

// describe is one parked session as the next daemon needs it. The chunk of output waiting to
// be stored is stored first, so the next daemon continues at the next chunk.
func (s *Service) describe(live *liveSession) (domain.Handed, error) {
	if live.recorder != nil {
		s.flushChunk(live)
	}
	live.snapshotMu.Lock()
	screen, err := live.emu.Snapshot()
	live.snapshotMu.Unlock()
	if err != nil {
		return domain.Handed{}, fmt.Errorf("sessions: snapshot %s: %w", live.session.ID, err)
	}

	live.mu.RLock()
	defer live.mu.RUnlock()
	h := domain.Handed{
		Session: live.session, Pid: live.pty.Pid(), Screen: screen, Seq: live.seq,
		ThreadCWD: live.threadCwd, Terminal: live.terminal,
		ChunkSeq: live.chunkSeq, ChunkBlockID: live.chunkBlockID,
		TypeAtPrompt:       append([]byte(nil), live.typeAtPrompt...),
		IntegrationPending: live.session.Integration == domain.IntegrationPending,
		BlockOpen:          live.blockOpen, PromptReady: live.promptReady, Prompts: live.prompts,
		Interrupted:  live.interrupted.Load(),
		InteractLine: live.interactLine, ProgramLine: live.programLine, DiscardLine: live.discardLine,
	}
	if live.recorder != nil {
		st := live.recorder.State()
		h.Recorder = &st
		if carrier, ok := live.scanner.(interface{ State() []byte }); ok {
			h.Scanner = carrier.State()
		}
	}
	return h, nil
}

// Thaw resumes every session Freeze stopped, after a handoff that failed (REQ-HND-002).
func (s *Service) Thaw() {
	s.mu.Lock()
	frozen := s.frozen
	s.frozen = nil
	s.mu.Unlock()
	thaw(s, frozen)
}

func thaw(s *Service, frozen []*liveSession) {
	for _, live := range frozen {
		if err := live.pty.Resume(); err != nil {
			s.cfg.Logger.Error("handoff: could not resume a session's reads",
				slog.String("session_id", live.session.ID), slog.Any("error", err))
		}
		live.mu.Lock()
		g := live.gate
		live.gate = nil
		pendingVerdict := live.session.Integration == domain.IntegrationPending && live.recorder != nil
		text := live.typeAtPrompt
		live.mu.Unlock()
		if g != nil {
			close(g.release)
		}
		if pendingVerdict {
			s.startIntegrationTimer(live)
		}
		if len(text) > 0 {
			s.armPendingInput(live, text)
		}
	}
}

// ReleaseHanded ends every frozen session's drain without recording an exit, after the next daemon
// has adopted them. Nothing is signalled and nothing is written: the shells are the next
// daemon's now, and Shutdown leaves them alone.
func (s *Service) ReleaseHanded() {
	s.mu.Lock()
	frozen := s.frozen
	s.frozen = nil
	for _, live := range frozen {
		delete(s.live, live.session.ID)
	}
	s.mu.Unlock()
	for _, live := range frozen {
		live.mu.Lock()
		g := live.gate
		g.released = true
		live.mu.Unlock()
		close(g.release)
		live.cancel()
	}
}

// Adopt builds the sessions another daemon handed over, one master per session in order, and
// registers them without reading their PTYs: StartAdopted does that, once the old daemon has
// let go. A failure closes what it built and leaves the registry as it was.
func (s *Service) Adopt(ctx context.Context, handed []domain.Handed, masters []*os.File) error {
	if len(handed) != len(masters) {
		return fmt.Errorf("sessions: %d sessions and %d masters", len(handed), len(masters))
	}
	if s.cfg.AdoptPTY == nil {
		return errors.New("sessions: no PTY adopter configured")
	}
	built := make([]*liveSession, 0, len(handed))
	fail := func(err error) error {
		for _, live := range built {
			_ = live.emu.Close()
			live.cancel()
		}
		return err
	}
	for i, h := range handed {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		live, err := s.adoptOne(h, masters[i])
		if err != nil {
			return fail(fmt.Errorf("sessions: adopt %s: %w", h.Session.ID, err))
		}
		built = append(built, live)
	}
	s.mu.Lock()
	for _, live := range built {
		s.live[live.session.ID] = live
	}
	s.adopted = built
	s.mu.Unlock()
	return nil
}

func (s *Service) adoptOne(h domain.Handed, master *os.File) (*liveSession, error) {
	if err := h.Session.Size.Validate(); err != nil {
		return nil, err
	}
	replies := &replyTarget{logger: s.cfg.Logger}
	emu, err := s.cfg.NewEmu(h.Session.Size, replies.write)
	if err != nil {
		return nil, err
	}
	// The snapshot replays the screen, the scrollback and the modes. Its own answers to
	// queries go nowhere: the PTY is not attached yet, and the program asked the old daemon.
	if _, err := emu.Write(h.Screen); err != nil {
		_ = emu.Close()
		return nil, fmt.Errorf("replay the screen: %w", err)
	}
	pty, err := s.cfg.AdoptPTY(master, h.Pid)
	if err != nil {
		_ = emu.Close()
		return nil, err
	}
	replies.attach(pty, h.Session.ID)

	ctx, cancel := blockContext()
	live := &liveSession{
		session: h.Session, pty: pty, emu: emu, done: make(chan struct{}), ctx: ctx, cancel: cancel,
		threadCwd: h.ThreadCWD, terminal: h.Terminal, seq: h.Seq,
		chunkSeq: h.ChunkSeq, chunkBlockID: h.ChunkBlockID, typeAtPrompt: h.TypeAtPrompt,
		blockOpen: h.BlockOpen, promptReady: h.PromptReady, prompts: h.Prompts,
		interactLine: h.InteractLine, programLine: h.ProgramLine, discardLine: h.DiscardLine,
	}
	live.interrupted.Store(h.Interrupted)
	if h.Recorder != nil && s.cfg.Blocks != nil && s.cfg.ResumeScanner != nil {
		scanner, err := s.cfg.ResumeScanner(h.Scanner)
		if err != nil {
			_ = emu.Close()
			cancel()
			return nil, err
		}
		live.scanner = scanner
		live.recorder = domain.RecorderFrom(domain.RecorderConfig{
			SessionID: h.Session.ID, Host: s.cfg.Host,
			NewID: func() string { return store.NewID(store.PrefixBlock) },
		}, *h.Recorder)
	}
	live.pendingVerdict = h.IntegrationPending && live.recorder != nil
	return live, nil
}

// StartAdopted starts reading the adopted sessions' PTYs, from the first byte the old daemon
// did not read, with the timers they had.
func (s *Service) StartAdopted() {
	s.mu.Lock()
	adopted := s.adopted
	s.adopted = nil
	s.mu.Unlock()
	for _, live := range adopted {
		if live.pendingVerdict {
			s.startIntegrationTimer(live)
		}
		live.mu.Lock()
		text := live.typeAtPrompt
		live.mu.Unlock()
		if len(text) > 0 {
			s.armPendingInput(live, text)
		}
		go s.drain(live)
	}
}

// DropAdopted lets go of the adopted sessions, whose drains never started, after the handoff
// failed on this side: nothing is signalled and nothing is written, and the old daemon resumes
// them (REQ-HND-002).
func (s *Service) DropAdopted() {
	s.mu.Lock()
	adopted := s.adopted
	s.adopted = nil
	for _, live := range adopted {
		delete(s.live, live.session.ID)
	}
	s.mu.Unlock()
	for _, live := range adopted {
		_ = live.pty.Close()
		_ = live.emu.Close()
		live.cancel()
	}
}
