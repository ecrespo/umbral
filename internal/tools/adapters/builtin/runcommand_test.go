package builtin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/tools/domain"
)

type fakeTerminal struct {
	thread, cwd, command string
	run                  sessdomain.AgentRun
	wait                 bool
	deadline             time.Time
}

func (f *fakeTerminal) RunForThread(ctx context.Context, threadID, cwd, command string) (sessdomain.AgentRun, error) {
	f.thread, f.cwd, f.command = threadID, cwd, command
	f.deadline, _ = ctx.Deadline()
	if f.wait {
		<-ctx.Done()
		return f.run, ctx.Err()
	}
	return f.run, nil
}

func exit(code int) *int { return &code }

// TestRunCommandRunsInTheThreadsTerminal_REQ_AGT_003: run_command hands the command to the
// thread's terminal with the thread and cwd, and returns the exit code and the tail of the
// output; a command that outlives its timeout is stopped and says so; with no thread or no
// terminal it runs nothing.
func TestRunCommandRunsInTheThreadsTerminal_REQ_AGT_003(t *testing.T) {
	t.Parallel()

	term := &fakeTerminal{run: sessdomain.AgentRun{
		Block:  sessdomain.Block{Origin: sessdomain.OriginAgent, ExitCode: exit(2), State: sessdomain.BlockFinished},
		Output: strings.Repeat("x", runMaxOutput+10) + "FAIL: TestFoo",
	}}
	tool := runCommand{term: term}
	env := domain.Env{ThreadID: "thr_1", Cwd: "/work"}
	res, err := tool.Run(context.Background(), env, in(map[string]any{"command": "go test ./..."}))
	if err != nil {
		t.Fatal(err)
	}
	if term.thread != "thr_1" || term.cwd != "/work" || term.command != "go test ./..." {
		t.Errorf("terminal got %q %q %q", term.thread, term.cwd, term.command)
	}
	if res.Summary != "exit 2" || !strings.HasSuffix(res.Text, "FAIL: TestFoo\n[exit 2]\n") ||
		!strings.HasPrefix(res.Text, "[the first 23 bytes of output are not shown]") || res.Tainted ||
		len(res.Text) > runMaxOutput+60 {
		t.Errorf("result summary %q, text starts %q", res.Summary, res.Text[:60])
	}
	if until := time.Until(term.deadline); until < 110*time.Second || until > 121*time.Second {
		t.Errorf("default timeout left %v, want about 120 s", until)
	}

	slow := &fakeTerminal{wait: true, run: sessdomain.AgentRun{Output: "partial"}}
	res, err = runCommand{term: slow}.Run(context.Background(), env, in(map[string]any{"command": "sleep 9", "timeout_seconds": 1}))
	if err != nil || res.Summary != "stopped after 1s" || !strings.Contains(res.Text, "partial") {
		t.Errorf("timed out: %+v, %v", res, err)
	}

	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := (runCommand{term: &fakeTerminal{wait: true}}).Run(cancelled, env, in(map[string]any{"command": "x"})); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled turn: err = %v, want context.Canceled", err)
	}
	if _, err := (runCommand{term: term}).Run(context.Background(), domain.Env{Cwd: "/w"}, in(map[string]any{"command": "x"})); !errors.Is(err, domain.ErrInvalidEnv) {
		t.Errorf("no thread: err = %v", err)
	}
	if _, err := (runCommand{}).Run(context.Background(), env, in(map[string]any{"command": "x"})); err == nil {
		t.Error("ran with no terminal")
	}
	a, _ := runCommand{}.Action(domain.Env{ThreadID: "thr_1", Cwd: "/w", WriteRoot: "/w"}, in(map[string]any{"command": "rm -rf /"}))
	if a.Target != "rm -rf /" || a.Tool != "run_command" {
		t.Errorf("action = %+v", a)
	}
}
