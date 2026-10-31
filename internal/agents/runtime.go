// Package agents is the agent runtime: it creates threads, runs a turn's loop of model calls
// and tool calls, and persists every message, tool call and result before publishing it
// (REQ-AGT-001, 008, 009, 010, 011, 015; DD-007). It reaches the other modules only through
// their ports, which cmd/umbrald wires.
package agents

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ecrespo/umbral/internal/agents/domain"
	"github.com/ecrespo/umbral/internal/agents/ports"
	"github.com/ecrespo/umbral/internal/bus"
	ctxdomain "github.com/ecrespo/umbral/internal/context/domain"
	ctxports "github.com/ecrespo/umbral/internal/context/ports"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
	"github.com/ecrespo/umbral/internal/slots"
	toolsports "github.com/ecrespo/umbral/internal/tools/ports"
)

// DefaultMaxConcurrentTurns is `[agents] max_concurrent_turns`' default (REQ-ORC-002).
const DefaultMaxConcurrentTurns = 3

// Config wires the runtime. Store, Models, Tools, Bus, NewID, Metrics and Tracer are required.
type Config struct {
	Store  ports.Store
	Models ports.Models
	Tools  toolsports.Registry
	Bus    ports.Publisher
	// Context reads rules files, git state and attachments; nil sends none of them.
	Context ctxports.Gatherer
	// Skills is the catalog of enabled skills each turn's prompt lists (REQ-SKL-004); nil
	// lists none.
	Skills ctxports.SkillCatalog
	// Worktrees is the worktree engine: a thread with an active worktree runs its turns there
	// (REQ-WT-002). Nil makes no worktree and runs every thread in its own directory.
	Worktrees ports.Worktrees
	// Agents hosts the threads created with an `agent` (REQ-ACP-001); nil refuses them.
	Agents ports.ExternalAgents
	// NewID makes a type-prefixed ULID (store.NewID).
	NewID func(prefix string) string
	// Metrics counts what the runtime measures.
	Metrics ports.Metrics
	// Tracer traces each turn (REQ-OBS-001).
	Tracer ports.Tracer
	// IsRepo says whether a directory is a repository root, for the write root; nil looks for
	// a `.git` entry.
	IsRepo func(dir string) bool
	// Policy is the engine over the destructive patterns in force, read at every decision so
	// a change of rules applies at once (REQ-SEC-010); nil is the built-in list.
	Policy func() secdomain.Policy
	// MaxCostMicroUSD is `router.max_cost_usd_per_thread` in micro-USD, read at every check so
	// a reload applies at once; 0, or a nil func, is no cap (REQ-AGT-008, delta
	// `2026-10-cost-cap`).
	MaxCostMicroUSD func() int64
	// StallLocal and StallRemote are REQ-AUT-008's windows, `[waits] stall_local_s` and
	// `stall_remote_s`; StallTick is how often running turns are checked. Zero is the default.
	StallLocal, StallRemote, StallTick time.Duration
	// MaxConcurrentTurns is `[agents] max_concurrent_turns`: how many turns, of all threads,
	// run at once; a turn over it is held, queued, until one ends (REQ-ORC-002). Zero is
	// DefaultMaxConcurrentTurns.
	MaxConcurrentTurns int
	Now                func() time.Time
	Logger             *slog.Logger
}

// Runtime is ports.Threads.
type Runtime struct {
	cfg  Config
	base context.Context
	stop context.CancelFunc

	// sendMu serialises thread.send per thread: the duplicate check and the conflict check
	// must see each other's writes (REQ-AGT-015).
	sendMu sync.Map // thread id → *sync.Mutex
	// attnMu holds each thread's attention lock (attention.go).
	attnMu sync.Map // thread id → *sync.Mutex
	// slots holds turns over MaxConcurrentTurns, granting them in arrival order.
	slots *slots.Queue

	mu     sync.Mutex
	closed bool
	turns  map[string]*turn // thread id → running turn
	// waiting are the turns paused on an approval, by approval id.
	waiting map[string]chan domain.Approval
	// ends and views are guarded by mu.
	ends  turnEnds
	views viewers
	wg    sync.WaitGroup
}

