package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	agentsdomain "github.com/ecrespo/umbral/internal/agents/domain"
	agentsports "github.com/ecrespo/umbral/internal/agents/ports"
	wtdomain "github.com/ecrespo/umbral/internal/worktrees/domain"
	wtports "github.com/ecrespo/umbral/internal/worktrees/ports"
)

// API Spec §5.41: worktree.* — a thread's isolated copy of its repository, shown as a diff,
// squashed back into the branch it came from or discarded (REQ-WT-001…008). The engine owns
// the git side; the agent runtime decides when a worktree may change under a thread: never
// while its turn runs (delta `2026-10-worktree-engine`, decision 19).

const capabilityWorktrees = "worktrees"

func hasWorktrees(cfg Config) bool { return cfg.Worktrees != nil && cfg.WorktreeThreads != nil }

// worktreeMethods: `umb worktree` lists, shows, merges and discards (§2); creating one and
// closing a group stay with the interactive clients, which own threads and workspaces.
func worktreeMethods() map[string]method {
	return map[string]method{
		"worktree.create":      {handle: handleWorktreeCreate, kinds: interactiveClients, params: worktreeCreateParams{}, result: Worktree{}},
		"worktree.list":        {handle: handleWorktreeList, kinds: treeClients, params: worktreeListParams{}, result: worktreeListResult{}},
		"worktree.get":         {handle: handleWorktreeGet, kinds: treeClients, params: worktreeIDParams{}, result: Worktree{}},
		"worktree.diff":        {handle: handleWorktreeDiff, kinds: treeClients, params: worktreeIDParams{}, result: worktreeDiffResult{}},
		"worktree.merge":       {handle: handleWorktreeMerge, userOnly: true, kinds: treeClients, params: worktreeMergeParams{}, result: worktreeMergeResult{}},
		"worktree.discard":     {handle: handleWorktreeDiscard, userOnly: true, kinds: treeClients, params: worktreeDiscardParams{}, result: Worktree{}},
		"worktree.close_group": {handle: handleWorktreeCloseGroup, userOnly: true, kinds: interactiveClients, params: worktreeCloseGroupParams{}, result: worktreeCloseGroupResult{}},
	}
}

// Worktree is API Spec §4's Worktree.
type Worktree struct {
	ID       string `json:"id"`
	ThreadID string `json:"thread_id"`
	RepoRoot string `json:"repo_root"`
	// OriginBranch is null for a worktree made from a detached HEAD, which no merge can land.
	OriginBranch *string `json:"origin_branch"`
	BaseCommit   string  `json:"base_commit"`
	Branch       string  `json:"branch"`
	Path         string  `json:"path"`
	State        string  `json:"state"`
	CreatedAt    int64   `json:"created_at"`
	UpdatedAt    int64   `json:"updated_at"`
}

func toWireWorktree(w wtdomain.Worktree) Worktree {
	return Worktree{
		ID: w.ID, ThreadID: w.ThreadID, RepoRoot: w.RepoRoot, OriginBranch: emptyAsNull(w.OriginBranch),
		BaseCommit: w.BaseCommit, Branch: w.Branch, Path: w.Path, State: string(w.State),
		CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt,
	}
}

const fieldWorktreeID = "worktree_id"

func worktreeSvc(c *conn) (wtports.Worktrees, error) {
	if !hasWorktrees(c.server.cfg) {
		return nil, fmt.Errorf("%w: the worktree engine is not wired in", ErrNotImplemented)
	}
	return c.server.cfg.Worktrees, nil
}

type worktreeCreateParams struct {
	ThreadID string `json:"thread_id"`
	Base     string `json:"base" api:"optional"`
}

func handleWorktreeCreate(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := worktreeSvc(c)
	if err != nil {
		return nil, err
	}
	var p worktreeCreateParams
	if err := decode(raw, &p, "worktree.create"); err != nil {
		return nil, err
	}
	if p.ThreadID == "" {
		return nil, ValidationError("thread_id is required", ErrorField{Field: fieldThreadID, Issue: requiredTag})
	}
	id, err := c.server.cfg.WorktreeThreads.CreateWorktree(ctx, p.ThreadID, p.Base)
	if err != nil {
		return nil, err
	}
	w, err := svc.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return toWireWorktree(w), nil
}

type worktreeListParams struct {
	ThreadID    string `json:"thread_id" api:"optional"`
	WorkspaceID string `json:"workspace_id" api:"optional"`
}

type worktreeListResult struct {
	Items []Worktree `json:"items"`
}

