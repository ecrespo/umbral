// Command umbral-tui is Umbral's terminal client: tabs, splits and a block list over the
// sessions `umbrald` owns (REQ-TUI-001).
//
// It is the composition root for the client, the way cmd/umbrald is for the daemon: the
// only place that picks a renderer and a transport. Art. 3 allows no other package to
// wire modules together.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	tea "charm.land/bubbletea/v2"

	"github.com/ecrespo/umbral/internal/client"
	"github.com/ecrespo/umbral/internal/tui"
	daemonadapter "github.com/ecrespo/umbral/internal/tui/adapters/daemon"
	"github.com/ecrespo/umbral/internal/tui/adapters/ghosttyvt"
	tuiports "github.com/ecrespo/umbral/internal/tui/ports"
)

// version is stamped by the linker (`-X main.version=…`); the Taskfile passes it.
var version = "0.0.0-dev"

// Exit codes from sysexits.h, the same three `umb` uses so a script driving either gets
// one answer.
const (
	exitOK          = 0
	exitFailure     = 1
	exitUnavailable = 69
)

func main() { os.Exit(run(os.Args[1:], os.Stderr)) }

// report writes one line to stderr. A failed write here is genuinely nothing to act on:
// the process is already on its way out with a non-zero code, which is the part a script
// reads.
func report(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func run(args []string, stderr *os.File) int {
	fs := flag.NewFlagSet("umbral-tui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", "", "path to the daemon socket")
	daemonPath := fs.String("daemon-path", "", "path to the umbrald binary to autostart")
	noAutostart := fs.Bool("no-autostart", false, "do not start umbrald if it is not running")
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return exitFailure
	}
	if *showVersion {
		report(stderr, "%s\n", buildVersion())
		return exitOK
	}

	client.Version = buildVersion()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	c, err := client.Connect(ctx, client.Options{
		SocketPath:  *socket,
		DaemonPath:  *daemonPath,
		NoAutostart: *noAutostart,
		// The session methods are outside the `cli` set of API Spec §2, so announcing
		// `cli` here would turn `session.create` into METHOD_NOT_FOUND.
		ClientKind: client.ClientKindTUI,
	})
	if err != nil {
		report(stderr, "umbral-tui: %v\n", err)
		if errors.Is(err, client.ErrDaemonUnavailable) || errors.Is(err, client.ErrNoSocket) {
			return exitUnavailable
		}
		return exitFailure
	}

	stream := client.NewStream(c)
	adapter := daemonadapter.New(stream)
	defer func() { _ = adapter.Close() }()

	// Named as the port rather than as the adapter: what the model depends on is the
	// interface, and saying so here is what keeps the dependency pointing inwards.
	var d tuiports.Daemon = adapter
	var screens tuiports.ScreenFactory = ghosttyvt.New

	// ghosttyvt.New is the renderer; the model never names it. DD-001 puts a VT parser on
	// this side of the socket, and it is the daemon's own so the two cannot disagree
	// about what the byte stream means.
	model := tui.New(d, screens)

	p := tea.NewProgram(model, tea.WithContext(ctx))
	if _, err := p.Run(); err != nil {
		report(stderr, "umbral-tui: %v\n", err)
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
