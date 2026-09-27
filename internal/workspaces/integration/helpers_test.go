package integration_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ecrespo/umbral/internal/bus"
	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/store"
	"github.com/ecrespo/umbral/internal/workspaces"
	"github.com/ecrespo/umbral/internal/workspaces/adapters/treestore"
	"github.com/ecrespo/umbral/internal/workspaces/domain"
	wsports "github.com/ecrespo/umbral/internal/workspaces/ports"
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
	// launched records the parameters each session was started with, because "the command
	// was stored" and "the command was run" are different claims and REQ-WS-005 makes the
	// second one.
	launched []sessdomain.CreateParams
	failOn   int
	n        int
}

func (f *fakeTerminals) Create(_ context.Context, params sessdomain.CreateParams) (sessdomain.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	f.launched = append(f.launched, params)
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

// failAfter makes the terminal after the nth refuse to start, which is how a test reaches
// the one failure `treestore.ApplyLayout`'s transaction cannot cover.
func (f *fakeTerminals) failAfter(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failOn = f.n + n + 1
}

// count reports how many terminals this fake has been asked for.
func (f *fakeTerminals) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
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
	// screens reads what the capture wrote, so a test asserts on the database rather than
	// on what the service reported writing.
	screens *treestore.Store
	events  *bus.Subscription
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
	// A real bus, subscribed to every kind the tree publishes. The §6 notifications are
	// part of what this module does, and a service that changed the tree correctly while
	// telling nobody would pass every other test here.
	eventBus := bus.New()
	t.Cleanup(eventBus.Close)
	events := eventBus.SubscribeBuffered(256,
		wsports.KindWorkspaceCreated, wsports.KindWorkspaceUpdated,
		wsports.KindWorkspaceClosed, wsports.KindWorkspaceFocused,
		wsports.KindTabCreated, wsports.KindTabClosed, wsports.KindTabFocused,
		wsports.KindPaneCreated, wsports.KindPaneUpdated, wsports.KindPaneClosed,
		wsports.KindPaneFocused, wsports.KindPaneMoved, wsports.KindLayoutUpdated)
	t.Cleanup(events.Close)

	service, err := workspaces.New(workspaces.Config{
		Tree: &sessionRegistering{Tree: tree, db: db, t: t}, Terminals: terminals,
		Bus: eventBus,
	})
	if err != nil {
		t.Fatalf("workspaces.New: %v", err)
	}
	return &harness{Service: service, terminals: terminals, store: db, events: events}
}

// newWorkspace creates a workspace and fails the test if it cannot.
func (h *harness) newWorkspace(t *testing.T, label string) domain.Tree {
	t.Helper()
	// Focus is passed explicitly because the `focus?: true` default of API Spec §5.4 is
	// applied at the wire layer, not here: the service honours what it is given, and a
	// test that relied on the default would be testing `internal/api` from the wrong side.
	tree, err := h.CreateWorkspace(t.Context(), domain.CreateWorkspaceParams{
		CWD: t.TempDir(), Label: label, TabLabel: label, Focus: true,
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
	wsports.Tree
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

// isValidation reports whether an error is the module's validation sentinel.
func isValidation(err error) bool { return errors.Is(err, domain.ErrValidation) }

// sprintf keeps the shape comparison's formatting in one place.
func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// launchedWith reports the parameters a session was started with.
func (f *fakeTerminals) launchedWith(sessionID string) (sessdomain.CreateParams, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, id := range f.created {
		if id == sessionID {
			return f.launched[i], true
		}
	}
	return sessdomain.CreateParams{}, false
}

// drain collects the events published so far, without waiting: everything the service
// publishes it publishes synchronously, before the call it belongs to returns.
func (h *harness) drain() []bus.Event {
	var out []bus.Event
	for {
		select {
		case ev, open := <-h.events.C():
			if !open {
				return out
			}
			out = append(out, ev)
		default:
			return out
		}
	}
}

// kindsOf names the events drained, for an assertion that reads like the sequence it checks.
func kindsOf(events []bus.Event) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, string(ev.EventKind()))
	}
	return out
}

