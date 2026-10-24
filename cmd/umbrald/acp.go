package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	acpsdk "github.com/ecrespo/umbral/internal/acp/adapters/sdk"
	acpdomain "github.com/ecrespo/umbral/internal/acp/domain"
	acpports "github.com/ecrespo/umbral/internal/acp/ports"
	agentsdomain "github.com/ecrespo/umbral/internal/agents/domain"
	agentsports "github.com/ecrespo/umbral/internal/agents/ports"
	"github.com/ecrespo/umbral/internal/config"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
	"github.com/ecrespo/umbral/internal/sessions"
	"github.com/ecrespo/umbral/internal/tools/adapters/builtin"
)

// newACP builds the host of `[[acp.agents]]` (T-F2-07). Each thread's agent gets its isolated
// home under `acp/` of the daemon's runtime directory, next to the socket, so nothing of it
// outlives the machine's session. awaiting is the runtime's word on a pending approval, which
// is built after the host and so is reached through a function. The files and terminals a turn
// allows are performed by the file tools' own code and in sessions the thread owns (T-F2-08).
func newACP(settings config.ACP, runtimeDir string, providers *providerConfig, sessions *sessions.Service, awaiting func(string) bool, logger *slog.Logger) (*acpsdk.Host, error) {
	return acpsdk.New(acpsdk.Config{
		Entries:        settings.Agents,
		Dir:            filepath.Join(runtimeDir, "acp"),
		Secrets:        acpSecrets{providers: providers},
		Files:          acpFiles{},
		Terminals:      acpTerminals{sessions: sessions},
		MaxTerminals:   settings.MaxTerminals,
		Pending:        awaiting,
		SilenceTimeout: time.Duration(settings.SilenceTimeoutS) * time.Second,
		Logger:         logger.With(slog.String("module", "acp")),
	})
}

// acpFiles performs a hosted agent's file requests with read_file's and write_file's guards
// (delta `2026-10-acp-mediation`, decision 6).
type acpFiles struct{}

func (acpFiles) Read(target string, line, limit int) (string, error) {
	return builtin.ReadText(target, line, limit)
}

func (acpFiles) Write(target, content string) error { return builtin.WriteText(target, content) }

// acpTerminals runs a hosted agent's terminals in sessions its thread owns (REQ-ACP-005).
type acpTerminals struct{ sessions *sessions.Service }

func (t acpTerminals) Start(ctx context.Context, threadID, cwd, command string) (acpports.Terminal, error) {
	term, err := t.sessions.StartTerminal(ctx, threadID, cwd, command)
	if err != nil {
		return nil, err
	}
	return acpTerminal{term}, nil
}

type acpTerminal struct{ *sessions.Terminal }

func (t acpTerminal) Output() (string, *acpdomain.TerminalExit) {
	out, ended := t.Terminal.Output()
	if ended == nil {
		return out, nil
	}
	return out, &acpdomain.TerminalExit{Code: ended.Block.ExitCode}
}

func (t acpTerminal) Wait(ctx context.Context) (acpdomain.TerminalExit, error) {
	run, err := t.Terminal.Wait(ctx)
	if err != nil {
		return acpdomain.TerminalExit{}, err
	}
	return acpdomain.TerminalExit{Code: run.Block.ExitCode}, nil
}

// acpSecrets resolves an entry's env_refs as an MCP server's are: the keyring, and the
// environment only under REQ-SEC-012's fallback.
type acpSecrets struct {
	providers *providerConfig
}

func (s acpSecrets) Resolve(ctx context.Context, raw string) (string, error) {
	kind, name, err := acpdomain.ParseRef(raw)
	if err != nil {
		return "", err
	}
	source := secdomain.SourceKeyring
	if kind == acpdomain.RefEnv {
		if !s.providers.envFallback(ctx) {
			return "", errors.New("env:<VAR> is accepted only where the keyring is unavailable and [secrets] allow_env = true (REQ-SEC-012)")
		}
		source = secdomain.SourceEnv
	}
	got := s.providers.credential(ctx, secdomain.CredentialRequest{ProviderID: "acp", Source: source, Ref: name})
	if got.Secret.IsZero() {
		return "", errors.New(got.Reason)
	}
	return got.Secret.Reveal(), nil
}

// runtimeAgents is the host as the agent runtime reaches it (agents ports.ExternalAgents).
type runtimeAgents struct{ host *acpsdk.Host }

var _ agentsports.ExternalAgents = runtimeAgents{}

func (a runtimeAgents) Check(_ context.Context, agent, dir string) error {
	return agentError(a.host.Check(agent, dir))
}

func (a runtimeAgents) Open(ctx context.Context, req agentsports.ExternalRequest) error {
	return agentError(a.host.Open(ctx, acpsdk.Request(req)))
}

func (a runtimeAgents) Prompt(ctx context.Context, req agentsports.ExternalRequest, text string, sink agentsports.ExternalSink) (agentsdomain.StopReason, error) {
	stop, err := a.host.Prompt(ctx, acpsdk.Request(req), text, acpSink{sink})
	return agentsdomain.StopReason(stop), agentError(err)
}

func (a runtimeAgents) Close(threadID string) { a.host.Close(threadID) }

// agentError carries the host's errors across in the runtime's terms: a refusal with its
// reason, and a failed agent as ErrAgentFailed. A write the sink refused passes as it is.
func agentError(err error) error {
	var refusal *acpdomain.Refusal
	switch {
	case err == nil:
		return nil
	case errors.As(err, &refusal):
		return &agentsports.AgentRefusal{Reason: refusal.Reason, Detail: refusal.Detail}
	case errors.Is(err, acpdomain.ErrAgent):
		return fmt.Errorf("%w: %w", agentsports.ErrAgentFailed, err)
	default:
		return err
	}
}

// acpSink hands the runtime's sink the host's reports, each tool call with its kind out of
// ACP's closed set and its risk.
type acpSink struct{ sink agentsports.ExternalSink }

func (s acpSink) Text(text string) error { return s.sink.Text(text) }
func (s acpSink) Thought(text string)    { s.sink.Thought(text) }

func (s acpSink) ToolCall(c acpdomain.ToolCall) error { return s.sink.ToolCall(externalCall(c)) }

func (s acpSink) Permission(ctx context.Context, c acpdomain.ToolCall) (bool, error) {
	return s.sink.Permission(ctx, externalCall(c))
}

func (s acpSink) Request(ctx context.Context, req acpdomain.Request, perform func(context.Context, string) (string, error)) error {
	return s.sink.Request(ctx, agentsports.ExternalOp(req), perform)
}

// externalCall is a hosted call in the runtime's terms: its kind out of ACP's closed set, its
// risk, and the targets its raw input names.
func externalCall(c acpdomain.ToolCall) agentsports.ExternalToolCall {
	kind := acpdomain.Kind(c.Kind)
	in := acpdomain.ParseInput(c.RawInput)
	return agentsports.ExternalToolCall{
		ID: c.ID, Title: c.Title, Kind: kind, Risk: string(acpdomain.Risk(kind)), Status: c.Status,
		RawInput: c.RawInput, Output: c.Output, Locations: c.Locations,
		Command: in.Command, Cwd: in.Cwd, URL: in.URL,
	}
}