var (
	_ ports.Threads         = (*Runtime)(nil)
	_ ports.WorktreeThreads = (*Runtime)(nil)
)

// turn is a running turn, which thread.cancel (T-F1-16) stops through cancel.
// maxTurnEnds bounds the runtime's memory of how turns ended: enough for every wait of a
// session to find its turn, small beside the daemon's memory budget.
const maxTurnEnds = 4096

// turnEnds remembers the end state of the last maxTurnEnds turns, for a wait whose pinned
// turn a later one has replaced (DD-011). It lives for the daemon's run only.
type turnEnds struct {
	byTurn map[string]turnEnd
	order  []string
}

type turnEnd struct {
	threadID string
	state    string
}

func (e *turnEnds) add(threadID, turnID, state string) {
	if e.byTurn == nil {
		e.byTurn = map[string]turnEnd{}
	}
	if len(e.order) >= maxTurnEnds {
		delete(e.byTurn, e.order[0])
		e.order = e.order[1:]
	}
	e.byTurn[turnID] = turnEnd{threadID: threadID, state: state}
	e.order = append(e.order, turnID)
}

type turn struct {
	id     string
	cancel context.CancelFunc
	done   chan struct{}
	// slot is the turn's place under MaxConcurrentTurns, released once its end is written.
	slot *slots.Ticket
	// stop and ended are how the turn ended, set before done is closed.
	stop  domain.StopReason
	ended int64
	// live is the turn's activity clock, guarded by Runtime.mu (REQ-AUT-008).
	live liveness
}

// ID prefixes (Art. 6; API §3).
const (
	prefixThread   = "thr"
	prefixMessage  = "msg"
	prefixToolCall = "tc"
	prefixTurn     = "trn"
)

// New builds the runtime. Turns run on a context derived from ctx; Close stops them.
func New(ctx context.Context, cfg Config) (*Runtime, error) {
	switch {
	case cfg.Store == nil:
		return nil, errors.New("agents: a Store is required")
	case cfg.Models == nil:
		return nil, errors.New("agents: a Models gateway is required")
	case cfg.Tools == nil:
		return nil, errors.New("agents: a tool Registry is required")
	case cfg.Bus == nil:
		return nil, errors.New("agents: a Bus is required")
	case cfg.NewID == nil:
		return nil, errors.New("agents: NewID is required")
	case cfg.Metrics == nil:
		return nil, errors.New("agents: Metrics are required")
	case cfg.Tracer == nil:
		return nil, errors.New("agents: a Tracer is required")
	}
	if cfg.Policy == nil {
		cfg.Policy = secdomain.BuiltinRules().Policy
	}
	if cfg.IsRepo == nil {
		cfg.IsRepo = func(dir string) bool {
			_, err := os.Lstat(filepath.Join(dir, ".git"))
			return err == nil
		}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.StallLocal <= 0 {
		cfg.StallLocal = DefaultStallLocal
	}
	if cfg.StallRemote <= 0 {
		cfg.StallRemote = DefaultStallRemote
	}
	if cfg.StallTick <= 0 {
		cfg.StallTick = DefaultStallTick
	}
	if cfg.MaxConcurrentTurns <= 0 {
		cfg.MaxConcurrentTurns = DefaultMaxConcurrentTurns
	}
	base, stop := context.WithCancel(context.WithoutCancel(ctx))
	r := &Runtime{
		cfg: cfg, base: base, stop: stop, slots: slots.New(cfg.MaxConcurrentTurns),
		turns: map[string]*turn{}, waiting: map[string]chan domain.Approval{},
	}
	r.wg.Add(1)
	go r.watchStalls()
	return r, nil
}

// Close cancels every running turn and waits for them to record how they ended.
func (r *Runtime) Close() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.stop()
	r.wg.Wait()
}

func (r *Runtime) now() int64 { return r.cfg.Now().UnixMilli() }

