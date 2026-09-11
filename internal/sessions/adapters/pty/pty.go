// Package pty opens pseudo-terminals with creack/pty and adapts them to the sessions
// module's PTY port.
//
// It is deliberately thin. Everything interesting about a session, the lock, the state
// machine, the emulator, lives above the port; this package only owns the file descriptor
// and the child process.
package pty

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"

	creack "github.com/creack/pty"

	"github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/sessions/ports"
)

// Open launches the command under a new PTY. It satisfies ports.PTYFactory.
func Open(spec ports.PTYSpec) (ports.PTY, error) {
	if err := spec.Size.Validate(); err != nil {
		return nil, err
	}
	if err := validateExecutable(spec.Path); err != nil {
		return nil, err
	}
	if err := validateDir(spec.Dir); err != nil {
		return nil, err
	}

	// exec.Command, not exec.CommandContext: binding the shell to a context would kill it
	// when whatever created it goes away, and REQ-TERM-003 promises the opposite. The
	// session's lifetime is session.close and nothing else.
	//nolint:gosec,noctx // the shell path is the operator's own and validated above; the session must outlive every request
	cmd := exec.Command(spec.Path, spec.Args...)
	cmd.Env = spec.Env
	cmd.Dir = spec.Dir
	// A session leader with a controlling terminal is what makes job control, Ctrl-C and
	// SIGWINCH work. Without it the shell reports "no job control in this shell".
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}

	file, err := creack.StartWithSize(cmd, winsize(spec.Size))
	if err != nil {
		return nil, fmt.Errorf("pty: start %s: %w", spec.Path, err)
	}

	return &session{file: file, cmd: cmd}, nil
}

// session is one open PTY and the process behind it.
type session struct {
	file *os.File
	cmd  *exec.Cmd

	waitOnce sync.Once
	waitErr  error
	exitCode int
}

// Read returns PTY output. On Linux, reading a PTY whose child has exited yields EIO
// rather than EOF; it is translated so callers can treat the end of output uniformly.
func (s *session) Read(p []byte) (int, error) {
	n, err := s.file.Read(p)
	if err != nil && isPTYClosed(err) {
		return n, nil
	}
	return n, err
}

func (s *session) Write(p []byte) (int, error) { return s.file.Write(p) }

// Close releases the PTY file descriptor. The child is signalled separately: closing the
// descriptor alone leaves an orphaned shell behind.
func (s *session) Close() error {
	if err := s.file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		return fmt.Errorf("pty: close: %w", err)
	}
	return nil
}

// Resize applies the new window size, which is what triggers SIGWINCH in the child
// (REQ-TERM-007).
func (s *session) Resize(size domain.Size) error {
	if err := size.Validate(); err != nil {
		return err
	}
	if err := creack.Setsize(s.file, winsize(size)); err != nil {
		return fmt.Errorf("pty: resize to %dx%d: %w", size.Cols, size.Rows, err)
	}
	return nil
}

// Wait blocks until the child exits and reports its code. It is safe to call from several
// goroutines: only the first actually waits, because os/exec forbids a second Wait.
func (s *session) Wait() (int, error) {
	s.waitOnce.Do(func() {
		err := s.cmd.Wait()
		var exitErr *exec.ExitError
		switch {
		case err == nil:
			s.exitCode = 0
		case errors.As(err, &exitErr):
			// A shell killed by a signal reports no exit code of its own; the shell
			// convention of 128+signal is what users already read in $?.
			s.exitCode = exitErr.ExitCode()
			if s.exitCode < 0 {
				if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
					s.exitCode = 128 + int(status.Signal())
				}
			}
		default:
			s.waitErr = fmt.Errorf("pty: wait: %w", err)
		}
	})
	return s.exitCode, s.waitErr
}

// Signal sends a signal to the child's process group, so that a shell's own children go
// with it rather than surviving as orphans.
func (s *session) Signal(sig ports.SignalKind) error {
	if s.cmd.Process == nil {
		return errors.New("pty: the process is not running")
	}

	var signal syscall.Signal
	switch sig {
	case ports.SignalHangup:
		signal = syscall.SIGHUP
	case ports.SignalKill:
		signal = syscall.SIGKILL
	default:
		return fmt.Errorf("pty: unknown signal %d", sig)
	}

	// The negative pid addresses the group. Setsid above made the shell its leader.
	if err := syscall.Kill(-s.cmd.Process.Pid, signal); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			// Already gone, which is the outcome the caller wanted.
			return nil
		}
		return fmt.Errorf("pty: signal %v: %w", signal, err)
	}
	return nil
}

func winsize(size domain.Size) *creack.Winsize {
	return &creack.Winsize{Cols: size.Cols, Rows: size.Rows}
}

// isPTYClosed reports whether the read error is the EIO a Linux PTY returns once its child
// has gone, which means end of output rather than a failure.
func isPTYClosed(err error) bool {
	return errors.Is(err, syscall.EIO)
}

// validateExecutable answers a question about the world, which is why it lives in the
// adapter rather than in CreateParams.Validate (API Spec §5.3: VALIDATION_ERROR).
func validateExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%w: shell %s: %w", domain.ErrValidation, path, err)
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return fmt.Errorf("%w: shell %s is not executable", domain.ErrValidation, path)
	}
	return nil
}

func validateDir(dir string) error {
	if dir == "" {
		return nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("%w: cwd %s: %w", domain.ErrValidation, dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: cwd %s is not a directory", domain.ErrValidation, dir)
	}
	return nil
}
