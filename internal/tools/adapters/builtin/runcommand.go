package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	secdomain "github.com/ecrespo/umbral/internal/security/domain"
	sessionsports "github.com/ecrespo/umbral/internal/sessions/ports"
	"github.com/ecrespo/umbral/internal/tools/domain"
)

// run_command's limits: how long a command may run unless it asks for less or more, and how
// much of its output goes back to the model — the tail, where errors are.
const (
	runDefaultTimeout = 120 * time.Second
	runMaxTimeout     = 600
	runMaxOutput      = 64 << 10
)

// runCommand is run_command: one command line in the thread's own PTY, recorded as a block
// with origin agent (REQ-AGT-003). The terminal is the sessions module's; without one the
// tool runs nothing.
type runCommand struct {
	term sessionsports.AgentTerminal
}

type runInput struct {
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func (runCommand) Spec() domain.Spec {
	return domain.Spec{
		Name: "run_command",
		Description: "Run one shell command line in this thread's terminal and return its exit code and output " +
			"(the last 64 KiB). The shell keeps its state between calls, the working directory included. " +
			"One line only: join steps with && or ;.",
		Risk: secdomain.RiskExec,
		InputSchema: object([]string{"command"}, map[string]any{
			"command": nonEmpty("The command line."),
			"timeout_seconds": map[string]any{
				kType: "integer", "minimum": 1, "maximum": runMaxTimeout,
				kDescription: "Stop the command after this many seconds; 120 by default.",
			},
		}),
	}
}

func (runCommand) Action(env domain.Env, input json.RawMessage) (secdomain.Action, error) {
	var in runInput
	if err := decode(input, &in); err != nil {
		return secdomain.Action{}, err
	}
	return secdomain.Action{
		ThreadID: env.ThreadID, Tool: "run_command", Risk: secdomain.RiskExec, Target: in.Command,
		Cwd: realPath(env.Cwd), WriteRoot: realPath(env.WriteRoot),
	}, nil
}

func (runCommand) Summary(input json.RawMessage) string {
	var in runInput
	_ = json.Unmarshal(input, &in)
	return in.Command
}

func (r runCommand) Run(ctx context.Context, env domain.Env, input json.RawMessage) (domain.Result, error) {
	var in runInput
	if err := decode(input, &in); err != nil {
		return domain.Result{}, err
	}
	if r.term == nil {
		return domain.Result{}, errors.New("run_command has no terminal in this daemon")
	}
	if env.ThreadID == "" {
		return domain.Result{}, fmt.Errorf("%w: run_command runs in a thread's terminal and this call has no thread", domain.ErrInvalidEnv)
	}
	timeout := runDefaultTimeout
	if in.TimeoutSeconds > 0 {
		timeout = time.Duration(in.TimeoutSeconds) * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	run, err := r.term.RunForThread(runCtx, env.ThreadID, env.Cwd, in.Command)
	timedOut := err != nil && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded)
	if err != nil && !timedOut {
		return domain.Result{}, err
	}

	var text strings.Builder
	output := run.Output
	if len(output) > runMaxOutput {
		cut := len(output) - runMaxOutput
		for cut < len(output) && !utf8.RuneStart(output[cut]) {
			cut++
		}
		fmt.Fprintf(&text, "[the first %d bytes of output are not shown]\n", cut)
		output = output[cut:]
	}
	text.WriteString(output)
	if output != "" && !strings.HasSuffix(output, "\n") {
		text.WriteString("\n")
	}
	var summary string
	switch {
	case timedOut:
		summary = fmt.Sprintf("stopped after %v", timeout)
		fmt.Fprintf(&text, "[stopped: the command ran longer than %v]\n", timeout)
	case run.Block.ExitCode != nil:
		summary = fmt.Sprintf("exit %d", *run.Block.ExitCode)
		fmt.Fprintf(&text, "[exit %d]\n", *run.Block.ExitCode)
	default:
		summary = "ended without an exit code"
		fmt.Fprintf(&text, "[the command ended without an exit code: %s]\n", run.Block.State)
	}
	return domain.Result{Summary: summary, Text: text.String()}, nil
}
