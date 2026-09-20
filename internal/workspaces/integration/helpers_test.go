package integration_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/store"
	"github.com/ecrespo/umbral/internal/workspaces"
	"github.com/ecrespo/umbral/internal/workspaces/adapters/treestore"
	"github.com/ecrespo/umbral/internal/workspaces/domain"
	"github.com/ecrespo/umbral/internal/workspaces/ports"
)

// fakeTerminals stands in for the sessions module.
//
// A real PTY is not what these tests are about: the tree's job is to hand a pane an
// identifier and remember it, and forking a shell per pane would make the suite slow
// without testing anything the sessions package does not already cover. What the fake does
// have to be is honest about *when* it is called, which is why it records every create and
// close in order.
type fakeTerminals struct {
	mu      sync.Mutex
	created []string
	closed  []string
	failOn  int
	n       int
}

func (f *fakeTerminals) Create(_ context.Context, _ sessdomain.CreateParams) (sessdomain.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	if f.failOn != 0 && f.n == f.failOn {
		return sessdomain.Session{}, fmt.Errorf("fake: refusing to start terminal %d", f.n)
	}
	// The prefix matches what store.NewID produces, so a pane's session_id is the shape
	// the sessions foreign key would see.
	id := fmt.Sprintf("ses_%026d", f.n)
	f.created = append(f.created, id)
	return sessdomain.Session{ID: id, State: sessdomain.StateAlive}, nil
}

func (f *fakeTerminals) Close(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, id)
	return nil
}

func (f *fakeTerminals) createdIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.created...)
}

func (f *fakeTerminals) closedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.closed...)
}

// harness is the service over a real database.
type harness struct {
	*workspaces.Service
	terminals *fakeTerminals
	store     *store.Store
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	db, err := store.Open(t.Context(), store.Options{
		Path: filepath.Join(t.TempDir(), "umbral.db"),
	})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("store.Close: %v", err)
		}
	})

	// Panes hold a foreign key on sessions(id), so a fake terminal's id has to exist as a
	// row before it can be attached. Inserting them here keeps the fake from having to
	// know about SQLite.
	tree, err := treestore.New(db)
	if err != nil {
		t.Fatalf("treestore.New: %v", err)
	}
	terminals := &fakeTerminals{}
	service, err := workspaces.New(workspaces.Config{
		Tree: &sessionRegistering{Tree: tree, db: db, t: t}, Terminals: terminals,
	})
	if err != nil {
		t.Fatalf("workspaces.New: %v", err)
	}
	return &harness{Service: service, terminals: terminals, store: db}
}

// newWorkspace creates a workspace and fails the test if it cannot.
func (h *harness) newWorkspace(t *testing.T, label string) domain.Tree {
	t.Helper()
	tree, err := h.CreateWorkspace(t.Context(), domain.CreateWorkspaceParams{
		CWD: t.TempDir(), Label: label, TabLabel: label,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	return tree
}

// sessionRegistering inserts the `sessions` row a pane's foreign key needs before the
// attach that references it.
//
// In the daemon that row is written by the sessions module, which these tests replace with
// a fake. Faking the row too would mean dropping the foreign key from the test database,
// and that key is exactly what makes `AttachSession` fail loudly when a pane is handed a
// terminal that does not exist — a guarantee worth keeping under test rather than under a
// comment.
type sessionRegistering struct {
	ports.Tree
	db *store.Store
	t  *testing.T
}

func (s *sessionRegistering) AttachSession(ctx context.Context, paneID, sessionID string) error {
	_, err := s.db.DB().ExecContext(ctx, `
		INSERT OR IGNORE INTO sessions(id, shell, cwd, cols, rows, state, created_at)
		VALUES (?, '/bin/sh', '/tmp', 80, 24, 'alive', 1757592000000)`, sessionID)
	if err != nil {
		s.t.Fatalf("register the fake session %s: %v", sessionID, err)
	}
	return s.Tree.AttachSession(ctx, paneID, sessionID)
}
