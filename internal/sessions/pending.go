package sessions

import (
	"log/slog"
	"time"

	"github.com/ecrespo/umbral/internal/sessions/domain"
)

// PendingPromptGrace is how long a session waits for a prompt before typing its pending text
// anyway.
//
// Writing before the shell has drawn a prompt loses the bytes to the terminal discipline, so
// the daemon would have honoured REQ-TERM-011's "without running it" while quietly failing
// its "leave it visible". Waiting forever is worse: a shell with no integration never sends
// OSC 133, and the user would face an empty pane with no sign that a command was waiting. So
// the wait has an end and the text goes in regardless — a command that appears early is a
// better failure than one that never appears.
const PendingPromptGrace = 2 * time.Second

// armPendingInput schedules a session's pending text (REQ-TERM-011).
//
// Two things race to deliver it and exactly one wins, because `takePendingInput` clears the
// buffer under the lock: the shell's first prompt marker, which is the good path, and the
// grace timer, which is the fallback for a shell that never reports one.
func (s *Service) armPendingInput(live *liveSession, text []byte) {
	if len(text) == 0 {
		return
	}
	live.mu.Lock()
	live.typeAtPrompt = append([]byte(nil), text...)
	live.mu.Unlock()

	time.AfterFunc(PendingPromptGrace, func() {
		s.deliverPendingInput(live, "grace period")
	})
}

// deliverPendingInput writes the pending text to the PTY, once.
//
// It goes straight to the PTY rather than through `Input`, and that is deliberate: `Input`
// enforces the per-session lock of REQ-TERM-008, which exists to stop a *client* typing while
// the agent holds the session. This is the daemon placing a command the user's own layout
// asked for, before anyone can have taken the lock, and refusing it because of a lock held
// for someone else's benefit would leave the pane blank with no explanation.
//
// No newline. That is the whole point: the command sits on the command line, the user reads
// it, edits it or abandons it with Ctrl-C, and presses Enter if they want it. The daemon does
// not watch for that Enter — see `Pane.CommandPending`.
func (s *Service) deliverPendingInput(live *liveSession, why string) {
	text := takePendingInput(live)
	if len(text) == 0 {
		return
	}

	live.mu.RLock()
	id := live.session.ID
	live.mu.RUnlock()

	if _, err := live.pty.Write(text); err != nil {
		s.cfg.Logger.Warn("could not show the pane's pending command",
			slog.String("session_id", id), slog.String("trigger", why),
			slog.String("error", err.Error()))
		return
	}
	s.cfg.Logger.Info("pending command shown at the prompt, not run",
		slog.String("session_id", id), slog.String("trigger", why))
}

// takePendingInput removes and returns the pending text, or nil when there is none left.
func takePendingInput(live *liveSession) []byte {
	live.mu.Lock()
	defer live.mu.Unlock()
	text := live.typeAtPrompt
	live.typeAtPrompt = nil
	return text
}

// notePrompt is called for every OSC 133 event the scanner produces. The first prompt that
// ends is the earliest moment a shell is ready to be typed into.
func (s *Service) notePrompt(live *liveSession, events []domain.Event) {
	for _, event := range events {
		if event.Kind == domain.EventPromptEnd {
			s.deliverPendingInput(live, "prompt")
			return
		}
	}
}