// Create makes a thread (API §5.19).
func (r *Runtime) Create(ctx context.Context, p domain.CreateParams) (domain.Thread, error) {
	p, err := p.Validate()
	if err != nil {
		return domain.Thread{}, err
	}
	now := r.now()
	t := domain.Thread{
		ID: r.cfg.NewID(prefixThread), Title: p.Title, Mode: p.Mode, Model: p.Model, ModelClass: p.ModelClass,
		Cwd: filepath.Clean(p.Cwd), State: domain.StateIdle, AttentionState: "idle", Ephemeral: p.Ephemeral,
		MaxSteps: p.MaxSteps, BudgetTokens: p.BudgetTokens, CreatedAt: now, UpdatedAt: now,
		WorkspaceID: p.Workspace, EgressVisibility: domain.EgressObserved,
	}
	if p.Worktree && r.cfg.Worktrees == nil {
		return domain.Thread{}, fmt.Errorf("%w: worktrees are not available", domain.ErrValidation)
	}
	if p.Agent != "" {
		if r.cfg.Agents == nil {
			return domain.Thread{}, fmt.Errorf("%w: hosted agents are not available", domain.ErrValidation)
		}
		// Refused before anything is written. A worktree is made from the same repository, so
		// its configuration is checked here too, and again as the agent is launched in it.
		if err := r.cfg.Agents.Check(ctx, p.Agent, t.Cwd); err != nil {
			return domain.Thread{}, err
		}
		t.Agent, t.EgressVisibility = p.Agent, domain.EgressUnobserved
	}
	if err := r.cfg.Store.CreateThread(ctx, t); err != nil {
		return domain.Thread{}, err
	}
	if p.Worktree {
		var err error
		if t, err = r.createWorktree(ctx, t, now); err != nil {
			return domain.Thread{}, err
		}
	}
	if t.Agent != "" {
		// The thread is kept when its agent does not start: the next thread.send launches it
		// again, as after a crash (REQ-ACP-006), and the answer says why it is not running.
		if err := r.openAgent(ctx, t); err != nil {
			t.AgentUnavailable = err
		}
	}
	return t, nil
}

// openAgent launches a hosted thread's agent where its turns run (REQ-ACP-001): in its active
// worktree, or in its own directory.
func (r *Runtime) openAgent(ctx context.Context, t domain.Thread) error {
	place, err := r.place(ctx, t)
	if err != nil {
		return err
	}
	return r.cfg.Agents.Open(ctx, ports.ExternalRequest{ThreadID: t.ID, Agent: t.Agent, Dir: place.Cwd})
}

// createWorktree makes the thread's worktree as thread.create asked (REQ-WT-001).
func (r *Runtime) createWorktree(ctx context.Context, t domain.Thread, now int64) (domain.Thread, error) {
	// The worktree's row points at the thread, so the thread is written first. When git refuses
	// the worktree, the thread keeps working without isolation and says why (REQ-WT-008); any
	// other failure takes it back.
	w, err := r.cfg.Worktrees.Create(ctx, t.ID, t.Cwd, "")
	if errors.Is(err, ports.ErrWorktreeRefused) {
		t.WorktreeRefusal = err
		return t, nil
	}
	if err != nil {
		return domain.Thread{}, errors.Join(err, r.cfg.Store.DeleteThread(context.WithoutCancel(ctx), t.ID))
	}
	if err := r.cfg.Store.SetWorktree(ctx, t.ID, w.ID, now); err != nil {
		return domain.Thread{}, err
	}
	t.WorktreeID = w.ID
	return t, nil
}

// CreateWorktree is worktree.create (REQ-WT-001): the thread's worktree, made while it has no
// turn running so a turn never changes directory part way.
func (r *Runtime) CreateWorktree(ctx context.Context, threadID, base string) (string, error) {
	if r.cfg.Worktrees == nil {
		return "", fmt.Errorf("%w: worktrees are not available", domain.ErrValidation)
	}
	var id string
	err := r.WhileIdle(ctx, threadID, func() error {
		thread, err := r.cfg.Store.Thread(ctx, threadID)
		if err != nil {
			return err
		}
		w, err := r.cfg.Worktrees.Create(ctx, thread.ID, thread.Cwd, base)
		if err != nil {
			return err
		}
		id = w.ID
		return r.cfg.Store.SetWorktree(ctx, thread.ID, w.ID, r.now())
	})
	return id, err
}

