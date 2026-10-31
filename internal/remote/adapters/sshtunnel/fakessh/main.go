// Command fakessh stands in for ssh in the remote module's tests. It understands the two command
// lines sshtunnel runs:
//
//   - `-N … -L local:remote -- dest` forwards the local Unix socket to the remote one, as ssh's
//     stream-local forwarding does, until SIGTERM;
//   - `… -- dest command…` runs the command with `sh -c`, as sshd does, in the "host"'s
//     environment: with FAKESSH_ROOT set, every XDG directory is moved under
//     $FAKESSH_ROOT/<host>/, so the command reaches that host's daemon.
//
// FAKESSH_LOG appends each argv, FAKESSH_PIDDIR records a forward's pid as <host>.<pid>, and a
// host named in FAKESSH_REFUSE is refused as an unreachable one is.
package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

func main() {
	args := os.Args[1:]
	if log := os.Getenv("FAKESSH_LOG"); log != "" {
		if f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil { //nolint:gosec // a test stand-in for ssh: its arguments and paths are the test's own
			_, _ = fmt.Fprintln(f, strings.Join(args, "\x1f"))
			_ = f.Close()
		}
	}
	var forward string
	noCommand := false
	i := 0
	for ; i < len(args); i++ {
		switch args[i] {
		case "-o", "-p":
			i++
		case "-L":
			i++
			forward = args[i]
		case "-N":
			noCommand = true
		case "-T":
		case "--":
			i++
			goto dest
		default:
			fail("fakessh: unexpected argument %q", args[i])
		}
	}
dest:
	if i >= len(args) {
		fail("fakessh: no destination")
	}
	dest := args[i]
	host := dest[strings.LastIndexByte(dest, '@')+1:]
	for _, refused := range strings.Split(os.Getenv("FAKESSH_REFUSE"), ",") {
		if refused != "" && refused == host {
			fmt.Fprintf(os.Stderr, "ssh: connect to host %s port 22: Connection refused\n", host)
			os.Exit(255)
		}
	}
	if noCommand {
		local, remote, ok := strings.Cut(forward, ":")
		if !ok {
			fail("fakessh: -N needs -L local:remote")
		}
		serve(host, local, remote)
		return
	}
	run(host, args[i+1:])
}

func run(host string, command []string) {
	cmd := exec.Command("sh", "-c", strings.Join(command, " ")) //nolint:gosec,noctx // sshd runs the command line with the user's shell, and so does this stand-in
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = os.Environ()
	if root := os.Getenv("FAKESSH_ROOT"); root != "" {
		for _, kv := range [][2]string{
			{"XDG_RUNTIME_DIR", "run"},
			{"XDG_DATA_HOME", "data"},
			{"XDG_CONFIG_HOME", "config"},
			{"XDG_STATE_HOME", "state"},
			{"XDG_CACHE_HOME", "cache"},
		} {
			cmd.Env = append(cmd.Env, kv[0]+"="+filepath.Join(root, host, kv[1]))
		}
	}
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.ExitCode())
		}
		fail("fakessh: %v", err)
	}
}

func serve(host, local, remote string) {
	// As OpenSSH's -L: the local socket is bound at start (ExitOnForwardFailure fails a bind that
	// cannot be made), and the remote one is opened per connection, so a remote daemon that is
	// not there closes each connection rather than failing the forward.
	_ = os.Remove(local) //nolint:gosec // a test stand-in for ssh: its arguments and paths are the test's own
	old := syscall.Umask(0o177)
	ln, err := net.Listen("unix", local) //nolint:noctx // the process lives as long as the listener
	syscall.Umask(old)
	if err != nil {
		fail("fakessh: listen %s: %v", local, err)
	}
	if dir := os.Getenv("FAKESSH_PIDDIR"); dir != "" {
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s.%d", host, os.Getpid())), nil, 0o600) //nolint:gosec // a test stand-in for ssh: its arguments and paths are the test's own
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	var mu sync.Mutex
	var open []net.Conn
	go func() {
		<-stop
		_ = ln.Close()
		mu.Lock()
		for _, c := range open {
			_ = c.Close()
		}
		mu.Unlock()
		_ = os.Remove(local) //nolint:gosec // a test stand-in for ssh: its arguments and paths are the test's own
		os.Exit(0)
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		up, err := net.Dial("unix", remote) //nolint:gosec,noctx // the forward's own remote end
		if err != nil {
			_ = c.Close()
			continue
		}
		mu.Lock()
		open = append(open, c, up)
		mu.Unlock()
		go pipe(c, up)
		go pipe(up, c)
	}
}

func pipe(dst, src net.Conn) {
	_, _ = io.Copy(dst, src)
	_ = dst.Close()
	_ = src.Close()
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(255)
}
