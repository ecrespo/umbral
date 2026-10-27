# Delta — The Wails desktop client over the published protocol

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-26` by the implementing agent under the Tech Lead's standing F2 instruction (extended to F2b … F2f on 2026-10-26), pending the Tech Lead's review |
| **Date** | 2026-10-26 |
| **Task** | T-F2-30 |
| **REQ** | REQ-DSK-001 |

## What the PRD and the task left open

REQ-DSK-001 asks for a Wails v3 client that is a thin client of `umbrald` — workspaces, tabs,
split panes rendered from the daemon's screen state, blocks, threads, approvals and worktrees —
using only the published protocol. Nothing says how Wails' GTK and WebKit dependencies reach the
build, what the window may call, how a terminal is drawn in a webview, or how the client is
tested. The Tech Lead decided on 2026-10-26 that the desktop is built only in CI or a container.

## Decisions

1. **Two halves.** The client's core is `internal/desktop` in the main module: a bridge over a
   `desktop` connection, its ports, and `internal/desktop/adapters/vtframe`, the frame renderer.
   It knows no Wails and is tested by the daemon's suite against a real daemon. The window is
   `cmd/umbral-desktop`, a Go module of its own on Wails **v3.0.0** (GTK 4 and WebKitGTK 6.0 on
   Linux), which binds the bridge to a page. It reaches the main module through a `replace` of
   the repository it lives in, so Wails and its cgo dependencies never enter the daemon's
   `go.sum`, `go test ./...`, lint, `govulncheck` or `go-arch-lint` (which excludes the
   directory). Its boundary is `scripts/desktop_imports.sh`, which `task arch` runs: of
   Umbral's packages the window imports `internal/client`, `internal/desktop` and
   `internal/desktop/adapters/vtframe` only, in every package and test of the module, and a
   module it cannot list fails it; `--selftest` (in `task arch:selftest`) proves a forbidden
   import and an unreadable module are refused. `task desktop` builds it in `packaging/desktop/Dockerfile`
   (Ubuntu 26.04 with the GTK and WebKit headers) with the host's Go toolchain and
   libghostty-vt mounted; CI's `desktop` job vets, tests, lints (golangci-lint with the
   repository's config; gofumpt already covers the directory in the `lint` job) and
   vuln-checks the module, builds it and runs the smoke.
2. **What the window calls.** API §2 lets a `desktop` connection call every method; the window
   limits itself to `desktop.Methods`, the methods its views of REQ-DSK-001 use:
   `system.status`, `session.snapshot`, `workspace.create|focus`, `tab.create|focus`,
   `pane.split|focus|close`, `block.list|get`, `thread.create|send|cancel|get|focus`,
   `approval.list|respond` and `worktree.list|diff|merge|discard`, plus
   `session.subscribe|unsubscribe|input|resize` through the terminal calls. A method enters
   the list when a view uses it. Configuration,
   keys, rules, skills, MCP servers, limits and `layout.apply` stay with `umb`. A method
   outside the list is refused in the client and never sent. Every listed method is checked
   against the daemon's own `api.schema` (served to `desktop`) by the test.
3. **How a terminal is drawn.** The daemon sends bytes (DD-001); the client runs the same
   libghostty the daemon and the TUI do and sends the page **frames**: the visible rows, each a
   list of runs of one style (colours as `#rrggbb`, bold, faint, italic, underline,
   strikethrough, inverse applied), the cursor and the default colours. The page builds the DOM
   from them with `textContent`, so nothing a program prints can become markup; the page's CSP
   allows only its own scripts and styles. A spacer cell after a wide character is skipped. A
   screen with new output is drawn at most every 16 ms.
4. **Attaching.** Attaching resizes the session to the pane, subscribes, replays the snapshot
   into a new, empty screen and applies output whose `seq` is above the snapshot's (API §5.11);
   the page runs a session's attach and its resizes one after another, so a resize never
   reaches the daemon before the screen it belongs to exists; output for a session still being
   attached waits for its screen: the bridge's lock is held
   across `session.subscribe`, which keeps the notification pump waiting for one call at most.
   `session.unsubscribed` makes the page attach again, which brings a fresh snapshot. The
   window reads its connection with an unbounded stream, as `umbral-tui` does: a page behind
   on frames never stops the pump, and the daemon's own per-connection limit (API §8, REQ-API-006)
   is what bounds a stalled window — it is unsubscribed and attaches again.
5. **The tree is the daemon's.** The page keeps no cache: it reads `session.snapshot` again on
   every `workspace.*`, `tab.*`, `pane.*` and `layout.*` notification — one asked while a read
   runs is read once more after it, so the last change is drawn — and draws the focused tab's
   layout as nested splits by `ratio`. A pane's head shows its label, cwd, detected agent's
   `name` and attention state.
6. **Keys.** Printable keys are sent as typed, alt as an ESC prefix, ctrl+letter as its control
   code, and Enter, Backspace, Tab, Escape, the arrows, Home, End, Insert, Delete, PageUp and
   PageDown as their xterm sequences; ctrl+shift+C and ctrl+shift+V are the webview's copy and
   paste, and a paste is sent as text. Meta (super) is the window manager's. No link the page
   draws and no file dropped on it navigates the webview.