// WhileIdle implements ports.WorktreeThreads. It holds the thread's send lock, which a send
// holds until its turn is written as running, so no turn can start while fn runs. Whether one
// runs is read from the store, as a send reads it: a turn's end is written before
// thread.turn_finished is published, and its goroutine lets go of it only after, so a merge
// sent the moment a wait returns would otherwise be refused.
func (r *Runtime) WhileIdle(ctx context.Context, threadID string, fn func() error) error {
	if _, err := r.cfg.Store.Thread(ctx, threadID); err != nil {
		return err
	}
	lock := r.threadLock(threadID)
	lock.Lock()
	defer lock.Unlock()
	st, err := r.cfg.Store.TurnStatus(ctx, threadID)
	if err != nil {
		return err
	}
	if st.State == domain.StateRunning || st.State == domain.StateAwaitingApproval {
		return fmt.Errorf("%w: thread %s", domain.ErrTurnRunning, threadID)
	}
	return fn()
}

// InWorkspace implements ports.WorktreeThreads.
func (r *Runtime) InWorkspace(ctx context.Context, workspaceID string) ([]domain.Thread, error) {
	return r.cfg.Store.ThreadsInWorkspace(ctx, workspaceID)
}

// place is where a thread's turn runs: TurnPlace with the runtime's engine and repository test.
func (r *Runtime) place(ctx context.Context, thread domain.Thread) (Place, error) {
	return TurnPlace(ctx, r.cfg.Worktrees, r.cfg.IsRepo, thread)
}

// Place is where a thread's turn runs and under which mode it decides.
type Place struct {
	Cwd       string
	WriteRoot string
	// Mode is the mode the turn decides under: full-auto while the thread's flag is set and
	// its worktree is active (REQ-ORC-004), the thread's own mode otherwise.
	Mode secdomain.Mode
}

// TurnPlace is where a thread's turn runs: its cwd and write root, and the mode it decides
// under. A thread with an active worktree runs in it (REQ-WT-002): the worktree is the write
// root, and the cwd is the worktree's directory matching the thread's own, or the worktree's
// top when the worktree lacks it. Only then does the thread's full-auto flag apply.
// policy.explain asks it too, so an explanation and a turn decide in one place. wts may be
// nil: there are no worktrees then.
func TurnPlace(ctx context.Context, wts ports.Worktrees, isRepo func(string) bool, thread domain.Thread) (Place, error) {
	if wts != nil {
		w, ok, err := wts.Active(ctx, thread.ID)
		if err != nil {
			return Place{}, err
		}
		if ok {
			mode := thread.Mode
			if thread.FullAuto {
				mode = secdomain.ModeFullAuto
			}
			return Place{Cwd: worktreeCwd(w, thread.Cwd), WriteRoot: w.Path, Mode: mode}, nil
		}
	}
	if thread.Agent != "" {
		// A hosted agent without a worktree has the thread's directory as its write root
		// (REQ-ACP-001), not the repository around it.
		return Place{Cwd: thread.Cwd, WriteRoot: thread.Cwd, Mode: thread.Mode}, nil
	}
	return Place{Cwd: thread.Cwd, WriteRoot: secdomain.WriteRoot(thread.Cwd, isRepo), Mode: thread.Mode}, nil
}

// worktreeCwd maps cwd, a directory of the main working copy, to the worktree's.
func worktreeCwd(w ports.Worktree, cwd string) string {
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return w.Path
	}
	rel, err := filepath.Rel(w.RepoRoot, real)
	if err != nil || !filepath.IsLocal(rel) {
		return w.Path
	}
	dir := filepath.Join(w.Path, rel)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return w.Path
	}
	// The worktree is the agent's to write, links included: a directory that resolves out of
	// it would move the turn's rules files and git context out with it.
	top, err := filepath.EvalSymlinks(w.Path)
	if err != nil {
		return w.Path
	}
	if real, err := filepath.EvalSymlinks(dir); err != nil || !inside(top, real) {
		return w.Path
	}
	return dir
}

