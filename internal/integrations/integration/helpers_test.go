package integration_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/integrations"
	"github.com/ecrespo/umbral/internal/integrations/adapters/reportstore"
	"github.com/ecrespo/umbral/internal/integrations/ports"
	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/store"
	"github.com/ecrespo/umbral/internal/workspaces"
	"github.com/ecrespo/umbral/internal/workspaces/adapters/treestore"
	wsdomain "github.com/ecrespo/umbral/internal/workspaces/domain"
	wsports "github.com/ecrespo/umbral/internal/workspaces/ports"
)

// fakeTerminals hands every pane a session id and writes the `sessions` row its foreign key
// needs; the PTY itself is not what these tests are about.
type fakeTerminals struct {
	mu sync.Mutex
	db *store.Store
	n  int
}

func (f *fakeTerminals) Create(ctx context.Context, _ sessdomain.CreateParams) (sessdomain.Session, error) {
	f.mu.Lock()
	f.n++
	id := fmt.Sprintf("ses_%026d", f.n)
	f.mu.Unlock()
	if _, err := f.db.DB().ExecContext(ctx, `
		INSERT INTO sessions(id, shell, cwd, cols, rows, state, created_at)
		VALUES (?, '/bin/sh', '/tmp', 80, 24, 'alive', 1757592000000)`, id); err != nil {
		return sessdomain.Session{}, err
	}
	return sessdomain.Session{ID: id, State: sessdomain.StateAlive}, nil
}

func (f *fakeTerminals) Close(context.Context, string) error { return nil }

type harness struct {
	*integrations.Service
	tree   *workspaces.Service
	store  *store.Store
	events *bus.Subscription
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := store.Open(t.Context(), store.Options{Path: filepath.Join(t.TempDir(), "umbral.db")})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	eventBus := bus.New()
	t.Cleanup(eventBus.Close)
	events := eventBus.SubscribeBuffered(256, ports.KindPaneStateChanged, wsports.KindWorkspaceUpdated)
	t.Cleanup(events.Close)

	treeStore, err := treestore.New(db)
	if err != nil {
		t.Fatalf("treestore.New: %v", err)
	}
	// The tree gets a bus of its own: its notifications are not what these tests read.
	treeBus := bus.New()
	t.Cleanup(treeBus.Close)
	tree, err := workspaces.New(workspaces.Config{
		Tree: treeStore, Terminals: &fakeTerminals{db: db}, Bus: treeBus,
	})
	if err != nil {
		t.Fatalf("workspaces.New: %v", err)
	}
	reports, err := reportstore.New(db)
	if err != nil {
		t.Fatalf("reportstore.New: %v", err)
	}
	// A clock that never moves: every report lands in the same millisecond, which is what
	// two hooks firing together look like, and the order must still be the order received.
	frozen := time.UnixMilli(1757592000000)
	service, err := integrations.New(integrations.Config{
		Store: reports, Panes: tree, Bus: eventBus, Now: func() time.Time { return frozen },
	})
	if err != nil {
		t.Fatalf("integrations.New: %v", err)
	}
	return &harness{Service: service, tree: tree, store: db, events: events}
}

// newPane creates a workspace whose root pane runs a shell that spoke OSC 133, so Umbral's
// own detection says `idle` before anything reports.
func (h *harness) newPane(t *testing.T) wsdomain.Tree {
	t.Helper()
	tree, err := h.tree.CreateWorkspace(t.Context(), wsdomain.CreateWorkspaceParams{
		CWD: t.TempDir(), Label: "int", TabLabel: "int", Focus: true,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	h.exec(t, `UPDATE sessions SET integration = 'osc133' WHERE id = ?`, tree.RootPane.SessionID)
	if err := h.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	h.drain()
	return tree
}

func (h *harness) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := h.store.DB().ExecContext(t.Context(), query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// pane reads a pane's effective state back through the tree.
func (h *harness) pane(t *testing.T, id string) wsdomain.Pane {
	t.Helper()
	pane, err := h.tree.GetPane(t.Context(), id)
	if err != nil {
		t.Fatalf("GetPane(%s): %v", id, err)
	}
	return pane
}

// drain returns every event published so far.
func (h *harness) drain() []bus.Event {
	var out []bus.Event
	for {
		select {
		case ev := <-h.events.C():
			out = append(out, ev)
		case <-time.After(20 * time.Millisecond):
			return out
		}
	}
}

func stateChanges(events []bus.Event) []ports.PaneStateChanged {
	var out []ports.PaneStateChanged
	for _, ev := range events {
		if change, ok := ev.(ports.PaneStateChanged); ok {
			out = append(out, change)
		}
	}
	return out
}

func rollups(events []bus.Event) []wsdomain.AttentionState {
	var out []wsdomain.AttentionState
	for _, ev := range events {
		if update, ok := ev.(wsports.WorkspaceEvent); ok {
			out = append(out, update.Workspace.RollupState)
		}
	}
	return out
}

func seq(n int64) *int64 { return &n }