// countKind counts one kind among the drained events.
func countKind(events []bus.Event, kind bus.Kind) int {
	n := 0
	for _, ev := range events {
		if ev.EventKind() == kind {
			n++
		}
	}
	return n
}

// restart builds a second service over the same database, which is what a daemon restart is:
// the rows survive and the processes do not.
//
// New fake terminals on purpose. Reusing the first set would let a restored pane point at a
// session that is still "alive" in the fake, and the one thing a restart has to get right is
// that those are gone.
func (h *harness) restart(t *testing.T) *harness {
	t.Helper()

	tree, err := treestore.New(h.store)
	if err != nil {
		t.Fatalf("treestore.New on restart: %v", err)
	}
	// The counter starts past the first run's, so the identifiers differ the way a real
	// restart's do. Without it every restored pane would appear to have kept its previous
	// session, and the assertion that they are new would fail on an artefact of the fake
	// rather than on anything the daemon did.
	terminals := &fakeTerminals{n: h.terminals.count() + 1000}
	eventBus := bus.New()
	t.Cleanup(eventBus.Close)
	events := eventBus.SubscribeBuffered(256,
		wsports.KindWorkspaceCreated, wsports.KindWorkspaceUpdated,
		wsports.KindWorkspaceClosed, wsports.KindWorkspaceFocused,
		wsports.KindTabCreated, wsports.KindTabClosed, wsports.KindTabFocused,
		wsports.KindPaneCreated, wsports.KindPaneUpdated, wsports.KindPaneClosed,
		wsports.KindPaneFocused, wsports.KindPaneMoved, wsports.KindLayoutUpdated)
	t.Cleanup(events.Close)

	service, err := workspaces.New(workspaces.Config{
		Tree: &sessionRegistering{Tree: tree, db: h.store, t: t}, Terminals: terminals,
		Bus: eventBus,
	})
	if err != nil {
		t.Fatalf("workspaces.New on restart: %v", err)
	}
	return &harness{Service: service, terminals: terminals, store: h.store, events: events}
}

// fakeScreens hands the capture a fixed screen, so the test is about what the service does
// with it rather than about a real emulator.
type fakeScreens struct{ screen string }

func (f fakeScreens) Screen(context.Context, string) (wsports.Screen, error) {
	return wsports.Screen{Data: []byte(f.screen), Rows: 1}, nil
}

// allLaunched reports every set of parameters a terminal was started with.
func (f *fakeTerminals) allLaunched() []sessdomain.CreateParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sessdomain.CreateParams(nil), f.launched...)
}

func (h *harness) restartWithHistory(t *testing.T, on bool, screen string) *harness {
	t.Helper()
	next := h.restart(t)
	return rebuild(t, next, on, screen)
}

func newHarnessWithHistory(t *testing.T, on bool, screen string) *harness {
	t.Helper()
	return rebuild(t, newHarness(t), on, screen)
}

// rebuild replaces a harness's service with one configured for pane history, over the same
// database and the same fake terminals.
func rebuild(t *testing.T, h *harness, on bool, screen string) *harness {
	t.Helper()

	tree, err := treestore.New(h.store)
	if err != nil {
		t.Fatalf("treestore.New: %v", err)
	}
	eventBus := bus.New()
	t.Cleanup(eventBus.Close)

	service, err := workspaces.New(workspaces.Config{
		Tree: &sessionRegistering{Tree: tree, db: h.store, t: t}, Terminals: h.terminals,
		Bus: eventBus, PaneHistory: on, Screens: fakeScreens{screen: screen},
	})
	if err != nil {
		t.Fatalf("workspaces.New: %v", err)
	}
	h.Service = service
	h.screens = tree
	return h
}
