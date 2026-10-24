# Delta — the agent panel lists every thread of the focused workspace

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-23 by the implementing agent under the Tech Lead's standing F2 instruction`; folded into Tech 1.62 (§5.3e) |
| **Date** | 2026-10-23 |
| **Task** | T-F2-06 |
| **REQ** | REQ-ORC-003 |

## What the PRD and the task left open

REQ-ORC-003 lists "the threads whose `workspace_id` is the focused workspace", but the TUI has no
workspace of its own: it creates raw sessions, which belong to no pane, so the threads it created
belonged to no workspace. Tech §5.3e said "one thread per TUI run". Nothing said which key
switches, where the list comes from, or when it is read again.

## Decisions

1. **The focused workspace is the daemon's.** The panel reads `focused.workspace_id` and the
   threads from one `session.snapshot` (API §5.3), so the two cannot straddle a focus change.
   With none focused it lists the threads of no workspace, the ones a TUI made before; F2's
   workspace view in the TUI is not this task's.
2. **The panel's own thread joins that workspace.** `thread.create` is sent the focused
   workspace (§5.19's `workspace`), or none, so the thread the panel creates is listed with the
   rest. The selected thread is listed even while it belongs to another workspace or is not
   listed yet.
3. **Each row** is the thread's id, its `attention_state`, `queued` when held, its worktree id or
   `no worktree`, its count of pending approvals and its title, made one line. Six rows at most,
   the rest counted as `… n more`, so the transcript keeps the panel's height.
4. **Up and down in agent mode switch threads.** The input has no history, so the arrows are
   free; ctrl+n and ctrl+p are the TUI's tab and block keys. With none selected down takes the
   first and up the last. A switch is refused while the panel's thread is being created.
5. **The selected thread is the panel's thread.** It is the one `thread.focus` views (`syncView`
   follows it, REQ-AGT-016), the one shown, the one sent to and the one ctrl+c stops when no
   approval is shown. A message to a thread whose turn runs is refused in the client, as before;
   a thread is running once a delta or tool call of it arrives or a listing shows its `state`
   `running` or `awaiting_approval`, whoever started the turn — another client, a held turn
   starting — and idle again only at its turn's end or a cancel that stopped nothing. A listing
   never makes a thread idle: a send of this panel may not be in it yet.
6. **Transcripts are kept per thread, from what this run saw.** Every thread's deltas and tool
   calls are folded into its own view, so switching back finds what arrived meanwhile. A thread
   is not read back with `thread.get`: the panel shows the events it received, and earlier text
   is `umb thread get`'s. A view lives while its thread is listed: every listing drops the others,
   which bounds them, since an ephemeral thread (`umb ai`) is never listed.
7. **When the list is read again:** at start, whenever the panel opens, after the panel creates a
   thread, and on `approval.requested`, `thread.turn_finished`, `thread.attention_changed`,
   `workspace.focused` and `workspace.closed`. A held turn that starts announces nothing but its
   first event, so a delta or tool call of a thread listed `queued` lists again. A thread created
   elsewhere appears with its first attention change, when its first turn starts. Listings are
   asked for concurrently, so each carries its snapshot's `seq` (API §5.3) and one below the
   listing applied is dropped. Rows keep the daemon's order, most recently updated first.
8. **No second thread of the panel's own.** Once a thread is selected, messages go to a selected
   thread; there is no "none" entry to create another. A run creates at most one, as in F1.
9. **Approvals are unchanged:** every thread's, oldest first, with the chords of T-F1-20, and the
   row of a thread shows how many wait.

## Tests

- `TestAgentPanelListsEveryThread_REQ_ORC_003` (harness): rows with attention, worktree,
  `queued` and approvals; another workspace's thread not listed; down views a thread; a kept
  transcript; a refused send to a running thread; a send to the selected idle one; an approval of
  another thread answered; relisting on an attention change and on a workspace focus.
- `TestThePanelsThreadJoinsTheFocusedWorkspace_REQ_ORC_003`: `thread.create` is sent the focused
  workspace, or none, and with none focused the threads of none are listed.
- `TestThePanelListsAgainWhenAThreadChanges_REQ_ORC_003`: each trigger of decision 7 lists again,
  another thread's delta does not.
- `TestATurnStartedElsewhereIsSeen_REQ_ORC_003`: a delta, or a listing showing it running, makes
  the selected thread running, so ctrl+c cancels it.
- `TestAStaleListingIsDropped_REQ_ORC_003`, `TestUnlistedThreadsAreForgotten_REQ_ORC_003`,
  `TestNoSwitchWhileTheThreadIsCreated_REQ_ORC_003`: decisions 7, 6 and 4.
- `TestToEventMapsTheThreadListNotifications_REQ_ORC_003` and
  `TestTheThreadListReadsTheSnapshotsNames_REQ_ORC_003`: the adapter's events and the snapshot's
  wire names; `TestTheThreadListAgainstARealDaemon_REQ_ORC_003`: `thread.create`'s `workspace`
  and the listing against a real daemon.
- Sixteen mutations each reddened one of these: listing every workspace, dropping other threads'
  events, creating without the workspace, not relisting on an attention change, a fresh view not
  taken as running, no relist on a turn's end, none on a queued thread's first event, no
  `workspace.closed`, switching while creating, a delta not marking running, a listing not
  marking running, no `seq` check, no pruning, two wrong wire names, and `workspace_id` sent for
  `workspace`.
- The spec-guardian review (FIX FIRST) found the panel blind to a turn it did not start, the
  adapter's wire mapping untested, four triggers untested, listings applied out of order and
  views never released; each is fixed above, with the test that bit its mutation.
