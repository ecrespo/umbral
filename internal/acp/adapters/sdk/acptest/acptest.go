// Package acptest is an ACP agent for tests, written with the same SDK Umbral's client uses: a
// test binary re-executed as a child process speaks ACP on stdio and acts out what each prompt
// asks for. The agent's environment is an allowlist (REQ-ACP-009), so the binary is told to be
// the agent by its arguments, not by a variable.
package acptest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
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

// Time is the prompt word that measures Umbral's answer to fs/read_text_file (NFR-F2-03).
const Time = "time"

// timeReads makes n reads of path and summarizes how long each took to be answered.
func (a *agent) timeReads(ctx context.Context, sid acp.SessionId, n, path string) string {
	count, err := strconv.Atoi(n)
	if err != nil || count < 1 {
		return "error: time needs a count"
	}
	took := make([]time.Duration, 0, count)
	for range count {
		start := time.Now()
		if _, err := a.conn.ReadTextFile(ctx, acp.ReadTextFileRequest{SessionId: sid, Path: path}); err != nil {
			return "error: " + err.Error()
		}
		took = append(took, time.Since(start))
	}
	slices.Sort(took)
	at := func(pct int) time.Duration { return took[max((len(took)*pct+99)/100-1, 0)] }
	return fmt.Sprintf("p50=%dus p95=%dus max=%dus", at(50).Microseconds(), at(95).Microseconds(), took[len(took)-1].Microseconds())
}

// StderrSecret is what `crash` writes to stderr and `fail` puts in its error, which must reach
// no log and no client.
const StderrSecret = "acptest-stderr-credential-marker" //nolint:gosec // a marker, not a credential

