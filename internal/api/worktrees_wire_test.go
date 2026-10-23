package api

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	agentsdomain "github.com/ecrespo/umbral/internal/agents/domain"
	wtdomain "github.com/ecrespo/umbral/internal/worktrees/domain"
)

// fakeWorktreeEngine holds wtr_1 (thr_1) and answers merges with whatever refusal it is told to.
type fakeWorktreeEngine struct {
	mu       sync.Mutex
	rows     map[string]wtdomain.Worktree
	mergeErr error
	merged   []string // "id revision message"
	listed   [][]string
	group    *wtdomain.CloseGroupRequest
}

func newFakeWorktreeEngine() *fakeWorktreeEngine {
	return &fakeWorktreeEngine{rows: map[string]wtdomain.Worktree{
		"wtr_1": {
			ID: "wtr_1", ThreadID: "thr_1", RepoRoot: "/repo", RepoID: "0123456789abcdef", BaseCommit: "abc",
			OriginBranch: "main", Branch: "umbral/thr_1", Path: "/data/wt/thr_1", State: wtdomain.StateActive, CreatedAt: 1, UpdatedAt: 2,
		},
	}}
}

func (f *fakeWorktreeEngine) Get(_ context.Context, id string) (wtdomain.Worktree, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.rows[id]
	if !ok {
		return w, wtdomain.ErrNotFound
	}
	return w, nil
}

func (f *fakeWorktreeEngine) List(_ context.Context, threadIDs []string) ([]wtdomain.Worktree, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listed = append(f.listed, threadIDs)
	var out []wtdomain.Worktree
	for _, w := range f.rows {
		if threadIDs == nil || slices.Contains(threadIDs, w.ThreadID) {
			out = append(out, w)
		}
	}
	return out, nil
}

func (f *fakeWorktreeEngine) Diff(context.Context, string) (wtdomain.DiffResult, error) {
	return wtdomain.DiffResult{Revision: "rev1", Files: []wtdomain.FileDiff{{
		Path: "a.txt", Status: "modified",
		Hunks: []wtdomain.Hunk{{ID: "h1", Header: "@@ -1 +1 @@", Lines: []string{"-one", "+uno"}}},
	}}}, nil
}

func (f *fakeWorktreeEngine) Merge(_ context.Context, req wtdomain.MergeRequest) (wtdomain.MergeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mergeErr != nil {
		return wtdomain.MergeResult{}, f.mergeErr
	}
	f.merged = append(f.merged, req.ID+" "+req.Revision+" "+req.Message)
	w := f.rows[req.ID]
	w.State = wtdomain.StateMerged
	f.rows[req.ID] = w
	return wtdomain.MergeResult{Commit: "c0ffee"}, nil
}