func handleWorktreeList(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := worktreeSvc(c)
	if err != nil {
		return nil, err
	}
	var p worktreeListParams
	if err := decode(raw, &p, "worktree.list"); err != nil {
		return nil, err
	}
	if p.ThreadID != "" && p.WorkspaceID != "" {
		return nil, ValidationError("give thread_id or workspace_id, not both", ErrorField{Field: "workspace_id", Issue: "exclusive"})
	}
	var threadIDs []string
	switch {
	case p.ThreadID != "":
		threadIDs = []string{p.ThreadID}
	case p.WorkspaceID != "":
		if threadIDs, err = workspaceThreads(ctx, c, p.WorkspaceID); err != nil {
			return nil, err
		}
	}
	rows, err := svc.List(ctx, threadIDs)
	if err != nil {
		return nil, err
	}
	out := worktreeListResult{Items: make([]Worktree, 0, len(rows))}
	for _, w := range rows {
		out.Items = append(out.Items, toWireWorktree(w))
	}
	return out, nil
}

// workspaceThreads is the group a workspace names: the threads created in it (REQ-WT-007). It
// is never nil, so an empty workspace lists no worktree rather than every one.
func workspaceThreads(ctx context.Context, c *conn, workspaceID string) ([]string, error) {
	threads, err := c.server.cfg.WorktreeThreads.InWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(threads))
	for _, t := range threads {
		ids = append(ids, t.ID)
	}
	return ids, nil
}

type worktreeIDParams struct {
	WorktreeID string `json:"worktree_id"`
}

func worktreeID(raw json.RawMessage, method string) (string, error) {
	var p worktreeIDParams
	if err := decode(raw, &p, method); err != nil {
		return "", err
	}
	if p.WorktreeID == "" {
		return "", ValidationError("worktree_id is required", ErrorField{Field: fieldWorktreeID, Issue: requiredTag})
	}
	return p.WorktreeID, nil
}

func handleWorktreeGet(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := worktreeSvc(c)
	if err != nil {
		return nil, err
	}
	id, err := worktreeID(raw, "worktree.get")
	if err != nil {
		return nil, err
	}
	w, err := svc.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return toWireWorktree(w), nil
}

// The diff's wire shapes (§5.41).
type worktreeDiffResult struct {
	WorktreeID string             `json:"worktree_id"`
	Revision   string             `json:"revision"`
	Files      []worktreeDiffFile `json:"files"`
}

type worktreeDiffFile struct {
	Path   string             `json:"path"`
	Status string             `json:"status"`
	Binary bool               `json:"binary"`
	Hunks  []worktreeDiffHunk `json:"hunks"`
}

type worktreeDiffHunk struct {
	ID     string   `json:"id"`
	Header string   `json:"header"`
	Lines  []string `json:"lines"`
}

func handleWorktreeDiff(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := worktreeSvc(c)
	if err != nil {
		return nil, err
	}
	id, err := worktreeID(raw, "worktree.diff")
	if err != nil {
		return nil, err
	}
	d, err := svc.Diff(ctx, id)
	if err != nil {
		return nil, err
	}
	out := worktreeDiffResult{WorktreeID: id, Revision: d.Revision, Files: make([]worktreeDiffFile, 0, len(d.Files))}
	for _, f := range d.Files {
		file := worktreeDiffFile{Path: f.Path, Status: f.Status, Binary: f.Binary, Hunks: make([]worktreeDiffHunk, 0, len(f.Hunks))}
		for _, h := range f.Hunks {
			lines := h.Lines
			if lines == nil {
				lines = []string{}
			}
			file.Hunks = append(file.Hunks, worktreeDiffHunk{ID: h.ID, Header: h.Header, Lines: lines})
		}
		out.Files = append(out.Files, file)
	}
	return out, nil
}

type worktreeMergeParams struct {
	WorktreeID string `json:"worktree_id"`
	Message    string `json:"message" api:"optional"`
	// Revision is worktree.diff's: the merge lands that content or nothing (decision 19).
	Revision string `json:"revision" api:"optional"`
}

type worktreeMergeResult struct {
	Worktree Worktree `json:"worktree"`
	// Commit is null when the worktree changed nothing.
	Commit *string `json:"commit"`
}

