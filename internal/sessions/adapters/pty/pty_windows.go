// Package pty opens shells on a pseudo-terminal. On Windows that is ConPTY, through
// aymanbagabas/go-pty (delta `2026-10-windows-port`, decision 5); the emulator, blocks and
// snapshots above the port are the same as on Unix.
package pty

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	conpty "github.com/aymanbagabas/go-pty"
	"golang.org/x/sys/windows"

	"github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/sessions/ports"
)

// ErrUnsupported is what Windows answers for a live handoff, which it does not support
// (decision 11): Adopt, Pause, Resume.
var ErrUnsupported = errors.New("pty: live handoff is not supported on Windows")

// ctrlC is what a Ctrl-C keystroke writes. ConPTY turns it into CTRL_C_EVENT for the processes
// attached to the console, which is SIGINT's place on Windows.
const ctrlC = 0x03

// Open launches the command under a new pseudo console. It satisfies ports.PTYFactory.
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

	p, err := conpty.New()
	if err != nil {
		return nil, fmt.Errorf("pty: create a pseudo console: %w", err)
	}
	if err := p.Resize(int(spec.Size.Cols), int(spec.Size.Rows)); err != nil {
		_ = p.Close()
		return nil, fmt.Errorf("pty: size the pseudo console: %w", err)
	}
	// Command, not CommandContext: the session must outlive every request (REQ-TERM-003).
	cmd := p.Command(spec.Path, spec.Args...)
	cmd.Env = spec.Env
	cmd.Dir = spec.Dir
	if err := cmd.Start(); err != nil {
		_ = p.Close()
		return nil, fmt.Errorf("pty: start %s: %w", spec.Path, err)
	}
	pid := cmd.Process.Pid
	job, err := jobFor(pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = p.Close()
		return nil, err
	}
	return &session{pty: p, cmd: cmd, pid: pid, job: job}, nil
}

