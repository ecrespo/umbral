// Command acp-spike is T-F2-00's throwaway: it hosts one ACP agent (OpenCode by default) over
// stdio with github.com/coder/acp-go-sdk, runs a few turns against a scratch repository and logs
// every frame in both directions, so the F2 ACP tasks are written against observed behaviour.
//
// It is not product code, and it is not a security boundary: it grants every permission once and
// runs whatever command the agent asks for, because observing that is the point. Run it only with
// an isolated HOME and XDG directories over a scratch repository, as the spike report describes.
// Umbral's own client decides each request with the policy engine instead (REQ-ACP-003..005).
//
// It lives in its own module so the SDK does not enter Umbral's go.mod
// before Q-F2-05 is decided.
//
//	go run . -work /tmp/umb-acp/work -log transcript.jsonl -- opencode acp
package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/acp-go-sdk"
)

// tap copies every newline-delimited frame it sees to the transcript, tagged with a direction.
type tap struct {
	mu  *sync.Mutex
	out io.Writer
	dir string
	buf bytes.Buffer
}

func (t *tap) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(p)
	for {
		line, err := t.buf.ReadBytes('\n')
		if err != nil {
			t.buf.Write(line)
			break
		}
		fmt.Fprintf(t.out, "{\"t\":%q,\"dir\":%q,\"frame\":%s}\n",
			time.Now().UTC().Format(time.RFC3339Nano), t.dir, bytes.TrimSpace(line))
	}
	return len(p), nil
}

// client answers the agent's requests. Files are confined to the work directory, permissions are
// granted once, and terminals run for real so the transcript shows what an agent expects back.
type client struct {
	work  string
	notes func(format string, a ...any)

	mu    sync.Mutex
	terms map[string]*term
	n     int
}

type term struct {
	out  bytes.Buffer
	done chan struct{}
	code int
	cmd  *exec.Cmd
	mu   sync.Mutex
}

func (t *term) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.out.Write(p)
}

// inside resolves symlinks on both sides, so a link in the work directory cannot lead out of it.
// A path that does not exist yet is judged by its parent directory.
func (c *client) inside(p string) error {
	work, err := filepath.EvalSymlinks(c.work)
	if err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(p))
	if errors.Is(err, os.ErrNotExist) {
		var dir string
		dir, err = filepath.EvalSymlinks(filepath.Dir(filepath.Clean(p)))
		real = filepath.Join(dir, filepath.Base(p))
	}
	if err != nil {
		return fmt.Errorf("outside the work directory: %s", p)
	}
	rel, err := filepath.Rel(work, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return fmt.Errorf("outside the work directory: %s", p)
	}
	return nil
}

func (c *client) RequestPermission(_ context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	title := ""
	if p.ToolCall.Title != nil {
		title = *p.ToolCall.Title
	}
	var kinds []string
	for _, o := range p.Options {
		kinds = append(kinds, fmt.Sprintf("%s=%s", o.OptionId, o.Kind))
	}
	c.notes("permission requested: %q options %v", title, kinds)
	for _, o := range p.Options {
		if o.Kind == acp.PermissionOptionKindAllowOnce {
			return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{
				Selected: &acp.RequestPermissionOutcomeSelected{OptionId: o.OptionId},
			}}, nil
		}
	}
	return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}}}, nil
}

func (c *client) SessionUpdate(_ context.Context, n acp.SessionNotification) error {
	u := n.Update
	switch {
	case u.ToolCall != nil:
		c.notes("tool call %s %q kind=%v status=%v", u.ToolCall.ToolCallId, u.ToolCall.Title, u.ToolCall.Kind, u.ToolCall.Status)
	case u.ToolCallUpdate != nil:
		c.notes("tool call update %s status=%v", u.ToolCallUpdate.ToolCallId, u.ToolCallUpdate.Status)
	case u.Plan != nil:
		c.notes("plan with %d entries", len(u.Plan.Entries))
	}
	return nil
}

func (c *client) ReadTextFile(_ context.Context, p acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	c.notes("fs/read_text_file %s", p.Path)
	if err := c.inside(p.Path); err != nil {
		return acp.ReadTextFileResponse{}, err
	}
	b, err := os.ReadFile(p.Path)
	if err != nil {
		return acp.ReadTextFileResponse{}, err
	}
	return acp.ReadTextFileResponse{Content: string(b)}, nil
}

func (c *client) WriteTextFile(_ context.Context, p acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	c.notes("fs/write_text_file %s (%d bytes)", p.Path, len(p.Content))
	if err := c.inside(p.Path); err != nil {
		return acp.WriteTextFileResponse{}, err
	}
	if err := os.MkdirAll(filepath.Dir(p.Path), 0o755); err != nil {
		return acp.WriteTextFileResponse{}, err
	}
	return acp.WriteTextFileResponse{}, os.WriteFile(p.Path, []byte(p.Content), 0o644)
}

func (c *client) CreateTerminal(_ context.Context, p acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	c.notes("terminal/create %s %v cwd=%v", p.Command, p.Args, p.Cwd)
	cmd := exec.Command(p.Command, p.Args...) //nolint:gosec // spike: the agent's own command, in a scratch directory
	cmd.Dir = c.work
	if p.Cwd != nil {
		if err := c.inside(*p.Cwd); err != nil {
			return acp.CreateTerminalResponse{}, err
		}
		cmd.Dir = *p.Cwd
	}
	t := &term{done: make(chan struct{}), cmd: cmd}
	cmd.Stdout, cmd.Stderr = t, t
	if err := cmd.Start(); err != nil {
		return acp.CreateTerminalResponse{}, err
	}
	go func() {
		err := cmd.Wait()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			t.code = ee.ExitCode()
		}
		close(t.done)
	}()
	c.mu.Lock()
	c.n++
	id := fmt.Sprintf("term-%d", c.n)
	if c.terms == nil {
		c.terms = map[string]*term{}
	}
	c.terms[id] = t
	c.mu.Unlock()
	return acp.CreateTerminalResponse{TerminalId: id}, nil
}

