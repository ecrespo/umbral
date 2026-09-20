package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/ecrespo/umbral/internal/config"
)

// StartTimeout is the budget REQ-CLI-003 gives the whole autostart: launch `umbrald` and
// get a usable connection within it, or give up. It covers the attempts together, not
// each one, because the requirement is about how long the user waits.
const StartTimeout = 3 * time.Second

// retryInterval is how often Connect re-dials while the daemon is coming up. The daemon
// binds its socket early, so the first or second retry usually succeeds; the interval is
// short enough that the common case costs a few milliseconds rather than a poll tick.
const retryInterval = 50 * time.Millisecond

// ErrDaemonUnavailable is what Connect returns when REQ-CLI-003's 3 s budget runs out.
// `cmd/umb` turns it into exit code 69 and prints the message, which is why the message
// has to say what the user can do next rather than only what failed.
var ErrDaemonUnavailable = errors.New("umbrald did not become available")

// UnavailableError carries why the autostart failed, so the CLI can print something
// actionable. It is a distinct type rather than a wrapped string because the two
// interesting cases — "the binary is not on PATH" and "it started but never listened" —
// need different advice.
type UnavailableError struct {
	SocketPath string
	// Cause is the last error seen: a launch failure, or the last dial attempt.
	Cause error
	// Launched reports whether `umbrald` was actually started.
	Launched bool
	// Found reports whether the binary was located at all. False is an installation
	// problem and deserves different advice from a daemon that started and went quiet.
	Found bool
}

func (e *UnavailableError) Error() string {
	if !e.Found {
		return fmt.Sprintf("%v: umbrald is not on PATH, not beside `umb`, and nothing is "+
			"listening on %s. Install it, or start one yourself with `umbrald -socket %s`. (%v)",
			ErrDaemonUnavailable, e.SocketPath, e.SocketPath, e.Cause)
	}
	if !e.Launched {
		return fmt.Sprintf("%v: umbrald was found but could not be started, and nothing is "+
			"listening on %s. (%v)", ErrDaemonUnavailable, e.SocketPath, e.Cause)
	}
	return fmt.Sprintf("%v within %v: it was started but never answered on %s. "+
		"Its log is at %s; or run `umbrald -socket %s` in another terminal to watch it. (%v)",
		ErrDaemonUnavailable, StartTimeout, e.SocketPath,
		filepath.Join(filepath.Dir(e.SocketPath), LogFileName), e.SocketPath, e.Cause)
}

func (e *UnavailableError) Unwrap() error { return ErrDaemonUnavailable }

// Options tunes Connect. The zero value is what `umb` uses.
type Options struct {
	// SocketPath overrides the platform default.
	SocketPath string
	// DaemonPath overrides how `umbrald` is found. Empty means "look on PATH, then
	// beside this binary", which is what makes a tarball install work without PATH
	// surgery.
	DaemonPath string
	// NoAutostart makes a missing daemon an immediate failure. `umb status` in a script
	// that must not spawn anything wants this.
	NoAutostart bool
}

// Connect returns a connected client, starting `umbrald` if nothing is listening
// (REQ-CLI-003).
//
// The order matters: dial first, launch only on ErrNoSocket. Any other failure — a
// socket owned by another user, an unreadable token, a daemon that answered and rejected
// the handshake — is reported as it is. Starting a second daemon would not fix those, and
// racing one against an existing daemon for the same socket would turn a diagnosable
// error into an intermittent one.
func Connect(ctx context.Context, opts Options) (*Client, error) {
	socketPath := opts.SocketPath
	if socketPath == "" {
		p, err := DefaultSocketPath()
		if err != nil {
			return nil, err
		}
		socketPath = p
	}

	c, err := Dial(ctx, socketPath)
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, ErrNoSocket) || opts.NoAutostart {
		return nil, err
	}

	// The whole budget covers the launch and every retry, because REQ-CLI-003 is about
	// how long the user waits, not about how many attempts are made.
	deadline := time.Now().Add(StartTimeout)
	startCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	// The context is deliberately not passed down: the daemon must outlive this CLI
	// invocation, and handing it a context that is cancelled when Connect returns would
	// kill it the moment the connection succeeded.
	//nolint:contextcheck // the started daemon is detached on purpose; see launchDaemon
	found, launched, launchErr := launchDaemon(socketPath, opts.DaemonPath)
	if !launched {
		return nil, &UnavailableError{
			SocketPath: socketPath, Cause: launchErr, Found: found, Launched: false,
		}
	}

	// From here the question is only whether the daemon we just started becomes usable
	// inside the budget. Two kinds of failure answer it differently:
	//
	//   - a JSON-RPC error means the daemon is up and said no. That is a real answer, it
	//     will not change on the next attempt, and hiding it behind "unavailable" would
	//     cost the user the domain code that says what to fix.
	//   - anything else — no socket yet, a refused connect, a handshake that timed out
	//     against a daemon still starting — means "not usable yet". Those are retried,
	//     and when the budget runs out they become the 69 REQ-CLI-003 asks for, because
	//     the requirement measures the wait rather than the failure mode.
	lastErr := err
	for startCtx.Err() == nil {
		c, dialErr := Dial(startCtx, socketPath)
		if dialErr == nil {
			return c, nil
		}
		lastErr = dialErr

		var daemonErr *Error
		if errors.As(dialErr, &daemonErr) {
			return nil, dialErr
		}

		timer := time.NewTimer(retryInterval)
		select {
		case <-startCtx.Done():
		case <-timer.C:
		}
		timer.Stop()
	}
	return nil, &UnavailableError{
		SocketPath: socketPath, Cause: lastErr, Found: true, Launched: true,
	}
}