func (f *fakeWorktreeEngine) Discard(_ context.Context, id string, confirm bool) error {
	if !confirm {
		return &wtdomain.Refusal{Reason: wtdomain.ReasonConfirmRequired}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	w := f.rows[id]
	w.State = wtdomain.StateDiscarded
	f.rows[id] = w
	return nil
}

func (f *fakeWorktreeEngine) CloseGroup(ctx context.Context, req wtdomain.CloseGroupRequest) ([]wtdomain.GroupEntry, error) {
	f.mu.Lock()
	f.group = &req
	w := f.rows["wtr_1"]
	f.mu.Unlock()
	for _, id := range req.Discard {
		if err := req.Guard(ctx, f.rows[id].ThreadID, func() error { return nil }); err != nil {
			return nil, err
		}
	}
	return []wtdomain.GroupEntry{{Worktree: w, Changes: wtdomain.ChangesChanged}}, nil
}

// fakeWorktreeThreads says thr_busy has a turn running; w1 holds thr_1 and thr_2.
type fakeWorktreeThreads struct {
	createErr error
}

func (f *fakeWorktreeThreads) CreateWorktree(_ context.Context, threadID, _ string) (string, error) {
	if f.createErr != nil {
		return "", f.createErr
	}
	if threadID == "thr_busy" {
		return "", agentsdomain.ErrTurnRunning
	}
	return "wtr_1", nil
}

func (f *fakeWorktreeThreads) WhileIdle(_ context.Context, threadID string, fn func() error) error {
	if threadID == "thr_busy" {
		return fmt.Errorf("%w: thread %s", agentsdomain.ErrTurnRunning, threadID)
	}
	return fn()
}

func (f *fakeWorktreeThreads) InWorkspace(_ context.Context, workspaceID string) ([]agentsdomain.Thread, error) {
	if workspaceID != "w1" {
		return nil, nil
	}
	return []agentsdomain.Thread{{ID: "thr_1"}, {ID: "thr_2"}}, nil
}

// TestWorktreeMethodsOverTheWire_REQ_WT_004: worktree.* is advertised as `worktrees`; a merge
// passes its revision and message to the engine; the engine's refusals reach the client as
// CONFLICT or VALIDATION_ERROR with `data.reason`, a conflict's `data.paths` and a failed
// commit's `data.output`; a change under a running turn is CONFLICT `running`; an unknown id is
// NOT_FOUND.
func TestWorktreeMethodsOverTheWire_REQ_WT_004(t *testing.T) {
	t.Parallel()
	engine := newFakeWorktreeEngine()
	threads := &fakeWorktreeThreads{}
	s := testServerWithConfig(t, Config{Worktrees: engine, WorktreeThreads: threads})
	c := dial(t, s)
	hello := c.hello(s.Token(), ClientTUI)
	if !slices.Contains(capabilitiesOf(t, hello.Result), "worktrees") {
		t.Fatalf("worktrees is not advertised: %v", capabilitiesOf(t, hello.Result))
	}

	resp := c.call(2, "worktree.diff", map[string]any{"worktree_id": "wtr_1"})
	want := `{"worktree_id":"wtr_1","revision":"rev1","files":[{"path":"a.txt","status":"modified","binary":false,` +
		`"hunks":[{"id":"h1","header":"@@ -1 +1 @@","lines":["-one","+uno"]}]}]}`
	if resp.Error != nil || string(ordered[worktreeDiffResult](t, resp.Result)) != want {
		t.Fatalf("worktree.diff = %s, %+v", resp.Result, resp.Error)
	}

	refusals := []struct {
		err    error
		code   string
		reason string
		paths  []string
		output string
	}{
		{&wtdomain.Refusal{Reason: wtdomain.ReasonDirty}, domainConflict, "dirty", nil, ""},
		{&wtdomain.Refusal{Reason: wtdomain.ReasonOtherBranch}, domainConflict, "other_branch", nil, ""},
		{&wtdomain.Refusal{Reason: wtdomain.ReasonDetached}, domainConflict, "detached", nil, ""},
		{&wtdomain.Refusal{Reason: wtdomain.ReasonConflict, Paths: []string{"a.txt", "b.txt"}}, domainConflict, "conflict", []string{"a.txt", "b.txt"}, ""},
		{&wtdomain.Refusal{Reason: wtdomain.ReasonCommitFailed, Output: "hook said no"}, domainConflict, "commit_failed", nil, "hook said no"},
		{&wtdomain.Refusal{Reason: wtdomain.ReasonChanged}, domainConflict, "changed", nil, ""},
		{&wtdomain.Refusal{Reason: wtdomain.ReasonNotGit}, domainValidationError, "not_git", nil, ""},
	}
	for i, r := range refusals {
		engine.mu.Lock()
		engine.mergeErr = r.err
		engine.mu.Unlock()
		resp := c.call(10+i, "worktree.merge", map[string]any{"worktree_id": "wtr_1", "revision": "rev1"})
		d := resp.Error
		if d == nil || d.Data == nil || d.Data.DomainCode != r.code || d.Data.Reason != r.reason ||
			!slices.Equal(d.Data.Paths, r.paths) || d.Data.Output != r.output {
			t.Errorf("%s: %+v / %+v", r.reason, d, d.Data)
		}
	}
	engine.mu.Lock()
	engine.mergeErr = nil
	engine.mu.Unlock()

	resp = c.call(30, "worktree.merge", map[string]any{"worktree_id": "wtr_1", "revision": "rev1", "message": "land it"})
	if resp.Error != nil || string(ordered[worktreeMergeResult](t, resp.Result)) !=
		`{"worktree":{"id":"wtr_1","thread_id":"thr_1","repo_root":"/repo","origin_branch":"main","base_commit":"abc",`+
			`"branch":"umbral/thr_1","path":"/data/wt/thr_1","state":"merged","created_at":1,"updated_at":2},"commit":"c0ffee"}` {
		t.Fatalf("worktree.merge = %s, %+v", resp.Result, resp.Error)
	}
	if !slices.Equal(engine.merged, []string{"wtr_1 rev1 land it"}) {
		t.Fatalf("the engine merged %v", engine.merged)
	}

	engine.rows["wtr_busy"] = wtdomain.Worktree{ID: "wtr_busy", ThreadID: "thr_busy", State: wtdomain.StateActive}
	for i, call := range []struct {
		method string
		params map[string]any
	}{
		{"worktree.merge", map[string]any{"worktree_id": "wtr_busy"}},
		{"worktree.discard", map[string]any{"worktree_id": "wtr_busy", "confirm": true}},
		{"worktree.create", map[string]any{"thread_id": "thr_busy"}},
		{"worktree.close_group", map[string]any{"thread_ids": []string{"thr_busy"}, "discard": []string{"wtr_busy"}, "confirm": true}},
	} {
		resp := c.call(40+i, call.method, call.params)
		if resp.Error == nil || resp.Error.Data.DomainCode != domainConflict || resp.Error.Data.Reason != "running" {
			t.Errorf("%s under a running turn: %+v", call.method, resp.Error)
		}
	}
	if got := engine.rows["wtr_busy"].State; got != wtdomain.StateActive {
		t.Errorf("a refused discard left wtr_busy %s", got)
	}

	threads.createErr = &wtdomain.Refusal{Reason: wtdomain.ReasonBare, Detail: "/repo"}
	resp = c.call(50, "worktree.create", map[string]any{"thread_id": "thr_1"})
	if resp.Error == nil || resp.Error.Data.DomainCode != domainValidationError || resp.Error.Data.Reason != "bare" {
		t.Errorf("worktree.create on a bare repository: %+v", resp.Error)
	}
	resp = c.call(51, "worktree.get", map[string]any{"worktree_id": "wtr_none"})
	if resp.Error == nil || resp.Error.Data.DomainCode != domainNotFound {
		t.Errorf("worktree.get of an unknown id: %+v", resp.Error)
	}
	resp = c.call(52, "worktree.discard", map[string]any{"worktree_id": "wtr_1"})
	if resp.Error == nil || resp.Error.Data.Reason != "confirm_required" || resp.Error.Data.DomainCode != domainValidationError {
		t.Errorf("worktree.discard without confirm: %+v", resp.Error)
	}
}

// TestCloseGroupOverTheWire_REQ_WT_007: a group is a workspace's threads or a list of threads,
// never both; each worktree comes back with its state and, for an active one, `changes`; the
// runtime's guard decides every discard.
func TestCloseGroupOverTheWire_REQ_WT_007(t *testing.T) {
	t.Parallel()
	engine := newFakeWorktreeEngine()
	s := testServerWithConfig(t, Config{Worktrees: engine, WorktreeThreads: &fakeWorktreeThreads{}})
	c := dial(t, s)
	c.hello(s.Token(), ClientTUI)

	resp := c.call(2, "worktree.close_group", map[string]any{"workspace_id": "w1", "discard": []string{"wtr_1"}, "confirm": true})
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	want := `{"items":[{"worktree":{"id":"wtr_1","thread_id":"thr_1","repo_root":"/repo","origin_branch":"main",` +
		`"base_commit":"abc","branch":"umbral/thr_1","path":"/data/wt/thr_1","state":"active","created_at":1,"updated_at":2},` +
		`"changes":"changed","discarded":false}]}`
	if got := string(ordered[worktreeCloseGroupResult](t, resp.Result)); got != want {
		t.Errorf("result =\n%s\nwant\n%s", got, want)
	}
	if g := engine.group; g == nil || !slices.Equal(g.ThreadIDs, []string{"thr_1", "thr_2"}) || !g.Confirm ||
		!slices.Equal(g.Discard, []string{"wtr_1"}) || g.Guard == nil {
		t.Fatalf("the engine got %+v", engine.group)
	}
	for name, params := range map[string]map[string]any{
		"both":    {"workspace_id": "w1", "thread_ids": []string{"thr_1"}},
		"neither": {},
	} {
		if resp := c.call(3, "worktree.close_group", params); resp.Error == nil || resp.Error.Data.DomainCode != domainValidationError {
			t.Errorf("%s: %+v", name, resp.Error)
		}
	}
	// An empty workspace is an empty group, not every worktree.
	c.call(4, "worktree.list", map[string]any{"workspace_id": "w9"})
	if last := engine.listed[len(engine.listed)-1]; last == nil || len(last) != 0 {
		t.Errorf("an empty workspace listed %v", last)
	}
}

// TestUmbMayReviewButNotCreateWorktrees_REQ_WT_004: §2 gives `umb` worktree.list, get, diff,
// merge and discard, and keeps create and close_group for the interactive clients.
func TestUmbMayReviewButNotCreateWorktrees_REQ_WT_004(t *testing.T) {
	t.Parallel()
	s := testServerWithConfig(t, Config{Worktrees: newFakeWorktreeEngine(), WorktreeThreads: &fakeWorktreeThreads{}})
	c := dial(t, s)
	c.hello(s.Token(), ClientCLI)
	for i, m := range []string{"worktree.list", "worktree.get", "worktree.diff", "worktree.merge", "worktree.discard"} {
		resp := c.call(2+i, m, map[string]any{"worktree_id": "wtr_1", "confirm": true})
		if resp.Error != nil && resp.Error.Data.DomainCode == "METHOD_NOT_FOUND" {
			t.Errorf("umb may not call %s", m)
		}
	}
	for i, m := range []string{"worktree.create", "worktree.close_group"} {
		resp := c.call(10+i, m, map[string]any{"thread_id": "thr_1"})
		if resp.Error == nil || resp.Error.Data.DomainCode != "METHOD_NOT_FOUND" {
			t.Errorf("umb called %s: %+v", m, resp.Error)
		}
	}
}

// TestWorktreesAreNotImplementedWithoutTheEngine: without the engine the namespace is not
// advertised, its methods and thread.create's `worktree` answer NOT_IMPLEMENTED.
func TestWorktreesAreNotImplementedWithoutTheEngine(t *testing.T) {
	t.Parallel()
	s := testServerWithConfig(t, Config{Threads: &fakeThreads{}})
	c := dial(t, s)
	hello := c.hello(s.Token(), ClientTUI)
	if slices.Contains(capabilitiesOf(t, hello.Result), "worktrees") {
		t.Error("worktrees advertised without the engine")
	}
	if resp := c.call(2, "worktree.list", map[string]any{}); resp.Error == nil || resp.Error.Data.DomainCode != "NOT_IMPLEMENTED" {
		t.Errorf("worktree.list: %+v", resp.Error)
	}
	resp := c.call(3, "thread.create", map[string]any{"cwd": "/w", "worktree": true})
	if resp.Error == nil || resp.Error.Data.DomainCode != "NOT_IMPLEMENTED" {
		t.Errorf("thread.create with a worktree: %+v", resp.Error)
	}
}
