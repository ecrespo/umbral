# Delta — the TUI and the desktop follow their daemon across a handoff

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-31` by the implementing agent under the Tech Lead's standing F2 instruction (F2b … F2f) |
| **Date** | 2026-10-31 |
| **Task** | T-F2-42 |
| **REQ** | REQ-HND-001 ("clients SHALL reconnect and resume from a snapshot"; no change to its text) |
| **Folded into** | Tech 1.83 §5.3h and DD-014, `specs/tasks/umbral-f2-tasks.md` (T-F2-42 done) |

## Evidence

- T-F2-40 (delta `2026-10-pty-handoff`) closes every client connection at `quiesce`. Before this
  task, the TUI's adapter and the desktop's bridge treated the stream's end as final: the TUI
  showed "lost the daemon" and the desktop showed "disconnected".
- During a live handoff the socket accepts again within milliseconds. A connection made in
  the meantime waits in the kernel's backlog for the new daemon (measured about 15 ms,
  `TestHandoffDowntime_NFR_F2_05`). A stalled handoff gives the old daemon 30 s before it
  resumes.
- API §8 batches `session.output` every 4 ms or 32 KiB, and a batch carries its last chunk's
  `seq`. Measured on 2026-10-31 against a daemon with no handoff at all: a subscribed client
  sees `seq` 2 … 7, then 9, so numbers are skipped by design. "No gap in `seq`" therefore
  means that `seq` only moves forward from the snapshot's value, never that it is contiguous.

## Decisions

1. **One redial, shared.** `client.Redial` dials until a daemon answers, the client closes, or
   `client.RedialWindow` (30 s, the old daemon's handoff timeout) has passed. Each attempt is
   cut at 5 s, and the wait between attempts doubles from 50 ms up to 1 s.
2. **Only the daemon closing the stream is followed.** A stream that ends with `io.EOF` while
   the client is still open is redialled. Overflow, which is this client falling behind, stays
   final, as before. Two other kinds of close are also redialled:
   - the daemon closing a client past its 8 MiB queue (REQ-API-006), as API §8 already says;
   - a daemon that crashed. That shows "reconnecting" for up to `RedialWindow` before
     "disconnected", where before it showed "disconnected" at once.
3. **No autostart on a redial.** The redial connects with `NoAutostart`, so a client never
   starts a daemon of its own in the middle of a handoff. A daemon that is gone for good
   leaves the client "disconnected" once the window passes.
4. **The TUI.** `daemon.NewReconnecting` emits `EventReconnecting`, then `EventReconnected`
   once it has a new stream. On `EventReconnected` the model:
   - empties every pane and subscribes it again (the same path as `session.unsubscribed`);
   - reads the thread list again. Its envelope `seq` restarts on the new daemon, and a listing
     asked on an earlier connection is dropped whatever its `seq` (API §6);
   - focuses the panel's thread again, since a focus belongs to the connection that sent it
     (REQ-AGT-016);
   - reads the block list and the panel's extensions again when they are open.

   Panes whose session is gone, as after a restart rather than a handoff, report their failed
   subscribe on the status line. Keys typed in the few milliseconds the connection is down go
   to the closed stream and are lost, as on any dropped connection.
5. **The desktop.** `desktop.NewReconnecting` emits `reconnecting`, then attaches every
   terminal again at its last size on the new connection. It skips a terminal the page
   detached meanwhile, and drops and closes one it cannot attach. It then emits `reconnected`
   with two lists:
   - `attached`: the sessions it attached;
   - `lost`: the sessions whose session is gone.

   The page attaches any terminal it holds that is in neither list, because that terminal was
   opened while the connection was down and its `Attach` went to the closed stream. It then
   reads its tree and its view again.
6. **The resume check.** After reconnecting, a client's snapshot is current as of a `seq` no
   lower than the last chunk it had. The output after the snapshot only moves forward, and the
   snapshot plus that output hold every line the pane printed.

## Tests

- `internal/client`: `TestRedialTriesUntilADaemonAnswers_REQ_HND_001`,
  `TestRedialGivesUpAfterItsWindow_REQ_HND_001`, `TestRedialBoundsEachAttempt` and
  `TestRedialStopsWithItsContext`.
- `internal/tui`: `TestAReconnectionReattachesEveryPane_REQ_HND_001`.
- `cmd/umbrald`, against real daemons and a real `--takeover`:
  - `TestTheTUIFollowsItsDaemonAcrossAHandoff_REQ_HND_001`;
  - `TestTheDesktopFollowsItsDaemonAcrossAHandoff_REQ_HND_001`.
