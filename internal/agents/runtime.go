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
	toolsports "github.com/ecrespo/umbral/internal/tools/ports"
)

// Config wires the runtime. Store, Models, Tools and Bus are required.
type Config struct {
	Store  ports.Store
	Models ports.Models
	Tools  toolsports.Registry
	Bus    ports.Publisher
	// Context reads rules files, git state and attachments; nil sends none of them.
	Context ctxports.Gatherer
	// Approver answers the policy's `ask` (T-F1-14). Without one an `ask` is refused as
	// denied_by_policy: no tool runs outside a decision that allowed it.
	Approver ports.Approver
	// NewID makes a type-prefixed ULID (store.NewID).
	NewID func(prefix string) string
	// IsRepo says whether a directory is a repository root, for the write root; nil looks for
	// a `.git` entry.
	IsRepo func(dir string) bool
	Now    func() time.Time
	Logger *slog.Logger
}

// Runtime is ports.Threads.
type Runtime struct {
	cfg  Config
	base context.Context
	stop context.CancelFunc

	// sendMu serialises thread.send per thread: the duplicate check and the conflict check
	// must see each other's writes (REQ-AGT-015).
	sendMu sync.Map // thread id → *sync.Mutex

	mu     sync.Mutex
	closed bool
	turns  map[string]*turn // thread id → running turn
	wg     sync.WaitGroup
}

var _ ports.Threads = (*Runtime)(nil)

// turn is a running turn, which thread.cancel (T-F1-16) stops through cancel.
type turn struct {
	id     string
	cancel context.CancelFunc
	done   chan struct{}
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
	base, stop := context.WithCancel(context.WithoutCancel(ctx))
	return &Runtime{cfg: cfg, base: base, stop: stop, turns: map[string]*turn{}}, nil
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
	}
	if err := r.cfg.Store.CreateThread(ctx, t); err != nil {
		return domain.Thread{}, err
	}
	return t, nil
}

// Get reads a thread.
func (r *Runtime) Get(ctx context.Context, id string) (domain.Thread, error) {
	return r.cfg.Store.Thread(ctx, id)
}

// List lists the threads that are not ephemeral.
func (r *Runtime) List(ctx context.Context) ([]domain.Thread, error) {
	return r.cfg.Store.Threads(ctx)
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
	if !r.cfg.Models.Available(ctx, thread.ModelClass, thread.Model) {
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
	stored, running, err := r.cfg.Store.BeginTurn(ctx, msg, r.now())
	if errors.Is(err, ports.ErrDuplicate) {
		return ports.SendResult{TurnID: stored.TurnID, MessageID: stored.ID}, nil
	}
	if err != nil {
		return ports.SendResult{}, err
	}
	if !r.start(running, msg) { //nolint:contextcheck // the turn outlives the request: it runs on the runtime's context
		return ports.SendResult{}, errors.New("agents: the runtime is closed")
	}
	return ports.SendResult{TurnID: msg.TurnID, MessageID: msg.ID}, nil
}

func (r *Runtime) threadLock(id string) *sync.Mutex {
	lock, _ := r.sendMu.LoadOrStore(id, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

// userContent is the user's text with its attachments read and rendered (REQ-CTX-002): what
// the model reads, and so what is persisted (REQ-AGT-011).
func (r *Runtime) userContent(ctx context.Context, thread domain.Thread, p ports.SendParams) (string, []domain.Attachment, error) {
	if len(p.Attachments) > 0 && r.cfg.Context == nil {
		return "", nil, fmt.Errorf("%w: attachments are not available", domain.ErrValidation)
	}
	var read []ctxdomain.Attachment
	var recorded []domain.Attachment
	for _, ref := range p.Attachments {
		a, err := r.cfg.Context.Attach(ctx, thread.Cwd, ctxdomain.Ref{Kind: ctxdomain.Kind(ref.Kind), Ref: ref.Ref})
		if err != nil {
			return "", nil, fmt.Errorf("%w: %w", domain.ErrValidation, err)
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

// start runs the turn in its own goroutine, on the runtime's context rather than the
// request's: the turn outlives the thread.send call that started it. A closed runtime starts
// nothing.
func (r *Runtime) start(thread domain.Thread, msg domain.Message) bool {
	ctx, cancel := context.WithCancel(r.base)
	t := &turn{id: msg.TurnID, cancel: cancel, done: make(chan struct{})}
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
		r.runTurn(ctx, thread, msg.TurnID)
		r.mu.Lock()
		if r.turns[thread.ID] == t {
			delete(r.turns, thread.ID)
		}
		r.mu.Unlock()
	}()
	return true
}

// publish puts an event on the bus. It is called only after the write it reports.
func (r *Runtime) publish(e bus.Event) { r.cfg.Bus.Publish(e) }

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