// Prompt acts out the prompt's first word:
//
//	report          answers a Report as JSON
//	stream          a thought, two text chunks and an `execute` tool call that completes
//	crash           a text chunk, StderrSecret on stderr, then the process exits 3
//	fail            a JSON-RPC error whose message and data hold StderrSecret
//	silent          nothing, until cancelled
//	garbage         a line that is not JSON-RPC, then nothing
//	ask             a permission request; answers allowed, rejected or cancelled
//	read PATH       fs/read_text_file; answers the content or the error
//	time N PATH     N fs/read_text_file of PATH, one after another; answers how long Umbral
//	                took to answer them, `p50=…us p95=…us max=…us`, or the first error
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
	case "fail":
		// What a provider's error tends to say, as the agent's own JSON-RPC error.
		return acp.PromptResponse{}, &acp.RequestError{
			Code: -32001, Message: "upstream 401: invalid api key " + StderrSecret,
			Data: map[string]any{"key": StderrSecret},
		}
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
	case "edit":
		// Asks to edit fields[1], writes fields[2:] joined to it and reads it back.
		path, content := arg(fields, 1), strings.Join(fields[min(2, len(fields)):], " ")
		resp, err := a.conn.RequestPermission(ctx, acp.RequestPermissionRequest{
			SessionId: sid,
			ToolCall: acp.ToolCallUpdate{
				ToolCallId: "call-edit", Title: acp.Ptr("edit " + path), Kind: acp.Ptr(acp.ToolKindEdit),
				Locations: []acp.ToolCallLocation{{Path: path}},
			},
			Options: []acp.PermissionOption{
				{OptionId: "once", Name: "Allow once", Kind: acp.PermissionOptionKindAllowOnce},
				{OptionId: "reject", Name: "Reject", Kind: acp.PermissionOptionKindRejectOnce},
			},
		})
		said := []string{"cancelled"}
		if err == nil && resp.Outcome.Selected != nil {
			said = []string{"selected " + string(resp.Outcome.Selected.OptionId)}
		}
		if _, err := a.conn.WriteTextFile(ctx, acp.WriteTextFileRequest{SessionId: sid, Path: path, Content: content}); err != nil {
			said = append(said, "write error "+errCode(err))
		} else {
			said = append(said, "written")
		}
		if r, err := a.conn.ReadTextFile(ctx, acp.ReadTextFileRequest{SessionId: sid, Path: path}); err != nil {
			said = append(said, "read error "+errCode(err))
		} else {
			said = append(said, "read "+r.Content)
		}
		a.say(ctx, sid, acp.UpdateAgentMessageText(strings.Join(said, "; ")))
	case "hold":
		// Runs `sleep 30` in a terminal and waits for it to exit, whatever cancels the turn: the
		// turn ends when the terminal does, saying how.
		resp, err := a.conn.CreateTerminal(context.WithoutCancel(ctx), acp.CreateTerminalRequest{SessionId: sid, Command: "sleep", Args: []string{"30"}})
		if err != nil {
			a.say(ctx, sid, acp.UpdateAgentMessageText("error "+errCode(err)))
			break
		}
		exit, err := a.conn.WaitForTerminalExit(context.WithoutCancel(ctx), acp.WaitForTerminalExitRequest{SessionId: sid, TerminalId: resp.TerminalId})
		if err != nil {
			a.say(context.WithoutCancel(ctx), sid, acp.UpdateAgentMessageText("wait error "+errCode(err)))
			break
		}
		a.say(context.WithoutCancel(ctx), sid, acp.UpdateAgentMessageText(fmt.Sprintf("exited %d", deref(exit.ExitCode))))
	case "deaf":
		// Ignores session/cancel: the turn never ends on its own.
		time.Sleep(time.Minute)
	case "terminals":
		// Creates N terminals running `sleep 9` and keeps them; says each id or error code.
		n, _ := strconv.Atoi(arg(fields, 1))
		var said []string
		for range n {
			resp, err := a.conn.CreateTerminal(ctx, acp.CreateTerminalRequest{SessionId: sid, Command: "sleep", Args: []string{"9"}})
			if err != nil {
				said = append(said, "error "+errCode(err))
				continue
			}
			said = append(said, resp.TerminalId)
		}
		a.say(ctx, sid, acp.UpdateAgentMessageText(strings.Join(said, " ")))
	case "release":
		_, err := a.conn.ReleaseTerminal(ctx, acp.ReleaseTerminalRequest{SessionId: sid, TerminalId: arg(fields, 1)})
		answer := "released"
		if err != nil {
			answer = "error " + errCode(err)
		}
		a.say(ctx, sid, acp.UpdateAgentMessageText(answer))
	case "term":
		// Runs fields[1] with fields[2:] in a terminal of 8 bytes of output, waits, reads and
		// releases it.
		limit := 8
		resp, err := a.conn.CreateTerminal(ctx, acp.CreateTerminalRequest{
			SessionId: sid, Command: arg(fields, 1), Args: fields[min(2, len(fields)):], OutputByteLimit: &limit,
			Env: []acp.EnvVariable{{Name: "GREETING", Value: "a b"}},
		})
		if err != nil {
			a.say(ctx, sid, acp.UpdateAgentMessageText("error "+errCode(err)))
			break
		}
		exit, _ := a.conn.WaitForTerminalExit(ctx, acp.WaitForTerminalExitRequest{SessionId: sid, TerminalId: resp.TerminalId})
		out, _ := a.conn.TerminalOutput(ctx, acp.TerminalOutputRequest{SessionId: sid, TerminalId: resp.TerminalId})
		_, _ = a.conn.ReleaseTerminal(ctx, acp.ReleaseTerminalRequest{SessionId: sid, TerminalId: resp.TerminalId})
		a.say(ctx, sid, acp.UpdateAgentMessageText(fmt.Sprintf("exit=%d output=%s truncated=%v", deref(exit.ExitCode), out.Output, out.Truncated)))
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
	case "orphan":
		// Leaves a child behind that holds the agent's stderr, answers its pid, ends the turn,
		// and exits between turns, when no request of Umbral's is waiting on it.
		// With "group" the child moves to a process group of its own, still in the agent's
		// session, so a kill of the agent's group alone misses it.
		child := exec.Command("sleep", "60") //nolint:noctx // outlives the agent on purpose
		child.Stderr = os.Stderr
		if arg(fields, 1) == "group" {
			child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		}
		if err := child.Start(); err != nil {
			a.say(ctx, sid, acp.UpdateAgentMessageText("error: "+err.Error()))
			return end(acp.StopReasonEndTurn)
		}
		a.say(ctx, sid, acp.UpdateAgentMessageText(strconv.Itoa(child.Process.Pid)))
		go func() {
			time.Sleep(200 * time.Millisecond)
			os.Exit(0)
		}()
	case "dial":
		// Calls fields[1] on the daemon's socket as a `cli` client, the way a hosted agent that
		// went looking for it would, and answers the method's error code or "ok".
		a.say(ctx, sid, acp.UpdateAgentMessageText(dialDaemon(ctx, arg(fields, 1))))
	case Time:
		a.say(ctx, sid, acp.UpdateAgentMessageText(a.timeReads(ctx, sid, arg(fields, 1), arg(fields, 2))))
	case "write":
		// Writes fields[2:] joined to the path fields[1].
		_, err := a.conn.WriteTextFile(ctx, acp.WriteTextFileRequest{SessionId: sid, Path: arg(fields, 1), Content: strings.Join(fields[min(2, len(fields)):], " ")})
		answer := "written"
		if err != nil {
			answer = "error: " + err.Error()
		}
		a.say(ctx, sid, acp.UpdateAgentMessageText(answer))
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
	// Where OpenCode looks on every platform: XDG_CONFIG_HOME, else ~/.config. os.UserConfigDir
	// is ~/Library/Application Support on macOS, which OpenCode never reads.
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".config")
		}
	}
	if dir != "" {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			r.GlobalConfig = append(r.GlobalConfig, e.Name())
		}
	}
	blob, _ := json.Marshal(r)
	return string(blob)
}

