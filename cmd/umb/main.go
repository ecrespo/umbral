// Command umb is the Umbral command-line client. It talks JSON-RPC to umbrald over the
// local socket and autostarts the daemon when it is not running (REQ-CLI-003).
//
// Exit codes follow sysexits.h, because a CLI meant for scripts has to be distinguishable
// from the commands it reports on:
//
//	0   the command answered
//	1   the daemon answered with an error, or the request was wrong
//	69  EX_UNAVAILABLE: the daemon is not available and could not be started (REQ-CLI-003)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/ecrespo/umbral/internal/client"
)

// version is stamped by the linker (`-X main.version=…`); the Taskfile passes it.
var version = "0.0.0-dev"

// Exit codes from sysexits.h. REQ-CLI-003 reserves 69 for "the daemon could not be
// started within the timeout"; a script distinguishes that from a command that ran and
// failed, which is exit 1.
const (
	exitOK          = 0
	exitFailure     = 1
	exitUnavailable = 69
)

// callTimeout bounds one request once the connection exists. It is separate from the
// 3 s autostart budget of REQ-CLI-003: connecting and answering are different waits, and
// a user who already has a daemon should not pay the start budget on every call.
const callTimeout = 10 * time.Second

func main() {
	client.Version = buildVersion()
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	out, errOut := newPrinter(stdout), newPrinter(stderr)
	if len(args) == 0 {
		usage(errOut)
		return exitFailure
	}

	// Ctrl-C has to reach the in-flight call rather than only the process, so a `umb`
	// waiting on a daemon that is wedged exits instead of hanging.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch args[0] {
	case "status":
		return finish(cmdStatus(ctx, args[1:], out, errOut), out, stderr)
	case "block":
		return finish(cmdBlock(ctx, args[1:], out, errOut), out, stderr)
	case "version", "--version", "-version":
		out.println(buildVersion())
		return finish(exitOK, out, stderr)
	case "help", "--help", "-h":
		usage(out)
		return finish(exitOK, out, stderr)
	default:
		errOut.printf("umb: unknown command %q\n\n", args[0])
		usage(errOut)
		return exitFailure
	}
}

func usage(p *printer) {
	p.print(`umb — Umbral command-line client

Usage:
  umb status [--json]            the daemon's health, providers and MCP servers
  umb block last [--json]        the last closed block of this session (REQ-CLI-002)
  umb version

Flags common to every command:
  --socket PATH                  the daemon socket; default is the platform runtime dir
  --daemon-path PATH             the umbrald binary to autostart; default is PATH, then
                                 the directory holding this binary
  --no-autostart                 fail instead of starting umbrald when it is not running

Exit codes: 0 answered, 1 the request or the daemon reported an error,
69 the daemon is unavailable and could not be started.
`)
}

// commonFlags are the two every subcommand shares. They are registered per subcommand
// rather than globally so that `umb block last --json` parses, which a global flag set
// before the subcommand would not.
type commonFlags struct {
	socket      string
	daemonPath  string
	noAutostart bool
	asJSON      bool
}

func (f *commonFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.socket, "socket", "", "path to the daemon socket")
	fs.StringVar(&f.daemonPath, "daemon-path", "", "path to the umbrald binary to autostart")
	fs.BoolVar(&f.noAutostart, "no-autostart", false, "do not start umbrald if it is not running")
	fs.BoolVar(&f.asJSON, "json", false, "print the daemon's answer as JSON")
}

func (f *commonFlags) options() client.Options {
	return client.Options{
		SocketPath:  f.socket,
		DaemonPath:  f.daemonPath,
		NoAutostart: f.noAutostart,
	}
}

// connect turns the client's failure modes into the exit code the caller should use, so
// no subcommand has to know that 69 exists.
func connect(ctx context.Context, opts client.Options, stderr *printer) (*client.Client, int) {
	c, err := client.Connect(ctx, opts)
	if err == nil {
		return c, exitOK
	}
	stderr.printf("umb: %v\n", err)
	if errors.Is(err, client.ErrDaemonUnavailable) || errors.Is(err, client.ErrNoSocket) {
		return nil, exitUnavailable
	}
	return nil, exitFailure
}

