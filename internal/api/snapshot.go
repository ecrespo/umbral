package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	remotedomain "github.com/ecrespo/umbral/internal/remote/domain"

	wsdomain "github.com/ecrespo/umbral/internal/workspaces/domain"
)

// `session.snapshot` (API Spec §5.3, REQ-API-001).
//
// It is the bootstrap for a client that keeps its own cache: subscribe first, buffer what
// arrives, take the snapshot, install it, then apply only the buffered events whose `seq` is
// greater than the one the snapshot reports. What makes that work is the order below, not
// the contents.

// handleSessionSnapshot returns the tree and the sequence number it contains.
//
// **The counter is read before the tree, and that ordering is the whole correctness
// argument.** The daemon persists before it notifies (DD-007), so an event that has been
// given a `seq` is already in the database. Reading the counter first therefore guarantees
// that everything at or below the number reported here is in this result. Reading it
// afterwards would admit the opposite: an event dispatched while the tree was being read
// would carry a number below the reported one, the client would discard it as already
// contained, and it would be in neither — a lost update with nothing to notice it.
//
// The result may reflect a few events *above* the reported number, because the tree is read
// after they landed. That makes the client apply something it already has, which every tree
// notification tolerates: they carry whole records, not deltas.
func handleSessionSnapshot(ctx context.Context, c *conn, _ json.RawMessage) (any, error) {
	local, err := localSnapshot(ctx, c)
	if err != nil {
		return nil, err
	}
	hosts := c.connectedHosts(ctx)
	if len(hosts) == 0 {
		return local, nil
	}
	// The hosts' trees follow this one's, each prefixed (REQ-RMT-001). Asking them is a wait,
	// so it runs beside the read loop; the counter was read above, before this tree, and every
	// host notification relayed from here on takes a number above it.
	return deferred(func(ctx context.Context) (any, error) {
		// Every host is asked at once, so the merge waits for the slowest host's 5 s at most;
		// the trees are appended in the hosts' order.
		trees := make([]*snapshotResult, len(hosts))
		var wg sync.WaitGroup
		for i, host := range hosts {
			wg.Go(func() { trees[i] = hostSnapshot(ctx, c, host) })
		}
		wg.Wait()
		for _, theirs := range trees {
			if theirs != nil {
				mergeInto(&local, theirs)
			}
		}
		return local, nil
	}), nil
}

// relaySnapshotTimeout bounds how long one host may hold up a merged snapshot; a host that does
// not answer is left out, and its `remote.state_changed` tells the client when to ask again.
const relaySnapshotTimeout = 5 * time.Second

// connectedHosts names the hosts whose links are up.
func (c *conn) connectedHosts(ctx context.Context) []string {
	svc := c.server.cfg.Remotes
	if svc == nil {
		return nil
	}
	list, err := svc.List(ctx)
	if err != nil {
		return nil
	}
	var out []string
	for _, s := range list {
		if s.State == remotedomain.StateConnected {
			out = append(out, s.Host.Name)
		}
	}
	return out
}

// hostSnapshot reads a host's tree and threads, with its ids prefixed and its workspaces
// labelled; nil leaves the host out.
func hostSnapshot(ctx context.Context, c *conn, host string) *snapshotResult {
	ctx, cancel := context.WithTimeout(ctx, relaySnapshotTimeout)
	defer cancel()
	raw, err := c.relayCall(ctx, host, "session.snapshot", nil)
	if err == nil {
		raw, err = prefixIDs(raw, host)
	}
	var theirs snapshotResult
	if err == nil {
		err = json.Unmarshal(raw, &theirs)
	}
	if err != nil {
		c.logger.Info("a remote host's snapshot is left out", slog.String("host", host), slog.Any("error", err))
		return nil
	}
	return &theirs
}

// mergeInto appends a host's snapshot to the local one. The focus stays this daemon's.
func mergeInto(into, theirs *snapshotResult) {
	into.Workspaces = append(into.Workspaces, theirs.Workspaces...)
	into.Tabs = append(into.Tabs, theirs.Tabs...)
	into.Panes = append(into.Panes, theirs.Panes...)
	into.Layouts = append(into.Layouts, theirs.Layouts...)
	into.Threads = append(into.Threads, theirs.Threads...)
}

// localSnapshot is this daemon's own tree and threads.
func localSnapshot(ctx context.Context, c *conn) (snapshotResult, error) {
	tree, err := c.tree()
	if err != nil {
		return snapshotResult{}, err
	}

	seq := c.server.currentSeq()

	snapshot, err := tree.Snapshot(ctx)
	if err != nil {
		return snapshotResult{}, err
	}

	workspaces := make([]Workspace, 0, len(snapshot.Workspaces))
	for _, ws := range snapshot.Workspaces {
		workspaces = append(workspaces, toWireWorkspace(ws))
	}
	tabs := make([]Tab, 0, len(snapshot.Tabs))
	for _, tab := range snapshot.Tabs {
		tabs = append(tabs, toWireTab(tab))
	}
	panes := make([]Pane, 0, len(snapshot.Panes))
	for _, pane := range snapshot.Panes {
		panes = append(panes, toWirePane(pane))
	}
	layouts := make([]Layout, 0, len(snapshot.Layouts))
	for _, layout := range snapshot.Layouts {
		layouts = append(layouts, toWireLayout(layout))
	}

	focus := toWireFocus(snapshot.Focus)
	// Threads are not part of the tree: the agent runtime lists them and knows which one a
	// client views (REQ-AGT-016). Without a runtime the member is present and empty, so a
	// client iterating the result does not branch on which build it is talking to.
	threadList := []Thread{}
	if svc := c.server.cfg.Threads; svc != nil {
		list, err := svc.List(ctx)
		if err != nil {
			return snapshotResult{}, err
		}
		for _, t := range list {
			threadList = append(threadList, toWireThread(t))
		}
		focus.ThreadID = emptyAsNull(svc.Focused())
	}

	return snapshotResult{
		Seq:        seq,
		Focused:    focus,
		Workspaces: workspaces,
		Tabs:       tabs,
		Panes:      panes,
		Layouts:    layouts,
		Threads:    threadList,
	}, nil
}

// Focus is API Spec §5.3's `focused` object.
type Focus struct {
	WorkspaceID *string `json:"workspace_id"`
	TabID       *string `json:"tab_id"`
	// ThreadID is always null in F0 and typed `string | null` by §5.3. It is here rather
	// than omitted so the shape does not change when threads arrive.
	ThreadID *string `json:"thread_id"`
}

func toWireFocus(f wsdomain.Focus) Focus {
	return Focus{
		WorkspaceID: emptyAsNull(f.WorkspaceID),
		TabID:       emptyAsNull(f.TabID),
		ThreadID:    emptyAsNull(f.ThreadID),
	}
}
