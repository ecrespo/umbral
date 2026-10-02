// Package mcptools puts an MCP server's tools in the tool registry (REQ-MCP-001): each one as
// mcp_<server>_<tool>, with the server's description and input schema, under the policy
// engine like every other tool. Every MCP tool is Network, whatever the server says of it:
// what it does happens outside Umbral's view, as a remote request's does, so it asks by default
// in every mode that offers it (REQ-MCP-004), a tainted turn asks for it (REQ-SEC-006), and
// `ask` mode does not offer it. Exec would not do: the policy reads an Exec target as a shell
// line. The target is the call's arguments as one line of JSON, which rules may match. An
// untrusted server's results are untrusted content (REQ-SEC-006).
package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	llmdomain "github.com/ecrespo/umbral/internal/llmgw/domain"
	mcpdomain "github.com/ecrespo/umbral/internal/mcp/domain"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
	"github.com/ecrespo/umbral/internal/tools/domain"
	"github.com/ecrespo/umbral/internal/tools/ports"
)

// Registrar is the registry as the catalog changes it.
type Registrar interface {
	Register(t ports.Tool) error
	Unregister(name string) bool
}

// Caller runs a tool of a server: the mcp module's manager, as cmd/umbrald hands it over.
type Caller func(ctx context.Context, server, tool string, args json.RawMessage) (mcpdomain.CallResult, error)

// Catalog keeps each server's tools in the registry. It is the mcp module's ToolSink.
type Catalog struct {
	reg    Registrar
	caller Caller

	mu    sync.Mutex
	names map[string][]string // server → registered tool names
}

// New builds a catalog over a registry.
func New(reg Registrar, caller Caller) *Catalog {
	return &Catalog{reg: reg, caller: caller, names: map[string][]string{}}
}

// Sync makes the registry hold exactly these tools for the server, and returns the ones it
// could not offer, by the server's name for them, with why.
func (c *Catalog) Sync(s mcpdomain.Server, tools []mcpdomain.ToolInfo) map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, name := range c.names[s.Name] {
		c.reg.Unregister(name)
	}
	skipped := map[string]string{}
	var registered []string
	for _, info := range tools {
		t, why := newTool(s, info, c.caller)
		if why == "" {
			if err := c.reg.Register(t); err != nil {
				why = err.Error()
			}
		}
		if why != "" {
			skipped[info.Name] = why
			continue
		}
		registered = append(registered, t.spec.Name)
	}
	c.names[s.Name] = registered
	return skipped
}

// Drop takes every tool of the server out of the registry.
func (c *Catalog) Drop(server string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, name := range c.names[server] {
		c.reg.Unregister(name)
	}
	delete(c.names, server)
}

// tool is one MCP tool as the registry sees it.
type tool struct {
	spec    domain.Spec
	server  string
	name    string // the server's own name for it
	tainted bool
	caller  Caller
}

var _ ports.Tool = tool{}

// newTool builds the registry's view of one tool, or says why it cannot be offered.
func newTool(s mcpdomain.Server, info mcpdomain.ToolInfo, caller Caller) (tool, string) {
	name, ok := mcpdomain.ToolName(s.Name, info.Name)
	if !ok {
		return tool{}, "the prefixed name is not a valid tool name of at most 64 characters"
	}
	if info.InputSchema == nil || info.InputSchema["type"] != "object" {
		return tool{}, "its input schema is not an object schema"
	}
	risk := secdomain.RiskNetwork
	desc := strings.TrimSpace(info.Description)
	if desc == "" {
		desc = fmt.Sprintf("Tool %s of the MCP server %s.", info.Name, s.Name)
	}
	if r := []rune(desc); len(r) > mcpdomain.MaxDescription {
		desc = string(r[:mcpdomain.MaxDescription])
	}
	return tool{
		spec:   domain.Spec{Name: name, Description: desc, Risk: risk, InputSchema: info.InputSchema},
		server: s.Name, name: info.Name, tainted: s.Trust != mcpdomain.TrustTrusted, caller: caller,
	}, ""
}

func (t tool) Spec() domain.Spec { return t.spec }

// Action is the call as the policy sees it: the arguments, compacted, are the target.
func (t tool) Action(env domain.Env, input json.RawMessage) (secdomain.Action, error) {
	return secdomain.Action{
		ThreadID: env.ThreadID, Tool: t.spec.Name, Risk: t.spec.Risk, Target: mcpdomain.Compact(input),
		Cwd: env.Cwd, WriteRoot: env.WriteRoot,
	}, nil
}

// Summary is the server, the tool and the start of its arguments.
func (t tool) Summary(input json.RawMessage) string {
	args := mcpdomain.Compact(input)
	if r := []rune(args); len(r) > 160 {
		args = string(r[:160]) + "…"
	}
	return t.server + "." + t.name + " " + args
}

// Run calls the server. A failure the server reports (`isError`) is still a result: its text
// is the server's, so it carries the server's taint like any other result, rather than reaching
// the model as an error message no taint follows.
func (t tool) Run(ctx context.Context, env domain.Env, input json.RawMessage) (domain.Result, error) {
	res, err := t.caller(llmdomain.WithThread(ctx, env.ThreadID), t.server, t.name, input)
	switch {
	case err == nil:
	case ctx.Err() != nil, errors.Is(err, mcpdomain.ErrUnavailable), errors.Is(err, mcpdomain.ErrRefused):
		// The daemon's own words, or a cancel: an error.
		return domain.Result{}, err
	default:
		// Anything else can carry the server's text — a JSON-RPC error's message is the
		// server's, verbatim — so it reaches the model as a result with the server's taint.
		text := "[the server returned an error]\n" + err.Error()
		return domain.Result{Summary: "the server returned an error", Text: text, Tainted: t.tainted}, nil
	}
	text := res.Text
	if res.IsError {
		text = "[the server reported that the call failed]\n" + text
	}
	summary, _, _ := strings.Cut(text, "\n")
	return domain.Result{Summary: summary, Text: text, Tainted: t.tainted}, nil
}
