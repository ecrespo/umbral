package sessions

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/sessions/ports"
)

// bootstrap assembles the child's argv and environment, injecting shell integration when
// the caller asked for it and the shell has a bootstrap (REQ-BLK-005).
//
// A shell with no bootstrap is not an error. The session starts without integration and
// REQ-BLK-003 marks it `integration: none` after five seconds, which is the degraded mode
// DD-002 describes.
func (s *Service) bootstrap(params domain.CreateParams) (args, env []string, cleanup func() error, err error) {
	env = environ(params.Env)

	if !params.ShellIntegration || s.cfg.Bootstrap == nil {
		return nil, env, nil, nil
	}

	args, extraEnv, cleanup, err := s.cfg.Bootstrap.Prepare(params.Shell)
	if err != nil {
		s.cfg.Logger.Info("starting without shell integration",
			slog.String("shell", params.Shell), slog.Any("reason", err))
		return nil, env, nil, nil
	}
	return args, append(env, extraEnv...), cleanup, nil
}

// environ builds the child's environment: the daemon's own, plus the caller's overrides,
// plus the marker that tells a bootstrap it is running under Umbral.
func environ(overrides map[string]string) []string {
	env := append([]string{}, os.Environ()...)
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return append(env, "UMBRAL_SESSION=1")
}

// replyTarget carries the emulator's answers to device queries back to the PTY.
//
// It exists because of an ordering problem: the emulator is built before the PTY, so it
// needs somewhere to send replies at a moment when the PTY it should reach does not exist
// yet. Replies that arrive before the PTY is attached are dropped, which is safe because
// nothing has been written to the emulator by then and so nothing can have asked anything.
type replyTarget struct {
	mu        sync.RWMutex
	pty       ports.PTY
	logger    *slog.Logger
	sessionID string
}

// attach names the PTY replies should go to.
func (r *replyTarget) attach(pty ports.PTY, sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pty = pty
	r.sessionID = sessionID
}

// write sends one reply. It is called from inside the emulator, on the drain goroutine, so
// it must not call back into the emulator and must not block for long: the replies are a
// handful of bytes each and a PTY that cannot take them is a PTY that is going away.
func (r *replyTarget) write(data []byte) {
	r.mu.RLock()
	pty, sessionID := r.pty, r.sessionID
	r.mu.RUnlock()

	if pty == nil {
		return
	}
	if _, err := pty.Write(data); err != nil {
		r.logger.Debug("could not answer a terminal query",
			slog.String("session_id", sessionID), slog.Any("error", err))
	}
}

