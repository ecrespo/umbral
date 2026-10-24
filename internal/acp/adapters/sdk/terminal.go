package sdk

import (
	"context"
	"fmt"
	"path/filepath"
	"unicode/utf8"

	"github.com/coder/acp-go-sdk"

	"github.com/ecrespo/umbral/internal/acp/domain"
	"github.com/ecrespo/umbral/internal/acp/ports"
)

// defaultOutputLimit is what `terminal/output` returns at most when the agent set no
// outputByteLimit (delta `2026-10-acp-mediation`, decision 8).
const defaultOutputLimit = 1 << 20

// terminal is one of the agent's terminals: the session Umbral runs it in, and the most of its
// output the agent asked to see.
type terminal struct {
	term  ports.Terminal
	limit int
}

// errTerminalCode is the JSON-RPC code of a terminal request Umbral refuses.
const errTerminalCode = -32000

func terminalError(format string, a ...any) error {
	return &acp.RequestError{Code: errTerminalCode, Message: fmt.Sprintf(format, a...)}
}

// reserveTerminal takes one of the thread's terminal places, or refuses past the cap
// (REQ-ACP-005). The place is the caller's to give back with unreserve or to fill with add.
func (p *process) reserveTerminal() error {
	p.termMu.Lock()
	defer p.termMu.Unlock()
	if max := p.h.cfg.MaxTerminals; len(p.terminals)+p.opening >= max {
		return terminalError("this thread already has %d terminals, the most [acp] max_terminals allows; release one first", max)
	}
	p.opening++
	return nil
}

func (p *process) unreserve() {
	p.termMu.Lock()
	p.opening--
	p.termMu.Unlock()
}

func (p *process) addTerminal(t *terminal) string {
	p.termMu.Lock()
	defer p.termMu.Unlock()
	p.opening--
	p.nextTerm++
	id := fmt.Sprintf("term_%d", p.nextTerm)
	p.terminals[id] = t
	return id
}

func (p *process) terminal(id string) (*terminal, error) {
	p.termMu.Lock()
	defer p.termMu.Unlock()
	t, ok := p.terminals[id]
	if !ok {
		return nil, terminalError("no terminal %q", id)
	}
	return t, nil
}

func (p *process) removeTerminal(id string) *terminal {
	p.termMu.Lock()
	defer p.termMu.Unlock()
	t := p.terminals[id]
	delete(p.terminals, id)
	return t
}

// releaseTerminals releases every terminal of an agent that is gone. It is not a request's to
// cancel: whatever ended the agent, its terminals' sessions must close.
//
//nolint:contextcheck,nolintlint // contextcheck reads this at its callers: a release does not belong to the request that ended the agent
func (p *process) releaseTerminals() {
	p.termMu.Lock()
	all := p.terminals
	p.terminals = map[string]*terminal{}
	p.termMu.Unlock()
	for _, t := range all {
		_ = t.term.Release(context.Background())
	}
}

// terminalCommand is the request's command line, its policy line and its directory, or why
// there is none.
func (p *process) terminalCommand(req acp.CreateTerminalRequest) (command, policy, cwd string, err error) {
	env := make([][2]string, 0, len(req.Env))
	for _, v := range req.Env {
		if !domain.ValidEnvName(v.Name) {
			return "", "", "", terminalError("%q is not a variable name", v.Name)
		}
		env = append(env, [2]string{v.Name, v.Value})
	}
	if err := domain.CheckTerminal(req.Command, req.Args, env); err != nil {
		return "", "", "", terminalError("%v", err)
	}
	cwd = p.dir
	if req.Cwd != nil && *req.Cwd != "" {
		cwd = *req.Cwd
		if !filepath.IsAbs(cwd) {
			cwd = filepath.Join(p.dir, cwd)
		}
	}
	return domain.CommandLine(req.Command, req.Args, env), domain.PolicyLine(req.Command, req.Args, env), filepath.Clean(cwd), nil
}

// tail is the end of s in at most limit bytes, cut on a character boundary.
func tail(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	s = s[len(s)-limit:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s, true
}