// LogFileName is where an autostarted daemon's log goes, inside the runtime directory.
const LogFileName = "umbrald.log"

// launchDaemon starts `umbrald` detached and returns whether it was started at all.
//
// It is detached deliberately: the daemon outlives this CLI invocation, and a `umb
// status` that owned the daemon would take it down on exit, so the next command would pay
// the start cost again.
//
// Its output cannot come back to this terminal — a daemon's JSON log interleaved with
// `umb block last --json` would corrupt what the caller is parsing — but it must not go
// to the null device either, because then every daemon started the ordinary way would
// discard the structured log Art. 7 requires, and the only diagnosis left would be the
// manual re-run the error message suggests. It goes to a file beside the socket, appended
// so that successive daemons share one history, and the error message points at it.
func launchDaemon(socketPath, daemonPath string) (found, launched bool, err error) {
	bin, err := findDaemon(daemonPath)
	if err != nil {
		return false, false, err
	}

	// On a machine where the daemon has never run there is no runtime directory yet, so
	// the log file has nowhere to go. The daemon would create the directory itself, but
	// not before this process needs to open a file in it.
	dir := filepath.Dir(socketPath)
	if err := os.MkdirAll(dir, config.RuntimeDirMode); err != nil {
		return true, false, fmt.Errorf("client: create the runtime directory %s: %w", dir, err)
	}

	logPath := filepath.Join(dir, LogFileName)
	//nolint:gosec // logPath is derived from the socket path, which comes from a flag or from config
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return true, false, fmt.Errorf("client: open the daemon log %s: %w", logPath, err)
	}
	defer func() { _ = logFile.Close() }()

	null, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		return true, false, fmt.Errorf("client: open %s: %w", os.DevNull, err)
	}
	defer func() { _ = null.Close() }()

	// context.Background rather than the caller's context, deliberately: the daemon has
	// to outlive this CLI invocation. Binding it to the context that bounds the autostart
	// would kill the daemon the moment the connection succeeded, which is the opposite of
	// what REQ-CLI-003 asks for.
	//nolint:gosec // bin is resolved from PATH or from a caller-supplied path, never from the socket
	cmd := exec.CommandContext(context.Background(), bin, "-socket", socketPath)
	cmd.Stdin = null
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = detachAttr()

	if err := cmd.Start(); err != nil {
		return true, false, fmt.Errorf("client: start %s: %w", bin, err)
	}
	// Reap it so the started daemon does not stay a zombie child of this short-lived
	// CLI. Releasing is enough: the process is already in its own session.
	if err := cmd.Process.Release(); err != nil {
		return true, true, fmt.Errorf("client: release %s: %w", bin, err)
	}
	return true, true, nil
}

// findDaemon locates `umbrald`: an explicit path wins, then PATH, then the directory
// holding this binary. The last one is what makes an unpacked tarball work, where `umb`
// and `umbrald` sit side by side and neither is on PATH.
func findDaemon(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("client: %s: %w", explicit, err)
		}
		return explicit, nil
	}
	if bin, err := exec.LookPath("umbrald"); err == nil {
		return bin, nil
	}
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("client: umbrald is not on PATH and this binary cannot locate itself: %w", err)
	}
	sibling := filepath.Join(filepath.Dir(self), "umbrald")
	if _, err := os.Stat(sibling); err != nil {
		return "", fmt.Errorf("client: umbrald is neither on PATH nor beside %s", self)
	}
	return sibling, nil
}