// persistCreate writes the session row before the PTY is announced (DD-007).
func (s *Service) persistCreate(ctx context.Context, session domain.Session) error {
	_, err := s.cfg.Store.DB().ExecContext(ctx, `
		INSERT INTO sessions(id, shell, cwd, cols, rows, state, integration, input_owner, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		session.ID, session.Shell, session.CWD,
		session.Size.Cols, session.Size.Rows,
		string(session.State), string(session.Integration), string(session.InputOwner),
		session.CreatedAt.UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("sessions: persist %s: %w", session.ID, err)
	}
	return nil
}

// drain reads the PTY until it ends, feeding the emulator and publishing output.
//
// It runs for the session's whole life on a background context, because REQ-TERM-003
// promises the PTY and the screen survive with no client attached. Nothing here waits on a
// subscriber: the bus drops rather than blocks, so a stalled client cannot stall the shell.
func (s *Service) drain(live *liveSession) {
	defer s.finish(live)

	buf := make([]byte, readBufferSize)
	for {
		n, err := live.pty.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])

			// The emulator write and the sequence increment are one step as far as a
			// snapshot is concerned: a reader must never see the screen updated but the
			// counter not, or it would ask for a chunk it already has.
			live.snapshotMu.Lock()
			if _, writeErr := live.emu.Write(chunk); writeErr != nil {
				s.cfg.Logger.Error("emulator write failed",
					slog.String("session_id", live.session.ID), slog.Any("error", writeErr))
			}
			live.mu.Lock()
			live.seq++
			seq := live.seq
			live.mu.Unlock()
			live.snapshotMu.Unlock()

			s.cfg.Bus.Publish(ports.SessionOutput{
				SessionID: live.session.ID, Seq: seq, Data: chunk,
			})

			// Blocks are recorded after the chunk is on its way to the clients, so a
			// slow database delays the history and never the screen.
			s.recordOutput(live, chunk)
		}
		if err != nil {
			return
		}
		if n == 0 {
			// A PTY whose child has gone reports EIO, which the adapter turns into a
			// zero-length read. Without this the loop would spin.
			return
		}
	}
}

// finish records the shell's exit exactly once (REQ-TERM-005).
//
// The blocks stay: only the session's state changes. A session that loses its history when
// the shell exits would defeat the point of owning the history in the daemon at all.
func (s *Service) finish(live *liveSession) {
	live.doneOnce.Do(func() {
		defer close(live.done)

		exitCode, waitErr := live.pty.Wait()
		if waitErr != nil {
			s.cfg.Logger.Error("wait failed",
				slog.String("session_id", live.session.ID), slog.Any("error", waitErr))
		}
		exitedAt := time.Now()

		// The block the shell died under is closed before the session is, so a client
		// that reacts to session.exited finds no block still claiming to be running.
		s.abandonOpenBlock(live)
		if live.integrationTimer != nil {
			live.integrationTimer.Stop()
		}

		live.mu.Lock()
		live.session.State = domain.StateExited
		live.session.ExitCode = &exitCode
		live.session.ExitedAt = &exitedAt
		sessionID := live.session.ID
		live.mu.Unlock()

		// Persist before notifying (DD-007): a client that reacts to session.exited must
		// find the row already saying so.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := s.cfg.Store.DB().ExecContext(ctx,
			"UPDATE sessions SET state = 'exited', exit_code = ?, exited_at = ? WHERE id = ?",
			exitCode, exitedAt.UTC().UnixMilli(), sessionID); err != nil {
			s.cfg.Logger.Error("persist the session exit",
				slog.String("session_id", sessionID), slog.Any("error", err))
		}

		s.cfg.Bus.Publish(ports.SessionExited{
			SessionID:  sessionID,
			ExitCode:   exitCode,
			ExitedAtMs: exitedAt.UTC().UnixMilli(),
		})

		_ = live.pty.Close()
		_ = live.emu.Close()
		runCleanup(live.cleanup)
		live.cancel()

		// The session stays in the map so session.list and a late session.get still find
		// it with its exit code, rather than answering NOT_FOUND for something that
		// existed a moment ago.
	})
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanSession reads one session row.
func scanSession(row rowScanner) (domain.Session, error) {
	var (
		session                              domain.Session
		state, integration, inputOwner       string
		cols, rows                           int64
		exitCode                             sql.NullInt64
		createdAtMillis, exitedAtMillisValue sql.NullInt64
	)

	err := row.Scan(&session.ID, &session.Shell, &session.CWD, &cols, &rows,
		&state, &integration, &inputOwner, &exitCode, &createdAtMillis, &exitedAtMillisValue)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.Session{}, fmt.Errorf("%w: %s", domain.ErrNotFound, session.ID)
	case err != nil:
		return domain.Session{}, fmt.Errorf("sessions: scan: %w", err)
	}

	// The schema CHECK bounds these to 1000 and 500, so the narrowing is safe.
	session.Size = domain.Size{Cols: uint16(cols), Rows: uint16(rows)}
	session.State = domain.State(state)
	session.Integration = domain.Integration(integration)
	session.InputOwner = domain.InputOwner(inputOwner)
	session.CreatedAt = time.UnixMilli(createdAtMillis.Int64).UTC()
	if exitCode.Valid {
		code := int(exitCode.Int64)
		session.ExitCode = &code
	}
	if exitedAtMillisValue.Valid {
		exitedAt := time.UnixMilli(exitedAtMillisValue.Int64).UTC()
		session.ExitedAt = &exitedAt
	}
	return session, nil
}