7. **Agents.** The threads view is REQ-ORC-003's desktop clause: `Bridge.Threads` reads one
   `session.snapshot` and the pending approvals and lists every thread whose `workspace_id` is
   the focused workspace — none of another and none of no workspace — with its attention
   state, `queued`, its worktree (or "no worktree") and its pending approvals counted; the list
   is read again on `thread.attention_changed` and `thread.turn_finished` (a new thread is
   announced by its first attention change; API §6 has no `thread.created`). The view creates
   a thread in the workspace, sends with a fresh ULID `client_msg_id`, cancels, reads every page
   of the transcript (`next_cursor`), and appends `thread.delta` text as it arrives, a new
   message after each tool call; the user's own message, and text streamed before a read, are
   read back from the transcript, never kept by the page. It focuses the thread
   it shows and unfocuses it when it goes back to the list or leaves the view, so a turn the
   user is not looking at ends `done`. A hosted agent's thread says its traffic is not recorded
   (REQ-ACP-007). Approvals are answered approve once, approve for the thread or deny, with the
   diff shown (per hunk is T-F2-31); the pending count is read again on `approval.requested`,
   `thread.tool_call` and `thread.attention_changed` (API §6 has no `approval.resolved`). A
   worktree's diff is shown per file and hunk; a merge needs that diff read first, asks, and
   passes the revision it was read as; a discard asks and sends `confirm: true`. The blocks view
   lists the focused pane's blocks and opens one's plain output.
8. **Connection.** The window connects as `desktop` with autostart, like `umbral-tui`, and shows
   a dropped connection as disconnected. A window that sent the last request is the foreground
   client of API §5.35 and receives `notification.posted`; it shows it in its status line, as
   text, until the next one, so a notification the daemon reports `shown` is shown.
9. **Residuals.** No reconnect after a drop yet; the window must be restarted. The page's
   own logic (keys, refresh, events) has no unit test: what can be is in `internal/desktop`
   and tested there, and the smoke drives the page against a real daemon. The app id
   `io.github.ecrespo.Umbral` is set here and tested by T-PKG-04; per-hunk approval and the
   worktree's file tree are T-F2-31; only Linux is built in CI (Windows is F2e, macOS T-PKG-05).
   WebKit's web process sandbox needs unprivileged user namespaces: the CI job allows them on
   the runner, and a container that forbids them cannot run the window.

## Tests

- `TestOutputTheSnapshotHeldIsNotAppliedAgain_REQ_DSK_001`,
  `TestATerminalSizeIsOneTheDaemonTakes_REQ_DSK_001` and
  `TestTheThreadsViewIsTheWorkspacesThreads_REQ_ORC_003` (`internal/desktop`): output at or
  below the snapshot's seq dropped; sizes the daemon refuses refused unsent, none wrapping
  uint16; the workspace's threads only, with attention, `queued`, worktree and pending count.
- `TestTheDesktopThreadsViewAgainstARealDaemon_REQ_ORC_003` (`cmd/umbrald`): over a `desktop`
  connection, the thread created in the workspace is listed and the one created in none is not.
- `TestAFrameIsTheVisibleScreenAsText_REQ_DSK_001` (`internal/desktop/adapters/vtframe`): the
  visible rows only, runs per style, markup kept as text, a wide character once, inverse
  swapped, the cursor, reset and resize.
- `TestTheDesktopBridgeDrivesADaemon_REQ_DSK_001` (`cmd/umbrald`): a real daemon, a `desktop`
  connection, a workspace created, its pane attached, `echo desk-$((40+2))` typed and `desk-42`
  drawn in a 90×20 frame; tree notifications passed on; threads, approvals, worktrees, blocks
  and the snapshot listed; a resize; `rules.reset` refused unsent; every listed method served to
  `desktop` by `api.schema`; closing tells the page. It reads the connection with the
  unbounded stream the window uses.
- `scripts/desktop_smoke.sh` (CI's `desktop` job, `task desktop:smoke`): the window under xvfb
  against a real daemon; the page types `echo smoke-$((<marker>+1))` into its first pane and
  quits once its own DOM shows the sum, which only the shell's output has — the echo of the
  typed line does not.

## Folded into

- Tech 1.78: §5.2 rows for `desktop`, `desktop/ports` and `desktop/adapters`; §5.3h the desktop
  client.
- `specs/tasks/umbral-f2-tasks.md`: T-F2-30 done, its traceability rows (REQ-DSK-001 and
  REQ-ORC-003's desktop clause).

## Review

spec-guardian's pass (FIX FIRST) found: the window's module not linted (now golangci-lint in
CI's `desktop` job); REQ-ORC-003's desktop clause missing (`Bridge.Threads`, decision 7); the
page listening for `thread.created` and `approval.resolved`, which API §6 does not have;
`detected_agent` drawn as an object; one page of the transcript only; a merge without the diff,
its revision or a confirmation; the seq drop untested, and the test on a bounded stream where
the window has an unbounded one; the module's imports unchecked (`scripts/desktop_imports.sh`);
the unbounded stream and the thread focus left set unrecorded (decisions 4 and 7); and the LOW
items: a refresh lost while one ran, the user's message shown twice, ctrl+shift+V unreachable,
the smoke marker not checked as digits, the size comment's range, `desktop:smoke` not building
the window, the lock across subscribe (decision 4), unused methods in the list (decision 2) and
navigation (decision 6). All are fixed or recorded here.

The second pass (FIX FIRST) found: `notification.posted` taken by the window as foreground client
and never shown (decision 8); the import gate passing a module it could not list, and checking
the main package only (decision 1); the smoke accepting the typed line's echo (now the shell's
sum); a resize racing the attach (decision 4); assistant text drawn twice after a tool call or
on reopening a thread mid-turn (decision 7); and `worktree_id` and `queued` read by no test
(the unit test now decodes the wire's JSON). All are fixed.