func arg(fields []string, i int) string {
	if i < len(fields) {
		return fields[i]
	}
	return ""
}

func errCode(err error) string {
	var re *acp.RequestError
	if errors.As(err, &re) {
		return strconv.Itoa(re.Code)
	}
	return err.Error()
}

func deref(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}

// dialDaemon finds the daemon from the agent's own HOME — the isolated home sits at
// <runtime dir>/acp/<thread>/home, beside the socket and its token — and calls method with
// empty params after the handshake. It answers the error code, "ok", or "dial: …" when the
// socket cannot be reached at all.
func dialDaemon(ctx context.Context, method string) string {
	runtimeDir := filepath.Dir(filepath.Dir(filepath.Dir(os.Getenv("HOME"))))
	token, err := os.ReadFile(filepath.Join(runtimeDir, "token")) //nolint:gosec // the test daemon's own token
	if err != nil {
		return "dial: " + err.Error()
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(runtimeDir, "umbral.sock"))
	if err != nil {
		return "dial: " + err.Error()
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	lines := bufio.NewScanner(conn)
	lines.Buffer(nil, 4<<20)
	call := func(id int, method string, params any) string {
		line, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		if _, err := conn.Write(append(line, '\n')); err != nil {
			return "dial: " + err.Error()
		}
		if !lines.Scan() {
			return "dial: no reply"
		}
		var reply struct {
			Error *struct {
				Data struct {
					DomainCode string `json:"domain_code"`
				} `json:"data"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(lines.Bytes(), &reply); err != nil {
			return "dial: " + err.Error()
		}
		if reply.Error != nil {
			if reply.Error.Data.DomainCode != "" {
				return reply.Error.Data.DomainCode
			}
			return reply.Error.Message
		}
		return "ok"
	}
	hello := map[string]any{
		"token": strings.TrimSpace(string(token)), "client_kind": "cli",
		"client_version": "0.1.0-acptest", "protocol_version": 1,
	}
	if got := call(1, "system.hello", hello); got != "ok" {
		return "dial: hello: " + got
	}
	return call(2, method, map[string]any{})
}