func (c *client) get(id string) (*term, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.terms[id]
	if !ok {
		return nil, fmt.Errorf("unknown terminal %s", id)
	}
	return t, nil
}

func (c *client) TerminalOutput(_ context.Context, p acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	c.notes("terminal/output %s", p.TerminalId)
	t, err := c.get(p.TerminalId)
	if err != nil {
		return acp.TerminalOutputResponse{}, err
	}
	t.mu.Lock()
	out := t.out.String()
	t.mu.Unlock()
	resp := acp.TerminalOutputResponse{Output: out}
	select {
	case <-t.done:
		code := t.code
		resp.ExitStatus = &acp.TerminalExitStatus{ExitCode: &code}
	default:
	}
	return resp, nil
}

func (c *client) WaitForTerminalExit(ctx context.Context, p acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	c.notes("terminal/wait_for_exit %s", p.TerminalId)
	t, err := c.get(p.TerminalId)
	if err != nil {
		return acp.WaitForTerminalExitResponse{}, err
	}
	select {
	case <-t.done:
	case <-ctx.Done():
		return acp.WaitForTerminalExitResponse{}, ctx.Err()
	}
	code := t.code
	return acp.WaitForTerminalExitResponse{ExitCode: &code}, nil
}

func (c *client) KillTerminal(_ context.Context, p acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	c.notes("terminal/kill %s", p.TerminalId)
	t, err := c.get(p.TerminalId)
	if err != nil {
		return acp.KillTerminalResponse{}, err
	}
	_ = t.cmd.Process.Kill()
	return acp.KillTerminalResponse{}, nil
}

func (c *client) ReleaseTerminal(_ context.Context, p acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	c.notes("terminal/release %s", p.TerminalId)
	return acp.ReleaseTerminalResponse{}, nil
}

func main() {
	work := flag.String("work", "", "scratch directory the agent works in (required)")
	logPath := flag.String("log", "transcript.jsonl", "where every frame is written")
	cancelAfter := flag.Duration("cancel-after", 3*time.Second, "how long the third turn runs before session/cancel")
	flag.Parse()
	if *work == "" || flag.NArg() == 0 {
		log.Fatal("usage: acp-spike -work DIR [-log FILE] -- AGENT [ARGS…]")
	}
	f, err := os.Create(*logPath)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	var mu sync.Mutex
	notes := func(format string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		msg := fmt.Sprintf(format, a...)
		fmt.Fprintf(f, "{\"t\":%q,\"note\":%q}\n", time.Now().UTC().Format(time.RFC3339Nano), msg)
		log.Print(msg)
	}

	ctx, stop := context.WithTimeout(context.Background(), 15*time.Minute)
	defer stop()
	cmd := exec.CommandContext(ctx, flag.Arg(0), flag.Args()[1:]...) //nolint:gosec // spike
	cmd.Dir = *work
	stderr, _ := os.Create(*logPath + ".stderr")
	defer stderr.Close()
	cmd.Stderr = stderr
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		log.Fatal(err)
	}
	toAgent := io.MultiWriter(in, &tap{mu: &mu, out: f, dir: "client->agent"})
	fromAgent := io.TeeReader(out, &tap{mu: &mu, out: f, dir: "agent->client"})

	c := &client{work: *work, notes: notes}
	conn := acp.NewClientSideConnection(c, toAgent, bufio.NewReader(fromAgent))

	t0 := time.Now()
	ini, err := conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{
			Fs:       acp.FileSystemCapabilities{ReadTextFile: true, WriteTextFile: true},
			Terminal: true,
		},
	})
	if err != nil {
		log.Fatalf("initialize: %v", err)
	}
	notes("initialize: protocol %v in %s", ini.ProtocolVersion, time.Since(t0))

	sess, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: *work, McpServers: []acp.McpServer{}})
	if err != nil {
		log.Fatalf("session/new: %v", err)
	}
	notes("session/new: %s", sess.SessionId)

	prompt := func(text string) {
		t := time.Now()
		resp, err := conn.Prompt(ctx, acp.PromptRequest{SessionId: sess.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock(text)}})
		if err != nil {
			notes("session/prompt %q: error %v after %s", text, err, time.Since(t))
			return
		}
		notes("session/prompt %q: stop_reason=%s after %s", text, resp.StopReason, time.Since(t))
	}
	prompt("Reply with the single word pong and nothing else.")
	prompt("Create a file hello.txt in the current directory containing the line hi, then run `cat hello.txt` in a shell and tell me its output.")

	// The third turn is cancelled mid-flight to see how the agent ends it.
	done := make(chan struct{})
	go func() {
		defer close(done)
		prompt("Count slowly from 1 to 200, one number per line, explaining each number.")
	}()
	time.Sleep(*cancelAfter)
	t := time.Now()
	if err := conn.Cancel(ctx, acp.CancelNotification{SessionId: sess.SessionId}); err != nil {
		notes("session/cancel: %v", err)
	}
	select {
	case <-done:
		notes("cancelled turn ended %s after session/cancel", time.Since(t))
	case <-time.After(30 * time.Second):
		notes("cancelled turn had not ended 30 s after session/cancel")
	}
	_ = in.Close()
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}
