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
	agentsdomain "github.com/ecrespo/umbral/internal/agents/domain"
	agentsports "github.com/ecrespo/umbral/internal/agents/ports"
	"github.com/ecrespo/umbral/internal/config"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
)

// newACP builds the host of `[[acp.agents]]` (T-F2-07). Each thread's agent gets its isolated
// home under `acp/` of the daemon's runtime directory, next to the socket, so nothing of it
// outlives the machine's session. awaiting is the runtime's word on a pending approval, which
// is built after the host and so is reached through a function.
func newACP(settings config.ACP, runtimeDir string, providers *providerConfig, awaiting func(string) bool, logger *slog.Logger) (*acpsdk.Host, error) {
	return acpsdk.New(acpsdk.Config{
		Entries:        settings.Agents,
		Dir:            filepath.Join(runtimeDir, "acp"),
		Secrets:        acpSecrets{providers: providers},
		Pending:        awaiting,
		SilenceTimeout: time.Duration(settings.SilenceTimeoutS) * time.Second,
		Logger:         logger.With(slog.String("module", "acp")),
	})
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

func (s acpSink) ToolCall(c acpdomain.ToolCall) error {
	kind := acpdomain.Kind(c.Kind)
	return s.sink.ToolCall(agentsports.ExternalToolCall{
		ID: c.ID, Title: c.Title, Kind: kind, Risk: string(acpdomain.Risk(kind)), Status: c.Status,
		RawInput: c.RawInput, Output: c.Output,
	})
}