// inside reports whether path is root or below it.
func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && filepath.IsLocal(rel)
}

// Get reads a thread.
func (r *Runtime) Get(ctx context.Context, id string) (domain.Thread, error) {
	t, err := r.cfg.Store.Thread(ctx, id)
	return r.withStall(t), err
}

// List lists the threads that are not ephemeral.
func (r *Runtime) List(ctx context.Context) ([]domain.Thread, error) {
	threads, err := r.cfg.Store.Threads(ctx)
	for i := range threads {
		threads[i] = r.withStall(threads[i])
	}
	return threads, err
}

// Messages is a thread's history.
func (r *Runtime) Messages(ctx context.Context, threadID string) ([]domain.Message, error) {
	if _, err := r.cfg.Store.Thread(ctx, threadID); err != nil {
		return nil, err
	}
	return r.cfg.Store.Messages(ctx, threadID)
}

// Update changes a thread's mode, model or title (API §5.22). A model change takes effect
// from the next turn, since a turn reads its thread once when it starts (REQ-AGT-010).
func (r *Runtime) Update(ctx context.Context, id string, p domain.UpdateParams) (domain.Thread, error) {
	if err := p.Validate(); err != nil {
		return domain.Thread{}, err
	}
	if p.Mode != nil && *p.Mode == secdomain.ModeFullAuto {
		// REQ-ORC-004: full-auto only inside an active worktree. Active also checks the worktree
		// is still on disk; the store checks the row again in the write that sets the flag.
		if _, err := r.cfg.Store.Thread(ctx, id); err != nil {
			return domain.Thread{}, err
		}
		active := false
		if r.cfg.Worktrees != nil {
			_, ok, err := r.cfg.Worktrees.Active(ctx, id)
			if err != nil {
				return domain.Thread{}, err
			}
			active = ok
		}
		if !active {
			return domain.Thread{}, fmt.Errorf("%w: thread %s has no active worktree", domain.ErrValidation, id)
		}
	}
	return r.cfg.Store.UpdateThread(ctx, id, p, r.now())
}

