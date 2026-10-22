package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
)

// The mcp.server.* methods, and the name parameter they and skill.* share.
const (
	methodMcpList   = "mcp.server.list"
	methodMcpAdd    = "mcp.server.add"
	methodMcpRemove = "mcp.server.remove"
	paramName       = "name"
)

type mcpServer struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Transport string   `json:"transport"`
	Trust     string   `json:"trust"`
	State     string   `json:"state"`
	Tools     []string `json:"tools"`
	LastError *string  `json:"last_error"`
}

// envRefs is --env, repeatable: NAME=keyring:<path> or NAME=env:<VAR>.
type envRefs map[string]string

// takeEnv removes every --env from the arguments before the flag package sees them, because
// that package quotes an invalid value in its error, and the value may be the secret itself.
// Nothing after a bare `--` is looked at: it is the server's own command line.
func takeEnv(args []string) (rest []string, refs envRefs, err error) {
	refs = envRefs{}
	// value is set when the previous argument was a bare --env, whose value this one is.
	value := false
	for i, a := range args {
		v, isRef := a, value
		switch {
		case value:
			value = false
		case a == "--":
			return append(rest, args[i:]...), refs, nil
		case a == "--env" || a == "-env":
			value = true
			continue
		case strings.HasPrefix(a, "--env=") || strings.HasPrefix(a, "-env="):
			_, v, _ = strings.Cut(a, "=")
			isRef = true
		}
		if !isRef {
			rest = append(rest, a)
			continue
		}
		if err := refs.set(v); err != nil {
			return nil, nil, err
		}
	}
	if value {
		return nil, nil, fmt.Errorf("--env needs NAME=REF")
	}
	return rest, refs, nil
}

// set refuses anything but a reference (REQ-SEC-004): a plaintext value typed on the command
// line must never be written to the socket. The value is not echoed back.
func (e envRefs) set(v string) error {
	name, ref, ok := strings.Cut(v, "=")
	if !ok || name == "" {
		return fmt.Errorf("want NAME=keyring:<path> or NAME=env:<VAR>")
	}
	for _, prefix := range []string{"keyring:", "env:"} {
		if rest, found := strings.CutPrefix(ref, prefix); found {
			if rest == "" {
				return fmt.Errorf("%s: %s names nothing", name, prefix)
			}
			e[name] = ref
			return nil
		}
	}
	return fmt.Errorf("%s: secrets are referenced, never given: use keyring:<path> or env:<VAR>", name)
}

// cmdMcp is `umb mcp` (REQ-CLI-008, API Spec §5.27): add, list and remove, each one
// mcp.server.* method. A server added here is always untrusted; trusting one is a policy
// decision, not a flag in a copied script (delta `2026-09-cli-mcp`).
func cmdMcp(ctx context.Context, args []string, stdout, stderr *printer) int {
	const usageText = "umb mcp: add NAME (--stdio [--env NAME=REF]… -- COMMAND [ARGS…] | --http URL) | list | remove NAME"
	if len(args) == 0 {
		stderr.println(usageText)
		return exitFailure
	}
	name := args[0]
	fs := flag.NewFlagSet("umb mcp "+name, flag.ContinueOnError)
	fs.SetOutput(stderr.w)
	var f commonFlags
	f.register(fs)
	stdio := fs.Bool("stdio", false, "a local server started with the command after --")
	url := fs.String("http", "", "a remote server's URL")
	rest, env, err := takeEnv(args[1:])
	if err != nil {
		stderr.printf("umb mcp %s: %v\n", name, err)
		return exitFailure
	}
	pos, command, err := parseInterleaved(fs, rest)
	if err != nil {
		return exitFailure
	}

	var method string
	var params map[string]any
	switch {
	case name == "list" && len(pos) == 0 && command == nil && len(env) == 0:
		method, params = methodMcpList, map[string]any{}
	case name == "remove" && len(pos) == 1 && command == nil && len(env) == 0:
		method, params = methodMcpRemove, map[string]any{paramName: pos[0]}
	case name == "add" && len(pos) == 1 && *stdio && *url == "" && len(command) > 0:
		method = methodMcpAdd
		params = map[string]any{paramName: pos[0], "transport": "stdio", "command": command[0], "args": command[1:], "trust": "untrusted"}
	case name == "add" && len(pos) == 1 && !*stdio && *url != "" && command == nil && len(env) == 0:
		method = methodMcpAdd
		params = map[string]any{paramName: pos[0], "transport": "http", "url": *url, "trust": "untrusted"}
	default:
		stderr.println(usageText)
		return exitFailure
	}
	if method == methodMcpAdd && len(env) > 0 {
		params["env_refs"] = map[string]string(env)
	}

	c, code := connect(ctx, f.options(), stderr)
	if code != exitOK {
		return code
	}
	defer func() { _ = c.Close() }()
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	prefix := "umb mcp " + name + ": "

	switch method {
	case methodMcpList:
		var out struct {
			Items []mcpServer `json:"items"`
		}
		if err := c.Call(callCtx, method, params, &out); err != nil {
			return callFailed(stderr, prefix, err)
		}
		if f.asJSON {
			return printJSON(stdout, stderr, out)
		}
		if len(out.Items) == 0 {
			stdout.println("no MCP servers configured")
		}
		for _, s := range out.Items {
			line := fmt.Sprintf("%s  %s  %s  %s", printable(s.Name), printable(s.Transport), printable(s.Trust), printable(s.State))
			if s.LastError != nil {
				line += "  " + printable(*s.LastError)
			}
			stdout.println(line)
		}
	case methodMcpAdd:
		var s mcpServer
		if err := c.Call(callCtx, method, params, &s); err != nil {
			return callFailed(stderr, prefix, err)
		}
		if f.asJSON {
			return printJSON(stdout, stderr, s)
		}
		stdout.printf("added %s (%s, untrusted): %s\n", printable(s.Name), printable(s.Transport), printable(s.State))
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