type statusResult struct {
	DaemonVersion  string `json:"daemon_version"`
	UptimeMS       int64  `json:"uptime_ms"`
	SessionsAlive  int    `json:"sessions_alive"`
	ThreadsRunning int    `json:"threads_running"`
	Providers      []struct {
		ID     string `json:"id"`
		Health string `json:"health"`
	} `json:"providers"`
	MCP []struct {
		Name  string `json:"name"`
		State string `json:"state"`
	} `json:"mcp"`
}

func cmdStatus(ctx context.Context, args []string, stdout, stderr *printer) int {
	fs := flag.NewFlagSet("umb status", flag.ContinueOnError)
	fs.SetOutput(stderr.w)
	var f commonFlags
	f.register(fs)
	if err := fs.Parse(args); err != nil {
		return exitFailure
	}

	c, code := connect(ctx, f.options(), stderr)
	if code != exitOK {
		return code
	}
	defer func() { _ = c.Close() }()

	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	var result statusResult
	if err := c.Call(callCtx, "system.status", nil, &result); err != nil {
		stderr.printf("umb: %v\n", err)
		return exitFailure
	}

	if f.asJSON {
		return printJSON(stdout, stderr, result)
	}

	stdout.printf("umbrald %s, up %s\n", result.DaemonVersion,
		(time.Duration(result.UptimeMS) * time.Millisecond).Round(time.Second))
	stdout.printf("sessions alive: %d\nthreads running: %d\n",
		result.SessionsAlive, result.ThreadsRunning)
	if len(result.Providers) > 0 {
		names := make([]string, 0, len(result.Providers))
		for _, p := range result.Providers {
			names = append(names, p.ID+" ("+p.Health+")")
		}
		stdout.printf("providers: %s\n", strings.Join(names, ", "))
	}
	if len(result.MCP) > 0 {
		names := make([]string, 0, len(result.MCP))
		for _, m := range result.MCP {
			names = append(names, m.Name+" ("+m.State+")")
		}
		stdout.printf("mcp: %s\n", strings.Join(names, ", "))
	}
	return exitOK
}

func cmdBlock(ctx context.Context, args []string, stdout, stderr *printer) int {
	if len(args) == 0 {
		stderr.println("umb block: expected a subcommand, e.g. `umb block last --json`")
		return exitFailure
	}
	switch args[0] {
	case "last":
		return cmdBlockLast(ctx, args[1:], stdout, stderr)
	default:
		stderr.printf("umb block: unknown subcommand %q\n", args[0])
		return exitFailure
	}
}

// block mirrors the API Spec §4 `Block` schema. It is spelled out rather than passed
// through as json.RawMessage so that a field the daemon stops sending shows up as a test
// failure here instead of as a silently absent key in whatever is parsing `--json`.
type block struct {
	ID              string  `json:"id"`
	SessionID       string  `json:"session_id"`
	Origin          string  `json:"origin"`
	ThreadID        *string `json:"thread_id"`
	Command         string  `json:"command"`
	CWD             string  `json:"cwd"`
	Host            string  `json:"host"`
	State           string  `json:"state"`
	ExitCode        *int    `json:"exit_code"`
	StartedAt       int64   `json:"started_at"`
	EndedAt         *int64  `json:"ended_at"`
	DurationMS      *int64  `json:"duration_ms"`
	OutputBytes     int64   `json:"output_bytes"`
	OutputTruncated bool    `json:"output_truncated"`
}