// Send starts a turn with the user's message (API §5.20, REQ-AGT-001). It returns once the
// message is persisted; the answer arrives as notifications. A client id the thread already
// has returns the original turn and message, starting nothing (REQ-AGT-015).
func (r *Runtime) Send(ctx context.Context, p ports.SendParams) (ports.SendResult, error) {
	if n := utf8.RuneCountInString(p.Text); n < 1 || n > domain.MaxMessageChars {
		return ports.SendResult{}, fmt.Errorf("%w: text must be 1-%d characters", domain.ErrValidation, domain.MaxMessageChars)
	}
	if p.ClientMsgID != "" && !isULID(p.ClientMsgID) {
		return ports.SendResult{}, fmt.Errorf("%w: client_msg_id is not a ULID", domain.ErrValidation)
	}
	thread, err := r.cfg.Store.Thread(ctx, p.ThreadID)
	if err != nil {
		return ports.SendResult{}, err
	}
	lock := r.threadLock(thread.ID)
	lock.Lock()
	defer lock.Unlock()

	if p.RejectBlocked {
		// Read again under the lock: the state read above may predate an approval.
		st, err := r.cfg.Store.TurnStatus(ctx, thread.ID)
		if err != nil {
			return ports.SendResult{}, err
		}
		if st.State == domain.StateAwaitingApproval {
			return ports.SendResult{}, fmt.Errorf("%w: thread %s", domain.ErrThreadBlocked, thread.ID)
		}
	}
	if p.ClientMsgID != "" {
		orig, found, err := r.cfg.Store.MessageByClientID(ctx, p.ThreadID, p.ClientMsgID)
		if err != nil {
			return ports.SendResult{}, err
		}
		if found {
			return ports.SendResult{TurnID: orig.TurnID, MessageID: orig.ID}, nil
		}
	}
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return ports.SendResult{}, errors.New("agents: the runtime is closed")
	}
	if thread.Agent != "" {
		if r.cfg.Agents == nil {
			return ports.SendResult{}, fmt.Errorf("%w: hosted agents are not available", domain.ErrValidation)
		}
	} else if !r.cfg.Models.Available(ctx, thread.ModelClass, thread.Model) {
		return ports.SendResult{}, fmt.Errorf("%w: class %q, model %q", domain.ErrProviderUnavailable, thread.ModelClass, thread.Model)
	}
	content, attachments, err := r.userContent(ctx, thread, p)
	if err != nil {
		return ports.SendResult{}, err
	}
	msg := domain.Message{
		ID: r.cfg.NewID(prefixMessage), ThreadID: thread.ID, TurnID: r.cfg.NewID(prefixTurn), Role: domain.RoleUser,
		Content: content, ClientMsgID: p.ClientMsgID, Attachments: attachments, CreatedAt: r.now(),
	}
	// The place is taken before the turn is written, so the store says queued from the
	// moment the send returns, and turns are granted in the order their sends got here.
	slot := r.slots.Enqueue()
	stored, running, err := r.beginTurn(ctx, msg, !slot.Granted())
	if err != nil {
		slot.Release()
	}
	if errors.Is(err, ports.ErrDuplicate) {
		return ports.SendResult{TurnID: stored.TurnID, MessageID: stored.ID}, nil
	}
	if err != nil {
		return ports.SendResult{}, err
	}
	if !r.start(running, msg, slot) { //nolint:contextcheck // the turn outlives the request: it runs on the runtime's context
		slot.Release()
		return ports.SendResult{}, errors.New("agents: the runtime is closed")
	}
	return ports.SendResult{TurnID: msg.TurnID, MessageID: msg.ID}, nil
}

// beginTurn persists the user's message and marks the thread running and `working`, queued
// when its turn waits for a slot, then announces the attention state, under the thread's
// attention lock.
func (r *Runtime) beginTurn(ctx context.Context, msg domain.Message, queued bool) (domain.Message, domain.Thread, error) {
	lock := r.attentionLock(msg.ThreadID)
	lock.Lock()
	defer lock.Unlock()
	stored, running, err := r.cfg.Store.BeginTurn(ctx, msg, queued, r.maxCost(), r.now())
	if err == nil {
		r.publish(ports.ThreadAttentionChanged{ThreadID: msg.ThreadID, AttentionState: running.AttentionState})
	}
	return stored, running, err
}

// maxCost is the thread cost cap now in force, 0 when there is none.
func (r *Runtime) maxCost() int64 {
	if r.cfg.MaxCostMicroUSD == nil {
		return 0
	}
	return r.cfg.MaxCostMicroUSD()
}

// Status reads what a wait pins (DD-011): the thread's state, its attention state and its
// latest turn, in one consistent reading.
func (r *Runtime) Status(ctx context.Context, threadID string) (ports.Status, error) {
	return r.cfg.Store.TurnStatus(ctx, threadID)
}

// TurnEnd is how a turn of this run ended, as its end event said; false for a turn still
// running, another thread's, or one the runtime no longer remembers.
func (r *Runtime) TurnEnd(threadID, turnID string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	end, ok := r.ends.byTurn[turnID]
	if !ok || end.threadID != threadID {
		return "", false
	}
	return end.state, true
}

// AwaitingApproval reports whether the thread's running turn waits for an approval: a hosted
// agent is not silent then (REQ-ACP-006).
func (r *Runtime) AwaitingApproval(threadID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.turns[threadID]
	return t != nil && t.live.awaiting
}

