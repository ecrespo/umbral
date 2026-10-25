package agents

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/agents/domain"
	"github.com/ecrespo/umbral/internal/agents/ports"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
)

// fakeHosted is a hosted agent whose turn is a script run against the turn's sink.
type fakeHosted struct {
	script func(ctx context.Context, req ports.ExternalRequest, sink ports.ExternalSink)
}

func (f *fakeHosted) Check(context.Context, string, string) error       { return nil }
func (f *fakeHosted) Open(context.Context, ports.ExternalRequest) error { return nil }
func (f *fakeHosted) Close(string)                                      {}
func (f *fakeHosted) Prompt(ctx context.Context, req ports.ExternalRequest, _ string, sink ports.ExternalSink) (domain.StopReason, error) {
	f.script(ctx, req, sink)
	return domain.StopEndTurn, nil
}

// performed records the targets a request's perform ran with.
type performed struct {
	mu      sync.Mutex
	targets []string
}

func (p *performed) fn(result string) func(context.Context, string) (string, error) {
	return func(_ context.Context, target string) (string, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.targets = append(p.targets, target)
		return result, nil
	}
}

func (p *performed) list() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.targets...)
}

// realDir is a temporary directory with its links resolved, as the policy compares paths.
func realDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// acpRig is a rig whose threads are hosted by script.
func acpRig(t *testing.T, wts *fakeWorktrees, script func(ctx context.Context, req ports.ExternalRequest, sink ports.ExternalSink)) *rig {
	t.Helper()
	r := newRigWith(t, func(c *Config) {
		c.Agents = &fakeHosted{script: script}
		if wts != nil {
			c.Worktrees = wts
		}
	})
	// The registry's command tool, whose action an execute and a terminal are decided by.
	r.tools.tools["run_command"] = &fakeTool{risk: secdomain.RiskExec, out: "ran"}
	return r
}