func cmdBlockLast(ctx context.Context, args []string, stdout, stderr *printer) int {
	fs := flag.NewFlagSet("umb block last", flag.ContinueOnError)
	fs.SetOutput(stderr.w)
	var f commonFlags
	f.register(fs)
	session := fs.String("session", "", "session id; default is $UMBRAL_SESSION_ID, or the whole history")
	if err := fs.Parse(args); err != nil {
		return exitFailure
	}

	// "The last closed block of the current session" (REQ-CLI-002). Which session that
	// is comes from the environment the daemon injects into every managed pane
	// (Tech Design §5.2b). Outside a managed pane there is no current session, and the
	// daemon's reserved id `last` then means the last block of the whole history, which
	// is the answer a user typing this in a plain terminal expects.
	sessionID := *session
	if sessionID == "" {
		sessionID = os.Getenv("UMBRAL_SESSION_ID")
	}

	c, code := connect(ctx, f.options(), stderr)
	if code != exitOK {
		return code
	}
	defer func() { _ = c.Close() }()

	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	params := map[string]any{"block_id": "last", "include": "none"}
	if sessionID != "" {
		params["session_id"] = sessionID
	}

	var result block
	if err := c.Call(callCtx, "block.get", params, &result); err != nil {
		if client.DomainCode(err) == "NOT_FOUND" {
			stderr.println("umb: no closed block yet. Shell integration records one " +
				"per command; check `umb status` if you expected some.")
			return exitFailure
		}
		stderr.printf("umb: %v\n", err)
		return exitFailure
	}

	if f.asJSON {
		return printJSON(stdout, stderr, result)
	}

	exit := "—"
	if result.ExitCode != nil {
		exit = fmt.Sprintf("%d", *result.ExitCode)
	}
	dur := "—"
	if result.DurationMS != nil {
		dur = (time.Duration(*result.DurationMS) * time.Millisecond).String()
	}
	stdout.printf("%s\nexit %s in %s · %s · %s\n",
		result.Command, exit, dur, result.State, result.CWD)
	return exitOK
}

// printJSON writes one object per line, so `umb ... --json` composes with jq and with a
// shell loop without a pretty-printer in between.
func printJSON(stdout, stderr *printer, v any) int {
	// Encoded into memory first, then written through the printer, so that a failed
	// write is reported by the same path as every other one. Encoding straight into the
	// stream would put the write error in the encoder's return value instead, and
	// `finish` would never see it.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		stderr.printf("umb: encode the JSON output: %v\n", err)
		return exitFailure
	}
	stdout.print(buf.String())
	return exitOK
}

// printer wraps an output stream and remembers the first write that failed.
//
// A CLI that ignores write errors reports success after `umb block last --json > /full/disk`,
// and the script reading that exit code then believes it has the block. Checking every
// Fprintf at its call site would bury the logic, so the failure is recorded here and each
// command asks once, at the end, through err().
type printer struct {
	w        io.Writer
	writeErr error
}

func newPrinter(w io.Writer) *printer { return &printer{w: w} }

func (p *printer) printf(format string, args ...any) {
	if p.writeErr != nil {
		return
	}
	_, p.writeErr = fmt.Fprintf(p.w, format, args...)
}

func (p *printer) println(args ...any) {
	if p.writeErr != nil {
		return
	}
	_, p.writeErr = fmt.Fprintln(p.w, args...)
}

func (p *printer) print(s string) {
	if p.writeErr != nil {
		return
	}
	_, p.writeErr = io.WriteString(p.w, s)
}

// err reports the first failed write, unless it was a closed pipe. `umb status | head -1`
// closes the pipe on purpose and is not a failure the user wants an exit code for.
func (p *printer) err() error {
	if p.writeErr == nil || errors.Is(p.writeErr, syscall.EPIPE) {
		return nil
	}
	return p.writeErr
}

// finish turns a command's outcome into its exit code, failing a nominally successful
// command whose output never reached the disk.
func finish(code int, out *printer, stderr io.Writer) int {
	if code != exitOK {
		return code
	}
	if err := out.err(); err != nil {
		_, _ = fmt.Fprintf(stderr, "umb: write the output: %v\n", err)
		return exitFailure
	}
	return exitOK
}

// buildVersion reports the linker-stamped version, falling back to the VCS revision the
// toolchain embeds when the binary was built with a plain `go build`.
func buildVersion() string {
	if version != "0.0.0-dev" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && s.Value != "" {
			return version + "+" + s.Value[:min(len(s.Value), 12)]
		}
	}
	return version
}
