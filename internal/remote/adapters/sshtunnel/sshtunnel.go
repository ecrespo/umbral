// Package sshtunnel reaches a remote daemon with the system's ssh, the remote module's
// ports.Tunnels. It asks the remote side's `umb remote endpoint` for the daemon's socket and token,
// and forwards that Unix socket with `ssh -N -L`: a remote daemon is never reached through a TCP
// port (REQ-RMT-001).
package sshtunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ecrespo/umbral/internal/remote/domain"
	"github.com/ecrespo/umbral/internal/remote/ports"
)

// readyTimeout bounds how long a forward may take to start accepting.
const readyTimeout = 30 * time.Second

// maxOutput bounds what is read of ssh's stdout and kept of its stderr.
const maxOutput = 64 << 10

// Tunnels is ports.Tunnels over an ssh binary.
type Tunnels struct {
	ssh       string
	remoteUmb string
}

var _ ports.Tunnels = (*Tunnels)(nil)

// New builds the adapter. ssh is the binary to run, "ssh" when empty; remoteUmb is the command
// the remote side runs to answer its endpoint, "umb" when empty.
func New(ssh, remoteUmb string) *Tunnels {
	if ssh == "" {
		ssh = "ssh"
	}
	if remoteUmb == "" {
		remoteUmb = "umb"
	}
	return &Tunnels{ssh: ssh, remoteUmb: remoteUmb}
}

// common are the options every ssh call takes: no prompt the daemon could never answer, a dead
// link noticed within a minute, and no shared master connection, which would carry the forward
// past Close and outlive this daemon.
func common(t domain.Target) []string {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
	}
	if t.Port != 0 {
		args = append(args, "-p", strconv.Itoa(t.Port))
	}
	return args
}

// EndpointArgs is the command line Endpoint runs, after the binary.
func (s *Tunnels) EndpointArgs(t domain.Target) []string {
	args := common(t)
	// The endpoint call forwards nothing, whatever the user's ssh config adds for the host.
	args = append(args, "-o", "ClearAllForwardings=yes")
	// `--` ends ssh's options, so the destination is never read as one whatever it holds.
	return append(args, "-T", "--", t.Destination(), s.remoteUmb, "remote", "endpoint")
}

// ForwardArgs is the command line Forward runs, after the binary.
func ForwardArgs(t domain.Target, local, remote string) []string {
	args := common(t)
	args = append(args,
		"-N",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "StreamLocalBindUnlink=yes",
		// The forwarded socket reaches a daemon: only this user may connect to it.
		"-o", "StreamLocalBindMask=0177",
		"-L", local+":"+remote,
		"--", t.Destination())
	return args
}

// Endpoint implements ports.Tunnels.
func (s *Tunnels) Endpoint(ctx context.Context, t domain.Target) (domain.Endpoint, error) {
	cmd := exec.CommandContext(ctx, s.ssh, s.EndpointArgs(t)...) //nolint:gosec // the binary is the user's configured ssh; every argument is checked in domain
	cmd.SysProcAttr = sysProcAttr()
	// A process ssh leaves holding its stderr does not hold Wait past this.
	cmd.WaitDelay = 5 * time.Second
	var stderr tail
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return domain.Endpoint{}, err
	}
	if err := cmd.Start(); err != nil {
		return domain.Endpoint{}, fmt.Errorf("start %s: %w", s.ssh, err)
	}
	out, readErr := io.ReadAll(io.LimitReader(stdout, maxOutput))
	if err := cmd.Wait(); err != nil {
		return domain.Endpoint{}, fmt.Errorf("ssh %s: %w%s", t.Destination(), err, stderr.reason())
	}
	if readErr != nil {
		return domain.Endpoint{}, readErr
	}
	var ep domain.Endpoint
	if err := json.Unmarshal(bytes.TrimSpace(out), &ep); err != nil {
		return domain.Endpoint{}, fmt.Errorf("%w: the remote `umb remote endpoint` did not answer JSON", domain.ErrValidation)
	}
	return ep, nil
}

// Forward implements ports.Tunnels. It returns once ssh listens on the local socket. ssh opens
// the remote socket per connection, not before, so a forward that started says nothing of the
// remote daemon: the caller probes it (ExitOnForwardFailure covers the local bind only).
func (s *Tunnels) Forward(ctx context.Context, t domain.Target, local, remote string) (ports.Forward, error) {
	cmd := exec.CommandContext(ctx, s.ssh, ForwardArgs(t, local, remote)...) //nolint:gosec // as in Endpoint
	cmd.SysProcAttr = sysProcAttr()
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	f := &forward{done: make(chan struct{}), local: local}
	cmd.Stderr = &f.stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", s.ssh, err)
	}
	f.cmd = cmd
	go func() {
		err := cmd.Wait()
		f.mu.Lock()
		if err == nil {
			err = errors.New("ssh exited")
		}
		f.err = fmt.Errorf("ssh %s: %w%s", t.Destination(), err, f.stderr.reason())
		f.mu.Unlock()
		close(f.done)
	}()

	deadline := time.NewTimer(readyTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		if info, err := os.Stat(local); err == nil && info.Mode()&os.ModeSocket != 0 {
			return f, nil
		}
		select {
		case <-f.done:
			return nil, f.Err()
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-deadline.C:
			_ = f.Close()
			return nil, fmt.Errorf("ssh %s: the forward was not up within %v", t.Destination(), readyTimeout)
		case <-tick.C:
		}
	}
}

// forward is one running `ssh -N -L`.
type forward struct {
	cmd    *exec.Cmd
	local  string
	done   chan struct{}
	stderr tail

	mu  sync.Mutex
	err error
}

func (f *forward) Done() <-chan struct{} { return f.done }

func (f *forward) Err() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

// Close stops ssh and waits for it, so no forward outlives its host.
func (f *forward) Close() error {
	select {
	case <-f.done:
	default:
		_ = f.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-f.done:
		case <-time.After(5 * time.Second):
			_ = f.cmd.Process.Kill()
			<-f.done
		}
	}
	_ = os.Remove(f.local)
	return nil
}

// tail keeps the end of what ssh printed on stderr, for the reason a link failed.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > maxOutput {
		t.buf = t.buf[len(t.buf)-maxOutput:]
	}
	return len(p), nil
}

// reason is the last non-empty line, ": "-prefixed, or nothing.
func (t *tail) reason() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(t.buf)), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if last == "" {
		return ""
	}
	return ": " + last
}