// jobFor puts pid in a Job Object that ends every process in it when it is closed, which is
// Windows' process group: the shell's children go with it rather than surviving as orphans.
// A child the shell started before this ran escapes the job; a shell that has just started has
// not had the time.
func jobFor(pid int) (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("pty: create a job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return 0, fmt.Errorf("pty: configure the job object: %w", err)
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return 0, fmt.Errorf("pty: open the shell's process: %w", err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		_ = windows.CloseHandle(job)
		return 0, fmt.Errorf("pty: put the shell in its job: %w", err)
	}
	return job, nil
}

// session is one pseudo console and the process behind it.
type session struct {
	pty conpty.Pty
	cmd *conpty.Cmd
	pid int
	job windows.Handle

	closeOnce sync.Once
	jobOnce   sync.Once
	waitOnce  sync.Once
	waitErr   error
	exitCode  int
	// exited is set once the child has been reaped, after which its pid is not ours.
	exited atomic.Bool
}

// Read returns console output. A pseudo console whose pipe closed has no more output, which
// is the end of output rather than a failure.
func (s *session) Read(p []byte) (int, error) {
	n, err := s.pty.Read(p)
	if err != nil && isClosedPipe(err) {
		return n, nil
	}
	return n, err
}

func (s *session) Write(p []byte) (int, error) { return s.pty.Write(p) }

// Pause answers ErrUnsupported: it exists for a live handoff.
func (s *session) Pause() error { return ErrUnsupported }

// Resume answers ErrUnsupported: it exists for a live handoff.
func (s *session) Resume() error { return ErrUnsupported }

// File is nil: a pseudo console has no single descriptor to hand over.
func (s *session) File() *os.File { return nil }

// Close closes the pseudo console, which ends the shell's console, and the job, which ends
// what is left in it.
func (s *session) Close() error {
	var err error
	s.closeOnce.Do(func() { err = s.pty.Close() })
	s.closeJob()
	if err != nil && !errors.Is(err, os.ErrClosed) {
		return fmt.Errorf("pty: close: %w", err)
	}
	return nil
}

func (s *session) closeJob() {
	s.jobOnce.Do(func() { _ = windows.CloseHandle(s.job) })
}

// Resize resizes the pseudo console, which the console host turns into a window-size event for
// the shell (REQ-TERM-007).
func (s *session) Resize(size domain.Size) error {
	if err := size.Validate(); err != nil {
		return err
	}
	if err := s.pty.Resize(int(size.Cols), int(size.Rows)); err != nil {
		return fmt.Errorf("pty: resize to %dx%d: %w", size.Cols, size.Rows, err)
	}
	return nil
}

// Wait blocks until the child exits and reports its code. Only the first call waits.
func (s *session) Wait() (int, error) {
	s.waitOnce.Do(func() {
		err := s.cmd.Wait()
		s.exited.Store(true)
		var exitErr *exec.ExitError
		switch {
		case err == nil:
			s.exitCode = 0
		case errors.As(err, &exitErr):
			s.exitCode = exitErr.ExitCode()
		default:
			s.waitErr = fmt.Errorf("pty: wait: %w", err)
		}
	})
	return s.exitCode, s.waitErr
}

// Pid implements ports.PTY.
func (s *session) Pid() int { return s.pid }

// Signal is the Windows reading of each signal: a hang-up closes the pseudo console, which
// sends the shell CTRL_CLOSE_EVENT; a kill closes the job, which terminates every process in
// it; the foreground signals reach what the shell runs and leave the shell.
func (s *session) Signal(sig ports.SignalKind) error {
	if s.pid == 0 {
		return errors.New("pty: the process is not running")
	}
	switch sig {
	case ports.SignalHangup:
		var err error
		s.closeOnce.Do(func() { err = s.pty.Close() })
		return err
	case ports.SignalKill:
		s.closeJob()
		if !s.exited.Load() {
			_ = s.cmd.Process.Kill()
		}
		return nil
	case ports.SignalTermForeground:
		if s.exited.Load() {
			return nil
		}
		if _, err := s.pty.Write([]byte{ctrlC}); err != nil {
			return fmt.Errorf("pty: interrupt the foreground: %w", err)
		}
		return nil
	case ports.SignalKillForeground:
		if s.exited.Load() {
			return nil
		}
		return killDescendants(uint32(s.pid))
	}
	return fmt.Errorf("pty: unknown signal %d", sig)
}

// killDescendants terminates every process below pid, leaving pid itself: the command the shell
// runs, and whatever that command started.
func killDescendants(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return fmt.Errorf("pty: list the processes: %w", err)
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	children := map[uint32][]uint32{}
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		if entry.ProcessID != entry.ParentProcessID {
			children[entry.ParentProcessID] = append(children[entry.ParentProcessID], entry.ProcessID)
		}
	}
	queue := append([]uint32(nil), children[pid]...)
	seen := map[uint32]bool{pid: true}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if seen[p] {
			continue
		}
		seen[p] = true
		queue = append(queue, children[p]...)
		if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, p); err == nil {
			_ = windows.TerminateProcess(h, 1)
			_ = windows.CloseHandle(h)
		}
	}
	return nil
}

// isClosedPipe is the end of a pseudo console's output: its pipe broken or closed.
func isClosedPipe(err error) bool {
	return errors.Is(err, windows.ERROR_BROKEN_PIPE) || errors.Is(err, os.ErrClosed) ||
		errors.Is(err, windows.ERROR_NO_DATA)
}

// validateExecutable checks that path names a file Windows runs as a program: one whose
// extension is in PATHEXT, since Windows has no executable bit.
func validateExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%w: shell %s: %w", domain.ErrValidation, path, err)
	}
	if info.IsDir() || !runnable(path) {
		return fmt.Errorf("%w: shell %s is not executable", domain.ErrValidation, path)
	}
	return nil
}

func runnable(path string) bool {
	exts := os.Getenv("PATHEXT")
	if exts == "" {
		exts = ".COM;.EXE;.BAT;.CMD"
	}
	ext := strings.ToUpper(filepath.Ext(path))
	for _, e := range strings.Split(exts, ";") {
		if ext != "" && ext == strings.ToUpper(e) {
			return true
		}
	}
	return false
}

// Adopt answers ErrUnsupported: a live handoff is not supported on Windows.
func Adopt(*os.File, int) (ports.PTY, error) { return nil, ErrUnsupported }