func handleWorktreeMerge(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := worktreeSvc(c)
	if err != nil {
		return nil, err
	}
	var p worktreeMergeParams
	if err := decode(raw, &p, "worktree.merge"); err != nil {
		return nil, err
	}
	if p.WorktreeID == "" {
		return nil, ValidationError("worktree_id is required", ErrorField{Field: fieldWorktreeID, Issue: requiredTag})
	}
	w, err := svc.Get(ctx, p.WorktreeID)
	if err != nil {
		return nil, err
	}
	var commit string
	err = c.server.cfg.WorktreeThreads.WhileIdle(ctx, w.ThreadID, func() error {
		res, err := svc.Merge(ctx, wtdomain.MergeRequest{ID: p.WorktreeID, Message: p.Message, Revision: p.Revision})
		commit = res.Commit
		if err != nil && res.Commit != "" {
			// The commit landed but `merged` was not recorded: the answer is the error, and the
			// commit is kept where the user can find it (delta `2026-10-worktree-methods`, 15).
			c.server.cfg.Logger.ErrorContext(ctx, "a merge landed but its worktree was not marked merged",
				slog.String("worktree", p.WorktreeID), slog.String("commit", res.Commit), slog.Any("error", err))
		}
		if res.Leftover != nil {
			c.server.cfg.Logger.WarnContext(ctx, "a merged worktree was left on disk",
				slog.String("worktree", p.WorktreeID), slog.Any("error", res.Leftover))
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	if w, err = svc.Get(ctx, p.WorktreeID); err != nil {
		return nil, err
	}
	return worktreeMergeResult{Worktree: toWireWorktree(w), Commit: emptyAsNull(commit)}, nil
}

type worktreeDiscardParams struct {
	WorktreeID string `json:"worktree_id"`
	Confirm    bool   `json:"confirm" api:"optional"`
}

func handleWorktreeDiscard(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := worktreeSvc(c)
	if err != nil {
		return nil, err
	}
	var p worktreeDiscardParams
	if err := decode(raw, &p, "worktree.discard"); err != nil {
		return nil, err
	}
	if p.WorktreeID == "" {
		return nil, ValidationError("worktree_id is required", ErrorField{Field: fieldWorktreeID, Issue: requiredTag})
	}
	w, err := svc.Get(ctx, p.WorktreeID)
	if err != nil {
		return nil, err
	}
	err = c.server.cfg.WorktreeThreads.WhileIdle(ctx, w.ThreadID, func() error {
		return svc.Discard(ctx, p.WorktreeID, p.Confirm)
	})
	if err != nil {
		return nil, err
	}
	if w, err = svc.Get(ctx, p.WorktreeID); err != nil {
		return nil, err
	}
	return toWireWorktree(w), nil
}

type worktreeCloseGroupParams struct {
	WorkspaceID string   `json:"workspace_id" api:"optional"`
	ThreadIDs   []string `json:"thread_ids" api:"optional"`
	Discard     []string `json:"discard" api:"optional"`
	Confirm     bool     `json:"confirm" api:"optional"`
}

type worktreeCloseGroupResult struct {
	Items []worktreeGroupItem `json:"items"`
}

type worktreeGroupItem struct {
	Worktree Worktree `json:"worktree"`
	// Changes is `changed` or `clean` for a worktree that was active, null otherwise.
	Changes   *string `json:"changes"`
	Discarded bool    `json:"discarded"`
}

func handleWorktreeCloseGroup(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := worktreeSvc(c)
	if err != nil {
		return nil, err
	}
	var p worktreeCloseGroupParams
	if err := decode(raw, &p, "worktree.close_group"); err != nil {
		return nil, err
	}
	if (p.WorkspaceID == "") == (p.ThreadIDs == nil) {
		return nil, ValidationError("give exactly one of workspace_id and thread_ids", ErrorField{Field: "workspace_id", Issue: "exclusive"})
	}
	threadIDs := p.ThreadIDs
	if p.WorkspaceID != "" {
		if threadIDs, err = workspaceThreads(ctx, c, p.WorkspaceID); err != nil {
			return nil, err
		}
	}
	entries, err := svc.CloseGroup(ctx, wtdomain.CloseGroupRequest{
		ThreadIDs: threadIDs, Discard: p.Discard, Confirm: p.Confirm,
		Guard: c.server.cfg.WorktreeThreads.WhileIdle,
	})
	if err != nil {
		return nil, err
	}
	out := worktreeCloseGroupResult{Items: make([]worktreeGroupItem, 0, len(entries))}
	for _, e := range entries {
		out.Items = append(out.Items, worktreeGroupItem{Worktree: toWireWorktree(e.Worktree), Changes: emptyAsNull(e.Changes), Discarded: e.Discarded})
	}
	return out, nil
}

// worktreesDomainError maps the engine's errors onto §3: a refusal about the request is
// VALIDATION_ERROR and one about the repository's state CONFLICT, both with `data.reason`
// (finishWire adds it).
func worktreesDomainError(err error) (int, string, bool) {
	var refusal *wtdomain.Refusal
	switch {
	case errors.As(err, &refusal) && refusal.Conflict():
		return codeConflict, domainConflict, true
	case errors.As(err, &refusal):
		return codeValidationError, domainValidationError, true
	case errors.Is(err, wtdomain.ErrNotFound):
		return codeNotFound, domainNotFound, true
	default:
		return 0, "", false
	}
}

// reasonRunning is `data.reason` of a worktree change refused because the thread's turn runs.
const reasonRunning = "running"

// worktreeErrorData fills `data.reason`, `data.paths` and `data.output` (§3): a worktree
// refusal's, or a hosted agent's (REQ-ACP-009).
func worktreeErrorData(err error, data *errorData) {
	var refusal *wtdomain.Refusal
	switch {
	case errors.As(err, &refusal):
		data.Reason = string(refusal.Reason)
		data.Paths = refusal.Paths
		data.Output = refusal.Output
	case errors.Is(err, agentsdomain.ErrTurnRunning):
		data.Reason = reasonRunning
	}
	var agent *agentsports.AgentRefusal
	if errors.As(err, &agent) {
		data.Reason = agent.Reason
	}
}