func (r *rig) row(t *testing.T, threadID, tool string) domain.ToolCall {
	t.Helper()
	calls, err := r.store.ToolCalls(t.Context(), threadID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range calls {
		if c.Tool == tool {
			return c
		}
	}
	t.Fatalf("no %s row among %+v", tool, calls)
	return domain.ToolCall{}
}

func (r *rig) noApproval(t *testing.T) {
	t.Helper()
	select {
	case a := <-r.bus.approvals:
		t.Fatalf("asked %s on %q (%s), want no approval", a.Tool, a.Summary, a.Reason)
	default:
	}
}

func (r *rig) respond(t *testing.T, a domain.Approval, approve bool) {
	t.Helper()
	if _, err := r.rt.Respond(t.Context(), domain.Response{ApprovalID: a.ID, Approve: approve, Scope: domain.ScopeOnce}); err != nil {
		t.Fatal(err)
	}
}

// TestACPPermissionEntersTheApprovalQueue_REQ_ACP_003: an agent's permission request is
// decided by the policy engine and, for an ask, enters Umbral's approval queue pointing at the
// reported call's row; the user's answer is the agent's.
func TestACPPermissionEntersTheApprovalQueue_REQ_ACP_003(t *testing.T) {
	dir := realDir(t)
	file := filepath.Join(dir, "a.go")
	answers := make(chan bool, 2)
	r := acpRig(t, nil, func(ctx context.Context, _ ports.ExternalRequest, sink ports.ExternalSink) {
		for _, id := range []string{"c1", "c2"} {
			call := ports.ExternalToolCall{ID: id, Kind: "edit", Risk: string(secdomain.RiskWriteFS), Status: "pending", Locations: []string{file}}
			if err := sink.ToolCall(call); err != nil {
				t.Error(err)
			}
			ok, err := sink.Permission(ctx, call)
			if err != nil {
				t.Error(err)
			}
			answers <- ok
		}
	})
	th := r.thread(t, domain.CreateParams{Cwd: dir, Agent: "fake", Mode: secdomain.ModeNormal})
	r.sendOnly(t, th.ID)

	a := r.requested(t)
	row := r.row(t, th.ID, "acp:edit")
	if a.ToolCallID != row.ID || a.Tool != "acp:edit" || a.Summary != file || a.Risk != string(secdomain.RiskWriteFS) {
		t.Errorf("approval %+v, want one for row %s on %s", a, row.ID, file)
	}
	r.respond(t, a, true)
	if ok := <-answers; !ok {
		t.Error("an approved permission was answered as refused")
	}
	r.respond(t, r.requested(t), false)
	if ok := <-answers; ok {
		t.Error("a denied permission was answered as allowed")
	}
	r.finished(t)
	if row := r.row(t, th.ID, "acp:edit"); row.Decision != domain.DecisionAsk || row.Via != domain.ViaACP {
		t.Errorf("row decision %q via %q, want ask and acp", row.Decision, row.Via)
	}
}

// TestACPWriteOutsideTheWorktreeAsks_REQ_ACP_004: a hosted thread's file write is WriteFS: in
// auto-edit, one inside its worktree is performed unasked, one outside asks, and a denied
// write is not performed.
func TestACPWriteOutsideTheWorktreeAsks_REQ_ACP_004(t *testing.T) {
	repo, outside := realDir(t), realDir(t)
	wts := &fakeWorktrees{root: realDir(t), repo: repo}
	did := &performed{}
	errs := make(chan error, 2)
	r := acpRig(t, wts, func(ctx context.Context, req ports.ExternalRequest, sink ports.ExternalSink) {
		errs <- sink.Request(ctx, ports.ExternalOp{Method: ports.OpWriteFile, Path: filepath.Join(req.Dir, "in.txt"), Content: "x"}, did.fn("wrote"))
		errs <- sink.Request(ctx, ports.ExternalOp{Method: ports.OpWriteFile, Path: filepath.Join(outside, "out.txt"), Content: "x"}, did.fn("wrote"))
	})
	th := r.thread(t, domain.CreateParams{Cwd: repo, Agent: "fake", Worktree: true, Mode: secdomain.ModeAutoEdit})
	r.sendOnly(t, th.ID)

	a := r.requested(t)
	if a.Tool != "acp:fs/write_text_file" || a.Summary != filepath.Join(outside, "out.txt") || a.Reason != secdomain.ReasonOutsideWriteRoot {
		t.Errorf("approval %s on %q (%s), want the write outside the worktree", a.Tool, a.Summary, a.Reason)
	}
	r.respond(t, a, false)
	if err := <-errs; err != nil {
		t.Errorf("the write inside the worktree: %v", err)
	}
	if err := <-errs; !errors.Is(err, ports.ErrDenied) {
		t.Errorf("the denied write: %v, want ErrDenied", err)
	}
	r.finished(t)
	got := did.list()
	if len(got) != 1 || filepath.Base(got[0]) != "in.txt" {
		t.Errorf("performed %v, want only the write inside the worktree", got)
	}
}

// TestACPReadOutsideTheWorktreeAsks_REQ_ACP_004: a read outside the write root asks, in a mode
// that allows every read of Umbral's own agent.
func TestACPReadOutsideTheWorktreeAsks_REQ_ACP_004(t *testing.T) {
	dir, outside := realDir(t), realDir(t)
	secret := filepath.Join(outside, "id_rsa")
	did := &performed{}
	errs := make(chan error, 1)
	r := acpRig(t, nil, func(ctx context.Context, _ ports.ExternalRequest, sink ports.ExternalSink) {
		errs <- sink.Request(ctx, ports.ExternalOp{Method: ports.OpReadFile, Path: secret}, did.fn("read"))
	})
	th := r.thread(t, domain.CreateParams{Cwd: dir, Agent: "fake", Mode: secdomain.ModeAutoEdit})
	r.sendOnly(t, th.ID)

	a := r.requested(t)
	if a.Tool != "acp:fs/read_text_file" || a.Summary != secret || a.Risk != string(secdomain.RiskReadOnly) {
		t.Errorf("approval %s on %q risk %s, want the read outside", a.Tool, a.Summary, a.Risk)
	}
	if len(did.list()) != 0 {
		t.Error("the read ran before it was approved")
	}
	r.respond(t, a, true)
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	r.finished(t)
	if got := did.list(); len(got) != 1 || got[0] != secret {
		t.Errorf("performed %v, want the approved read", got)
	}
}

// TestACPReadInsideTheWorktreeIsReadOnly_REQ_ACP_004: a read inside the write root is
// ReadOnly, allowed unasked even in `ask` mode, and recorded as a row of its own.
func TestACPReadInsideTheWorktreeIsReadOnly_REQ_ACP_004(t *testing.T) {
	repo := realDir(t)
	wts := &fakeWorktrees{root: realDir(t), repo: repo}
	did := &performed{}
	errs := make(chan error, 1)
	r := acpRig(t, wts, func(ctx context.Context, req ports.ExternalRequest, sink ports.ExternalSink) {
		errs <- sink.Request(ctx, ports.ExternalOp{Method: ports.OpReadFile, Path: filepath.Join(req.Dir, "main.go")}, did.fn("read 3 lines"))
	})
	th := r.thread(t, domain.CreateParams{Cwd: repo, Agent: "fake", Worktree: true, Mode: secdomain.ModeAsk})
	r.sendOnly(t, th.ID)
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	r.finished(t)
	r.noApproval(t)
	row := r.row(t, th.ID, "acp:fs/read_text_file")
	if row.Risk != string(secdomain.RiskReadOnly) || row.Decision != domain.DecisionAllow || row.Via != domain.ViaACP || row.Status != domain.ToolOK {
		t.Errorf("row %+v, want ReadOnly, allow, acp, ok", row)
	}
	if len(did.list()) != 1 {
		t.Errorf("performed %v, want the read", did.list())
	}
}

// TestACPWriteCoveredByAPermissionIsNotAskedTwice_REQ_ACP_004: a write to a path an allowed
// permission of the same turn listed is not decided again; a path it did not list is.
func TestACPWriteCoveredByAPermissionIsNotAskedTwice_REQ_ACP_004(t *testing.T) {
	dir := realDir(t)
	covered, other := filepath.Join(dir, "a.go"), filepath.Join(dir, "b.go")
	did := &performed{}
	errs := make(chan error, 2)
	r := acpRig(t, nil, func(ctx context.Context, _ ports.ExternalRequest, sink ports.ExternalSink) {
		call := ports.ExternalToolCall{ID: "e1", Kind: "edit", Risk: string(secdomain.RiskWriteFS), Status: "pending", Locations: []string{covered}}
		if ok, err := sink.Permission(ctx, call); !ok || err != nil {
			t.Errorf("the edit's permission: %v, %v", ok, err)
		}
		errs <- sink.Request(ctx, ports.ExternalOp{Method: ports.OpWriteFile, Path: covered, Content: "x"}, did.fn("wrote"))
		errs <- sink.Request(ctx, ports.ExternalOp{Method: ports.OpWriteFile, Path: other, Content: "x"}, did.fn("wrote"))
	})
	th := r.thread(t, domain.CreateParams{Cwd: dir, Agent: "fake", Mode: secdomain.ModeNormal})
	r.sendOnly(t, th.ID)

	if a := r.requested(t); a.Tool != "acp:edit" {
		t.Fatalf("first approval for %s, want the edit's permission", a.Tool)
	} else {
		r.respond(t, a, true)
	}
	select {
	case err := <-errs:
		if err != nil {
			t.Fatalf("the covered write: %v", err)
		}
	case a := <-r.bus.approvals:
		t.Fatalf("the covered write asked again: %s on %q", a.Tool, a.Summary)
	case <-time.After(10 * time.Second):
		t.Fatal("the covered write never answered")
	}
	a := r.requested(t)
	if a.Tool != "acp:fs/write_text_file" || a.Summary != other {
		t.Errorf("approval %s on %q, want the write the permission did not list", a.Tool, a.Summary)
	}
	r.respond(t, a, true)
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	r.finished(t)
	writes := 0
	calls, _ := r.store.ToolCalls(t.Context(), th.ID)
	for _, c := range calls {
		if c.Tool == "acp:fs/write_text_file" && c.Decision == domain.DecisionAllow {
			writes++
		}
	}
	if writes != 1 || len(did.list()) != 2 {
		t.Errorf("%d write rows decided allow and %d performed, want the covered one allowed and both performed", writes, len(did.list()))
	}
}

// TestACPToolCallsAreRecordedWithTheirDecision_REQ_ACP_007: every reported call is a row with
// via acp and its kind's risk; one the agent never asked about is `unmediated`, one the policy
// denied is `deny`, one the user was asked about is `ask` with its approval.
func TestACPToolCallsAreRecordedWithTheirDecision_REQ_ACP_007(t *testing.T) {
	dir := realDir(t)
	r := acpRig(t, nil, func(ctx context.Context, _ ports.ExternalRequest, sink ports.ExternalSink) {
		read := ports.ExternalToolCall{ID: "r1", Kind: "read", Risk: string(secdomain.RiskReadOnly), Status: "completed", Locations: []string{filepath.Join(dir, "x")}}
		if err := sink.ToolCall(read); err != nil {
			t.Error(err)
		}
		exec := ports.ExternalToolCall{ID: "x1", Kind: "execute", Risk: string(secdomain.RiskExec), Status: "pending", Command: "make"}
		if err := sink.ToolCall(exec); err != nil {
			t.Error(err)
		}
		if ok, _ := sink.Permission(ctx, exec); ok {
			t.Error("an execute in ask mode was allowed")
		}
		edit := ports.ExternalToolCall{ID: "e1", Kind: "edit", Risk: string(secdomain.RiskWriteFS), Status: "pending", Locations: []string{filepath.Join(dir, "y")}}
		if ok, _ := sink.Permission(ctx, edit); ok {
			t.Error("a denied edit was allowed")
		}
		exec.Status = "failed"
		if err := sink.ToolCall(exec); err != nil {
			t.Error(err)
		}
	})
	// `ask` exposes ReadOnly only: the policy denies the execute without asking.
	th := r.thread(t, domain.CreateParams{Cwd: dir, Agent: "fake", Mode: secdomain.ModeAsk})
	r.sendOnly(t, th.ID)
	r.finished(t)
	r.noApproval(t)
	for tool, want := range map[string]string{"acp:read": domain.DecisionUnmediated, "acp:execute": domain.DecisionDeny, "acp:edit": domain.DecisionDeny} {
		row := r.row(t, th.ID, tool)
		if row.Decision != want || row.Via != domain.ViaACP {
			t.Errorf("%s: decision %q via %q, want %s and acp", tool, row.Decision, row.Via, want)
		}
	}
	if row := r.row(t, th.ID, "acp:execute"); row.Risk != string(secdomain.RiskExec) || row.Status != domain.ToolError {
		t.Errorf("the execute's row %+v: a later update must keep its risk and decision", row)
	}

	// In normal mode the same edit asks: `ask`, and the approval points at the row.
	r2 := acpRig(t, nil, func(ctx context.Context, _ ports.ExternalRequest, sink ports.ExternalSink) {
		edit := ports.ExternalToolCall{ID: "e1", Kind: "edit", Risk: string(secdomain.RiskWriteFS), Status: "pending", Locations: []string{filepath.Join(dir, "y")}}
		_, _ = sink.Permission(ctx, edit)
	})
	th2 := r2.thread(t, domain.CreateParams{Cwd: dir, Agent: "fake", Mode: secdomain.ModeNormal})
	r2.sendOnly(t, th2.ID)
	a := r2.requested(t)
	r2.respond(t, a, false)
	r2.finished(t)
	if row := r2.row(t, th2.ID, "acp:edit"); row.Decision != domain.DecisionAsk || a.ToolCallID != row.ID {
		t.Errorf("row %+v approval for %s, want ask and the approval on this row", row, a.ToolCallID)
	}
}

// TestFullAutoDecidesACPPermissions_REQ_ORC_004: under full-auto an `edit` inside the worktree
// and an `execute` without a cwd — which runs in the agent's session directory, the worktree —
// are allowed unasked; a `fetch` still asks.
func TestFullAutoDecidesACPPermissions_REQ_ORC_004(t *testing.T) {
	repo := realDir(t)
	wts := &fakeWorktrees{root: realDir(t), repo: repo}
	outside := realDir(t)
	allowed := make(chan bool, 5)
	terminal := make(chan error, 2)
	r := acpRig(t, wts, func(ctx context.Context, req ports.ExternalRequest, sink ports.ExternalSink) {
		for _, call := range []ports.ExternalToolCall{
			{ID: "e1", Kind: "edit", Risk: string(secdomain.RiskWriteFS), Locations: []string{filepath.Join(req.Dir, "main.go")}},
			{ID: "x1", Kind: "execute", Risk: string(secdomain.RiskExec), Command: "go test ./..."},
			{ID: "f1", Kind: "fetch", Risk: string(secdomain.RiskNetwork), URL: "https://example.com/"},
			// Two locations, one outside: the strictest verdict decides.
			{ID: "e2", Kind: "edit", Risk: string(secdomain.RiskWriteFS), Locations: []string{filepath.Join(req.Dir, "ok.go"), filepath.Join(outside, "x.go")}},
			// A kind ACP lacks names nothing to decide on: never allowed unasked.
			{ID: "o1", Kind: "other", Risk: string(secdomain.RiskExec), Title: "switch mode"},
		} {
			ok, err := sink.Permission(ctx, call)
			if err != nil {
				t.Error(err)
			}
			allowed <- ok
		}
		// A terminal in the worktree runs unasked; one whose cwd is outside it asks.
		terminal <- sink.Request(ctx, ports.ExternalOp{Method: ports.OpTerminal, Command: "make", Cwd: req.Dir}, func(context.Context, string) (string, error) { return "started", nil })
		terminal <- sink.Request(ctx, ports.ExternalOp{Method: ports.OpTerminal, Command: "make", Cwd: outside}, func(context.Context, string) (string, error) { return "started", nil })
	})
	th := r.thread(t, domain.CreateParams{Cwd: repo, Agent: "fake", Worktree: true, Mode: secdomain.ModeNormal})
	if _, err := r.rt.Update(t.Context(), th.ID, domain.UpdateParams{Mode: modePtr(secdomain.ModeFullAuto)}); err != nil {
		t.Fatal(err)
	}
	r.sendOnly(t, th.ID)

	for _, want := range []string{
		"acp:fetch https://example.com/", "acp:edit " + filepath.Join(outside, "x.go"), "acp:other switch mode",
		"acp:terminal/create make",
	} {
		a := r.requested(t)
		if got := a.Tool + " " + a.Summary; got != want {
			t.Errorf("approval %q (%s), want %q", got, a.Reason, want)
		}
		r.respond(t, a, true)
	}
	for i, name := range []string{"edit", "execute", "fetch", "edit outside", "other"} {
		select {
		case ok := <-allowed:
			if !ok {
				t.Errorf("%s was refused", name)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("no answer for call %d", i)
		}
	}
	for range 2 {
		if err := <-terminal; err != nil {
			t.Errorf("terminal: %v", err)
		}
	}
	r.finished(t)
	calls, _ := r.store.ToolCalls(t.Context(), th.ID)
	terminals := map[string]int{}
	for _, c := range calls {
		if c.Tool == "acp:terminal/create" {
			terminals[c.Decision]++
		}
	}
	if terminals[domain.DecisionAllow] != 1 || terminals[domain.DecisionAsk] != 1 {
		t.Errorf("terminal rows by decision %v, want one allowed and one asked", terminals)
	}
	for tool, want := range map[string]string{"acp:edit": domain.DecisionAllow, "acp:execute": domain.DecisionAllow, "acp:fetch": domain.DecisionAsk} {
		if row := r.row(t, th.ID, tool); row.Decision != want {
			t.Errorf("%s decided %q, want %s", tool, row.Decision, want)
		}
	}
}

// TestAnACPTerminalOutsideTheWriteRootAsks_REQ_ACP_005: a terminal whose cwd is outside the
// write root asks even when a remembered rule allows its command line; inside, the rule
// allows it unasked.
func TestAnACPTerminalOutsideTheWriteRootAsks_REQ_ACP_005(t *testing.T) {
	dir, outside := realDir(t), realDir(t)
	errs := make(chan error, 2)
	r := acpRig(t, nil, func(ctx context.Context, req ports.ExternalRequest, sink ports.ExternalSink) {
		errs <- sink.Request(ctx, ports.ExternalOp{Method: ports.OpTerminal, Command: "make", Cwd: req.Dir}, func(context.Context, string) (string, error) { return "started", nil })
		errs <- sink.Request(ctx, ports.ExternalOp{Method: ports.OpTerminal, Command: "make", Cwd: outside}, func(context.Context, string) (string, error) { return "started", nil })
	})
	r.store.mu.Lock()
	r.store.rules = append(r.store.rules, secdomain.Rule{Tool: "acp:terminal/create", Pattern: "make", Decision: secdomain.VerdictAllow, Source: "user"})
	r.store.mu.Unlock()
	th := r.thread(t, domain.CreateParams{Cwd: dir, Agent: "fake", Mode: secdomain.ModeNormal})
	r.sendOnly(t, th.ID)

	if err := <-errs; err != nil {
		t.Fatalf("the terminal the rule allows: %v", err)
	}
	a := r.requested(t)
	if a.Tool != "acp:terminal/create" || a.Reason != secdomain.ReasonOutsideWriteRoot {
		t.Errorf("approval %s on %q (%s), want the terminal outside the write root", a.Tool, a.Summary, a.Reason)
	}
	r.respond(t, a, true)
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	r.finished(t)
}

// TestAnUndecidablePermissionIsRecordedDenied_REQ_ACP_007: a permission whose target Umbral
// cannot resolve is refused, and its row says `deny` — the agent asked, so it is never
// `unmediated`.
func TestAnUndecidablePermissionIsRecordedDenied_REQ_ACP_007(t *testing.T) {
	dir := realDir(t)
	r := acpRig(t, nil, func(ctx context.Context, _ ports.ExternalRequest, sink ports.ExternalSink) {
		call := ports.ExternalToolCall{ID: "e1", Kind: "edit", Risk: string(secdomain.RiskWriteFS), Status: "pending", Locations: []string{filepath.Join(dir, "x")}}
		if err := sink.ToolCall(call); err != nil {
			t.Error(err)
		}
		if ok, err := sink.Permission(ctx, call); ok || err != nil {
			t.Errorf("an undecidable permission: %v, %v; want refused", ok, err)
		}
	})
	r.tools.tools["write_file"].actionErr = errors.New("unresolvable")
	th := r.thread(t, domain.CreateParams{Cwd: dir, Agent: "fake", Mode: secdomain.ModeAutoEdit})
	r.sendOnly(t, th.ID)
	r.finished(t)
	r.noApproval(t)
	if row := r.row(t, th.ID, "acp:edit"); row.Decision != domain.DecisionDeny {
		t.Errorf("row decided %q, want deny", row.Decision)
	}
}

// TestATerminalIsDecidedOnTheWordsTheShellSees_REQ_ACP_005: a quoted word holding spaces or a
// separator must not hide the program from the destructive floor — `env 'A=x y' rm -rf /`
// runs rm — so under full-auto, in the worktree, both terminals ask, shown as typed.
func TestATerminalIsDecidedOnTheWordsTheShellSees_REQ_ACP_005(t *testing.T) {
	repo := realDir(t)
	wts := &fakeWorktrees{root: realDir(t), repo: repo}
	errs := make(chan error, 2)
	r := acpRig(t, wts, func(ctx context.Context, req ports.ExternalRequest, sink ports.ExternalSink) {
		for _, op := range []ports.ExternalOp{
			{Method: ports.OpTerminal, Command: `env 'A=x y' rm -rf /`, Policy: "env A=x_y rm -rf /", Cwd: req.Dir},
			{Method: ports.OpTerminal, Command: `rm 'x;y' -rf /`, Policy: "rm x_y -rf /", Cwd: req.Dir},
		} {
			errs <- sink.Request(ctx, op, func(context.Context, string) (string, error) { return "started", nil })
		}
	})
	th := r.thread(t, domain.CreateParams{Cwd: repo, Agent: "fake", Worktree: true, Mode: secdomain.ModeNormal})
	if _, err := r.rt.Update(t.Context(), th.ID, domain.UpdateParams{Mode: modePtr(secdomain.ModeFullAuto)}); err != nil {
		t.Fatal(err)
	}
	r.sendOnly(t, th.ID)
	for _, want := range []string{`env 'A=x y' rm -rf /`, `rm 'x;y' -rf /`} {
		a := r.requested(t)
		if a.Summary != want || a.Reason != secdomain.ReasonDestructive {
			t.Errorf("approval on %q (%s), want %q as destructive", a.Summary, a.Reason, want)
		}
		r.respond(t, a, false)
		if err := <-errs; !errors.Is(err, ports.ErrDenied) {
			t.Errorf("the refused terminal: %v", err)
		}
	}
	r.finished(t)
}

// TestAnACPExecuteEvasionAsks_REQ_ACP_003: an `execute` permission whose raw_input command is
// one of the evasions asks as destructive, even under full-auto in the worktree, and the agent
// is refused when the user refuses (delta `2026-10-shell-parser`).
func TestAnACPExecuteEvasionAsks_REQ_ACP_003(t *testing.T) {
	repo := realDir(t)
	wts := &fakeWorktrees{root: realDir(t), repo: repo}
	allowed := make(chan bool, len(shellEvasions))
	r := acpRig(t, wts, func(ctx context.Context, _ ports.ExternalRequest, sink ports.ExternalSink) {
		for i, line := range shellEvasions {
			ok, err := sink.Permission(ctx, ports.ExternalToolCall{ID: fmt.Sprintf("x%d", i), Kind: "execute", Risk: string(secdomain.RiskExec), Command: line})
			if err != nil {
				t.Error(err)
			}
			allowed <- ok
		}
	})
	th := r.thread(t, domain.CreateParams{Cwd: repo, Agent: "fake", Worktree: true, Mode: secdomain.ModeNormal})
	if _, err := r.rt.Update(t.Context(), th.ID, domain.UpdateParams{Mode: modePtr(secdomain.ModeFullAuto)}); err != nil {
		t.Fatal(err)
	}
	r.sendOnly(t, th.ID)
	for _, line := range shellEvasions {
		a := r.requested(t)
		if a.Summary != line || a.Reason != secdomain.ReasonDestructive {
			t.Errorf("approval on %q (%s), want %q as destructive", a.Summary, a.Reason, line)
		}
		r.respond(t, a, false)
		if <-allowed {
			t.Errorf("%q was allowed after a refusal", line)
		}
	}
	r.finished(t)
}

// TestATerminalThatRunsAScriptAsks_REQ_ACP_005: a terminal that hands a script to a shell asks
// even under full-auto in the worktree, even when the script is harmless — `sh -c 'make build'`.
// A destructive script asks at the destructive step instead (delta `2026-10-shell-parser`).
func TestATerminalThatRunsAScriptAsks_REQ_ACP_005(t *testing.T) {
	repo := realDir(t)
	wts := &fakeWorktrees{root: realDir(t), repo: repo}
	errs := make(chan error, 1)
	r := acpRig(t, wts, func(ctx context.Context, req ports.ExternalRequest, sink ports.ExternalSink) {
		errs <- sink.Request(ctx, ports.ExternalOp{
			Method: ports.OpTerminal, Command: `sh -c 'make build'`,
			Policy: "sh -c make_build", Script: true, Cwd: req.Dir,
		}, func(context.Context, string) (string, error) { return "started", nil })
	})
	th := r.thread(t, domain.CreateParams{Cwd: repo, Agent: "fake", Worktree: true, Mode: secdomain.ModeNormal})
	if _, err := r.rt.Update(t.Context(), th.ID, domain.UpdateParams{Mode: modePtr(secdomain.ModeFullAuto)}); err != nil {
		t.Fatal(err)
	}
	r.sendOnly(t, th.ID)
	a := r.requested(t)
	if a.Tool != "acp:terminal/create" || a.Reason != reasonScript {
		t.Errorf("approval %s (%s), want the script terminal asked as a script", a.Tool, a.Reason)
	}
	r.respond(t, a, false)
	if err := <-errs; !errors.Is(err, ports.ErrDenied) {
		t.Errorf("the refused terminal: %v", err)
	}
	r.finished(t)
}

// TestAnACPTurnHasATrace_REQ_ACP_007: a hosted turn is one trace, like Umbral's own: its span,
// and inside it a span per tool call the agent reports — ended at its last status, or with the
// turn when it never finished — and one per request Umbral mediated.
func TestAnACPTurnHasATrace_REQ_ACP_007(t *testing.T) {
	dir := realDir(t)
	r := acpRig(t, nil, func(ctx context.Context, _ ports.ExternalRequest, sink ports.ExternalSink) {
		read := ports.ExternalToolCall{ID: "r1", Kind: "read", Risk: string(secdomain.RiskReadOnly), Status: "pending", Locations: []string{filepath.Join(dir, "x")}}
		_ = sink.ToolCall(read)
		read.Status = "completed"
		_ = sink.ToolCall(read)
		exec := ports.ExternalToolCall{ID: "x1", Kind: "execute", Risk: string(secdomain.RiskExec), Status: "pending", Command: "make"}
		_ = sink.ToolCall(exec)
		_, _ = sink.Permission(ctx, exec)
		exec.Status = "failed"
		_ = sink.ToolCall(exec)
		_ = sink.ToolCall(ports.ExternalToolCall{ID: "p1", Kind: "search", Risk: string(secdomain.RiskReadOnly), Status: "pending"})
		if err := sink.Request(ctx, ports.ExternalOp{Method: ports.OpReadFile, Path: filepath.Join(dir, "x")}, func(context.Context, string) (string, error) { return "read", nil }); err != nil {
			t.Error(err)
		}
		// A request refused before it has a row still has its span, ended as an error.
		if err := sink.Request(ctx, ports.ExternalOp{Method: ports.OpWriteFile, Path: "relative"}, func(context.Context, string) (string, error) { return "", nil }); err == nil {
			t.Error("a relative write was performed")
		}
	})
	th := r.thread(t, domain.CreateParams{Cwd: dir, Agent: "fake", Mode: secdomain.ModeAsk})
	r.sendOnly(t, th.ID)
	r.finished(t)

	r.tracer.mu.Lock()
	defer r.tracer.mu.Unlock()
	if len(r.tracer.turns) != 1 || r.tracer.turns[0].threadID != th.ID || r.tracer.turns[0].end == nil {
		t.Fatalf("turn spans %+v, want the hosted turn's, ended", r.tracer.turns)
	}
	parent := "turn:" + r.tracer.turns[0].turnID
	want := map[string]domain.ToolStatus{
		"acp:read": domain.ToolOK, "acp:execute": domain.ToolError, "acp:search": domain.ToolPending,
		"acp:fs/read_text_file": domain.ToolOK, "acp:fs/write_text_file": domain.ToolError,
	}
	got := map[string]domain.ToolStatus{}
	for _, s := range r.tracer.tools {
		if s.parent != parent || s.end == nil {
			t.Errorf("span %s under %q ended %v, want under the turn's and ended", s.tool, s.parent, s.end != nil)
			continue
		}
		if _, dup := got[s.tool]; dup {
			t.Errorf("two spans for %s", s.tool)
		}
		got[s.tool] = s.end.Status
	}
	for tool, status := range want {
		if got[tool] != status {
			t.Errorf("%s's span ended %q, want %q (spans %v)", tool, got[tool], status, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("spans %v, want one per call and request", got)
	}
}
