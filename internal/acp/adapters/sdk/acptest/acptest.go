// Package acptest is an ACP agent for tests, written with the same SDK Umbral's client uses: a
// test binary re-executed as a child process speaks ACP on stdio and acts out what each prompt
// asks for. The agent's environment is an allowlist (REQ-ACP-009), so the binary is told to be
// the agent by its arguments, not by a variable.
package acptest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/acp-go-sdk"
)

// Arg is the first argument that makes a test binary the agent: Serve, called from TestMain,
// checks it.
const Arg = "umbral-acptest-agent"

// FailOnce is the argument that, followed by a path, makes the agent's first launch exit before it speaks ACP.
const FailOnce = "--fail-once"

// Args are the arguments that launch the test binary as the agent.
func Args() []string { return []string{Arg} }

// Report is what the `report` prompt answers, as JSON: what the agent's process saw.
type Report struct {
	Cwd        string   `json:"cwd"`
	SessionCwd string   `json:"session_cwd"`
	Env        []string `json:"env"`
	// Config is the file the manifest's variable names, "" when it names none.
	Config string `json:"config"`
	// Capabilities are the client capabilities `initialize` advertised.
	ReadFile, WriteFile, Terminal bool
	// GlobalConfig lists what the agent found under the user's real config directory, which it
	// must not be able to reach: its HOME and XDG_CONFIG_HOME point elsewhere.
	GlobalConfig []string `json:"global_config"`
}

// Serve runs the agent and exits when os.Args say so; otherwise it returns.
func Serve() {
	if len(os.Args) < 2 || os.Args[1] != Arg {
		return
	}
	if len(os.Args) == 4 && os.Args[2] == FailOnce {
		// The first launch exits before the handshake, leaving the file behind; later ones run.
		if _, err := os.Stat(os.Args[3]); err != nil { //nolint:gosec // the test's own marker path
			_ = os.WriteFile(os.Args[3], nil, 0o600) //nolint:gosec // the test's own marker path
			os.Exit(2)
		}
	}
	out := &lockedWriter{w: os.Stdout}
	a := &agent{out: out, prompts: map[string]context.CancelFunc{}}
	conn := acp.NewAgentSideConnection(a, out, os.Stdin)
	a.conn = conn
	<-conn.Done()
	os.Exit(0)
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

type agent struct {
	conn *acp.AgentSideConnection
	out  *lockedWriter

	mu         sync.Mutex
	caps       acp.ClientCapabilities
	sessionCwd string
	prompts    map[string]context.CancelFunc
}

var _ acp.Agent = (*agent)(nil)

func (a *agent) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

func (a *agent) Initialize(_ context.Context, p acp.InitializeRequest) (acp.InitializeResponse, error) {
	a.mu.Lock()
	a.caps = p.ClientCapabilities
	a.mu.Unlock()
	return acp.InitializeResponse{ProtocolVersion: acp.ProtocolVersionNumber}, nil
}

func (a *agent) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, nil
}

func (a *agent) Cancel(_ context.Context, p acp.CancelNotification) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cancel := a.prompts[string(p.SessionId)]; cancel != nil {
		cancel()
	}
	return nil
}

func (a *agent) CloseSession(context.Context, acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	return acp.CloseSessionResponse{}, nil
}

func (a *agent) ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, nil
}

func (a *agent) NewSession(_ context.Context, p acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	a.mu.Lock()
	a.sessionCwd = p.Cwd
	a.mu.Unlock()
	return acp.NewSessionResponse{SessionId: "sess-1"}, nil
}

func (a *agent) ResumeSession(context.Context, acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, nil
}

func (a *agent) SetSessionConfigOption(context.Context, acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, nil
}

func (a *agent) SetSessionMode(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}

func (a *agent) say(ctx context.Context, sid acp.SessionId, u acp.SessionUpdate) {
	_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: sid, Update: u})
}

// StderrSecret is what `crash` writes to stderr, which must reach no log and no client.
const StderrSecret = "acptest-stderr-credential-marker" //nolint:gosec // a marker, not a credential