func (r *Runtime) threadLock(id string) *sync.Mutex {
	lock, _ := r.sendMu.LoadOrStore(id, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

// userContent is the user's text with its attachments read and rendered (REQ-CTX-002): what
// the model reads, and so what is persisted (REQ-AGT-011).
func (r *Runtime) userContent(ctx context.Context, thread domain.Thread, p ports.SendParams) (string, []domain.Attachment, error) {
	var read []ctxdomain.Attachment
	var recorded []domain.Attachment
	cwd := thread.Cwd
	if len(p.Attachments) > 0 {
		// A relative path names a file where the turn will run: in the worktree, if any.
		place, err := r.place(ctx, thread)
		if err != nil {
			return "", nil, err
		}
		cwd = place.Cwd
	}
	for _, ref := range p.Attachments {
		a, err := r.attach(ctx, cwd, ref)
		if err != nil {
			return "", nil, err
		}
		read = append(read, a)
		recorded = append(recorded, domain.Attachment{Kind: string(a.Kind), Ref: a.Ref, Bytes: a.Bytes, TruncatedBytes: a.TruncatedBytes})
	}
	content, err := ctxdomain.RenderUser(ctxdomain.UserInput{Text: p.Text, Attachments: read})
	if err != nil {
		return "", nil, fmt.Errorf("agents: render the message: %w", err)
	}
	return content, recorded, nil
}

// attach reads one attachment. A `stdin` one arrived inline with the request, so it needs no
// gatherer; the others are read from the machine.
func (r *Runtime) attach(ctx context.Context, cwd string, ref ports.AttachmentRef) (ctxdomain.Attachment, error) {
	if ctxdomain.Kind(ref.Kind) == ctxdomain.KindStdin {
		return ctxdomain.NewStdinAttachment(ref.Data, ref.Truncated), nil
	}
	if r.cfg.Context == nil {
		return ctxdomain.Attachment{}, fmt.Errorf("%w: attachments are not available", domain.ErrValidation)
	}
	a, err := r.cfg.Context.Attach(ctx, cwd, ctxdomain.Ref{Kind: ctxdomain.Kind(ref.Kind), Ref: ref.Ref})
	if err != nil {
		return ctxdomain.Attachment{}, fmt.Errorf("%w: %w", domain.ErrValidation, err)
	}
	return a, nil
}

// start runs the turn in its own goroutine, on the runtime's context rather than the
// request's: the turn outlives the thread.send call that started it. A closed runtime starts
// nothing. The turn owns slot from here, and releases it once its end is written.
func (r *Runtime) start(thread domain.Thread, msg domain.Message, slot *slots.Ticket) bool {
	ctx, cancel := context.WithCancel(r.base)
	t := &turn{id: msg.TurnID, cancel: cancel, done: make(chan struct{}), slot: slot, live: liveness{
		window: r.stallWindow(ctx, thread), lastAt: r.cfg.Now(), lastEvent: activityTurnStarted, queued: thread.Queued,
	}}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		cancel()
		return false
	}
	r.turns[thread.ID] = t
	r.wg.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.wg.Done()
		defer close(t.done)
		defer cancel()
		defer slot.Release()
		t.stop, t.ended = r.runTurn(ctx, thread, msg, slot)
		r.mu.Lock()
		if r.turns[thread.ID] == t {
			delete(r.turns, thread.ID)
		}
		r.mu.Unlock()
	}()
	return true
}

// publish puts an event on the bus. It is called only after the write it reports.
func (r *Runtime) publish(e bus.Event) {
	r.observe(e)
	r.cfg.Bus.Publish(e)
}

// isULID reports whether s is a ULID: 26 characters of Crockford's base32.
func isULID(s string) bool {
	if len(s) != 26 || s[0] > '7' {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'A' && c <= 'H', c == 'J', c == 'K', c == 'M', c == 'N',
			c >= 'P' && c <= 'T', c >= 'V' && c <= 'Z':
		default:
			return false
		}
	}
	return true
}

// Busy reports whether a turn is running or queued. A daemon handoff refuses while one is
// (REQ-HND-002, delta `2026-10-pty-handoff`): its tool calls wait on this daemon's sessions.
func (r *Runtime) Busy() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.turns) > 0
}
