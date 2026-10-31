package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ecrespo/umbral/internal/config"
)

// The remote.* methods (API Spec §5.44).
// verbAdd is `umb remote add`.
const verbAdd = "add"

const (
	methodRemoteList   = "remote.list"
	methodRemoteAdd    = "remote.add"
	methodRemoteRemove = "remote.remove"
)

type remoteHost struct {
	Name      string  `json:"name"`
	URL       string  `json:"url"`
	State     string  `json:"state"`
	LastError *string `json:"last_error"`
	CreatedAt int64   `json:"created_at"`
}

// cmdRemote is `umb remote` (REQ-RMT-001): add, list and remove, each one remote.* method, and
// `endpoint`, which a local daemon runs on the remote machine over ssh to learn where that
// machine's daemon listens and with which token.
func cmdRemote(ctx context.Context, args []string, stdout, stderr *printer) int {
	const usageText = "umb remote: add NAME ssh://[user@]host[:port] | list | remove NAME | endpoint"
	if len(args) == 0 {
		stderr.println(usageText)
		return exitFailure
	}
	name := args[0]
	fs := flag.NewFlagSet("umb remote "+name, flag.ContinueOnError)
	fs.SetOutput(stderr.w)
	var f commonFlags
	f.register(fs)
	pos, command, err := parseInterleaved(fs, args[1:])
	if err != nil {
		return exitFailure
	}

	var method string
	var params map[string]any
	switch {
	case name == "endpoint" && len(pos) == 0 && command == nil:
		return remoteEndpoint(ctx, f, stdout, stderr)
	case name == "list" && len(pos) == 0 && command == nil:
		method, params = methodRemoteList, map[string]any{}
	case name == verbAdd && len(pos) == 2 && command == nil:
		method, params = methodRemoteAdd, map[string]any{paramName: pos[0], "url": pos[1]}
	case name == "remove" && len(pos) == 1 && command == nil:
		method, params = methodRemoteRemove, map[string]any{paramName: pos[0]}
	default:
		stderr.println(usageText)
		return exitFailure
	}

	c, code := connect(ctx, f.options(), stderr)
	if code != exitOK {
		return code
	}
	defer func() { _ = c.Close() }()
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	prefix := "umb remote " + name + ": "

	switch method {
	case methodRemoteList:
		var out struct {
			Items []remoteHost `json:"items"`
		}
		if err := c.Call(callCtx, method, params, &out); err != nil {
			return callFailed(stderr, prefix, err)
		}
		if f.asJSON {
			return printJSON(stdout, stderr, out)
		}
		if len(out.Items) == 0 {
			stdout.println("no remote hosts configured")
		}
		for _, h := range out.Items {
			line := fmt.Sprintf("%s  %s  %s", printable(h.Name), printable(h.URL), printable(h.State))
			if h.LastError != nil {
				line += "  " + printable(*h.LastError)
			}
			stdout.println(line)
		}
	case methodRemoteAdd:
		var h remoteHost
		if err := c.Call(callCtx, method, params, &h); err != nil {
			return callFailed(stderr, prefix, err)
		}
		if f.asJSON {
			return printJSON(stdout, stderr, h)
		}
		stdout.printf("added %s (%s): %s\n", printable(h.Name), printable(h.URL), printable(h.State))
	default:
		var out map[string]any
		if err := c.Call(callCtx, method, params, &out); err != nil {
			return callFailed(stderr, prefix, err)
		}
		if f.asJSON {
			return printJSON(stdout, stderr, out)
		}
		stdout.printf("removed %s\n", printable(pos[0]))
	}
	return exitOK
}

// remoteEndpoint prints `{"socket": …, "token": …}` for this machine's daemon, starting it if it
// is not running. It is what a local daemon's ssh runs here (REQ-RMT-001); the token leaves
// this machine only on that ssh channel, to a user who can already read it.
func remoteEndpoint(ctx context.Context, f commonFlags, stdout, stderr *printer) int {
	socket := f.socket
	if socket == "" {
		p, err := config.DefaultSocketPath()
		if err != nil {
			stderr.printf("umb remote endpoint: %v\n", err)
			return exitFailure
		}
		socket = p
	}
	opts := f.options()
	opts.SocketPath = socket
	c, code := connect(ctx, opts, stderr)
	if code != exitOK {
		return code
	}
	_ = c.Close()
	abs, err := filepath.Abs(socket)
	if err != nil {
		stderr.printf("umb remote endpoint: %v\n", err)
		return exitFailure
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(abs), config.TokenFileName))
	if err != nil {
		stderr.printf("umb remote endpoint: read the token: %v\n", err)
		return exitFailure
	}
	line, err := json.Marshal(struct {
		Socket string `json:"socket"`
		Token  string `json:"token"`
	}{abs, strings.TrimSpace(string(raw))})
	if err != nil {
		stderr.printf("umb remote endpoint: %v\n", err)
		return exitFailure
	}
	stdout.println(string(line))
	return exitOK
}