// Prompt acts out the prompt's first word:
//
//	report          answers a Report as JSON
//	stream          a thought, two text chunks and an `execute` tool call that completes
//	crash           a text chunk, StderrSecret on stderr, then the process exits 3
//	silent          nothing, until cancelled
//	garbage         a line that is not JSON-RPC, then nothing
//	ask             a permission request; answers allowed, rejected or cancelled
//	read PATH       fs/read_text_file; answers the content or the error
//	slow MS N       N text chunks, MS milliseconds apart
//	stop REASON     ends the turn with that stop reason
//
// Anything else is echoed.
func (a *agent) Prompt(ctx context.Context, p acp.PromptRequest) (acp.PromptResponse, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.mu.Lock()
	a.prompts[string(p.SessionId)] = cancel
	a.mu.Unlock()

	var text string
	for _, b := range p.Prompt {
		if b.Text != nil {
			text += b.Text.Text
		}
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		fields = []string{""}
	}
	sid := p.SessionId
	end := func(r acp.StopReason) (acp.PromptResponse, error) {
		if ctx.Err() != nil {
			r = acp.StopReasonCancelled
		}
		return acp.PromptResponse{StopReason: r}, nil
	}
	switch fields[0] {
	case "report":
		a.say(ctx, sid, acp.UpdateAgentMessageText(a.report()))
	case "stream":
		a.say(ctx, sid, acp.UpdateAgentThoughtText("pondering"))
		a.say(ctx, sid, acp.UpdateAgentMessageText("hello "))
		a.say(ctx, sid, acp.UpdateAgentMessageText("world"))
		a.say(ctx, sid, acp.StartToolCall("call-1", "ls", acp.WithStartKind(acp.ToolKindExecute),
			acp.WithStartStatus(acp.ToolCallStatusPending), acp.WithStartRawInput(map[string]any{"command": "ls"})))
		a.say(ctx, sid, acp.UpdateToolCall("call-1", acp.WithUpdateStatus(acp.ToolCallStatusCompleted),
			acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock("a.txt"))})))
	case "crash":
		a.say(ctx, sid, acp.UpdateAgentMessageText("partial"))
		// What a provider's error tends to print: the key it rejected.
		fmt.Fprintln(os.Stderr, "401 Unauthorized: invalid api key "+StderrSecret)
		time.Sleep(50 * time.Millisecond)
		os.Exit(3)
	case "silent":
		<-ctx.Done()
	case "garbage":
		_, _ = a.out.Write([]byte("this is not json-rpc\n"))
		<-ctx.Done()
	case "ask":
		resp, err := a.conn.RequestPermission(ctx, acp.RequestPermissionRequest{
			SessionId: sid,
			ToolCall:  acp.ToolCallUpdate{ToolCallId: "call-ask", Title: acp.Ptr("touch x"), Kind: acp.Ptr(acp.ToolKindEdit)},
			Options: []acp.PermissionOption{
				{OptionId: "once", Name: "Allow once", Kind: acp.PermissionOptionKindAllowOnce},
				{OptionId: "always", Name: "Always allow", Kind: acp.PermissionOptionKindAllowAlways},
				{OptionId: "reject", Name: "Reject", Kind: acp.PermissionOptionKindRejectOnce},
			},
		})
		answer := "cancelled"
		switch {
		case err != nil:
			answer = "error: " + err.Error()
		case resp.Outcome.Selected != nil:
			answer = "selected " + string(resp.Outcome.Selected.OptionId)
		}
		a.say(ctx, sid, acp.UpdateAgentMessageText(answer))
	case "read":
		path := ""
		if len(fields) > 1 {
			path = fields[1]
		}
		resp, err := a.conn.ReadTextFile(ctx, acp.ReadTextFileRequest{SessionId: sid, Path: path})
		if err != nil {
			a.say(ctx, sid, acp.UpdateAgentMessageText("error: "+err.Error()))
		} else {
			a.say(ctx, sid, acp.UpdateAgentMessageText(resp.Content))
		}
	case "slow":
		ms, n := 100, 3
		if len(fields) > 2 {
			ms, _ = strconv.Atoi(fields[1])
			n, _ = strconv.Atoi(fields[2])
		}
		for i := range n {
			select {
			case <-time.After(time.Duration(ms) * time.Millisecond):
			case <-ctx.Done():
				return end(acp.StopReasonCancelled)
			}
			a.say(ctx, sid, acp.UpdateAgentMessageText(fmt.Sprintf("%d ", i)))
		}
	case "stop":
		reason := acp.StopReasonEndTurn
		if len(fields) > 1 {
			reason = acp.StopReason(fields[1])
		}
		return end(reason)
	default:
		a.say(ctx, sid, acp.UpdateAgentMessageText("echo: "+text))
	}
	return end(acp.StopReasonEndTurn)
}

func (a *agent) report() string {
	a.mu.Lock()
	r := Report{
		SessionCwd: a.sessionCwd, ReadFile: a.caps.Fs.ReadTextFile, WriteFile: a.caps.Fs.WriteTextFile,
		Terminal: a.caps.Terminal, Env: os.Environ(), GlobalConfig: []string{},
	}
	a.mu.Unlock()
	r.Cwd, _ = os.Getwd()
	if path := os.Getenv("OPENCODE_CONFIG"); path != "" {
		b, _ := os.ReadFile(path) // #nosec G304 G703 -- a test agent reading the file its manifest named
		r.Config = string(b)
	}
	if dir, err := os.UserConfigDir(); err == nil {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			r.GlobalConfig = append(r.GlobalConfig, e.Name())
		}
	}
	blob, _ := json.Marshal(r)
	return string(blob)
}
