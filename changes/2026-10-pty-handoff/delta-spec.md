# Delta — live PTY handoff between daemon versions

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-31` by the implementing agent under the Tech Lead's standing F2 instruction (F2b … F2f) |
| **Date** | 2026-10-31 |
| **Task** | T-F2-40 (split into T-F2-40 and T-F2-42, decision 1) |
| **REQ** | REQ-HND-001, REQ-HND-002, NFR-F2-05 (no change to their text) |
| **Folded into** | API 1.57 §6, Tech 1.82 DD-014, `specs/tasks/umbral-f2-tasks.md` (T-F2-40 done, T-F2-42 added) |

## Evidence

- DD-014 (Tech §3) leaves a daemon update "lost in the MVP" and names live handoff as F2.
- `internal/sessions` holds every live session in memory, in `liveSession`: the PTY, the emulator,
  the scanner's carried bytes, the recorder's open block and its plain text, the chunk being
  stored, the sequence number, the pending text of a restored pane and the agent's bookkeeping.
  A handoff has to carry each of these or prove it empty.
- The drain goroutine blocks in `Read` on the PTY master. On 2026-10-31, on Linux with
  creack/pty 1.1.24, a deadline set on the file `creack.Start` returns did not wake a read
  in progress: the read returned 1 s later with the child's output. A duplicate of the
  descriptor set non-blocking and wrapped with `os.NewFile` joins Go's poller, and then a
  deadline does wake the read (`i/o timeout`), and the next read gets the output that
  arrived meanwhile. Without that, an old daemon cannot stop reading a PTY whose descriptor
  the new daemon also holds, and every byte it read after the transfer would be lost.
- Neither client reconnects today: `internal/client`'s stream closes its notification channel
  when the daemon goes, and the TUI and the desktop stop there. `umb` connects per call.
- `config.AcquireInstanceLock` and `AcquireDatabaseLock` are `flock` locks, held by an open
  file description, so a descriptor passed over `SCM_RIGHTS` carries the lock with it.
- The API token persists in `umbral.token` (`api.LoadOrCreateToken`), so a client's token
  stays valid across daemons.
- A process's exit code is reported only to its parent. The new daemon is not the parent of
  the shells it adopts; they are reparented to init (or the nearest subreaper) once the old
  daemon exits, which reaps them.

## Decisions

1. **The task is split.** T-F2-40 is the daemon's half: the handoff itself, REQ-HND-002, and
   the reconnection proved with `umb` and a protocol client. **T-F2-42** (new, F2d, REQ-HND-001)
   makes the TUI and the desktop reconnect and resume from a snapshot. REQ-HND-001 is done when
   both are.
2. **The channel is a second socket, `handoff.sock`, beside `umbral.sock`.** Every daemon
   listens on it while it serves. It is created at mode 0600 in the 0700 runtime directory,
   and a peer whose uid differs from the daemon's is refused (`SO_PEERCRED`,
   `LOCAL_PEERCRED`). The protocol is the leaf package `internal/handoff`: length-prefixed
   JSON frames, with descriptors passed beside a frame as `SCM_RIGHTS`. It is not
   JSON-RPC and not the API: it moves descriptors, which the API never does, and only a
   daemon of the same user may speak it. A peer inside an agent's terminal, or whose pid the
   kernel did not report, is refused at `hello`. This is the same check the API makes for
   REQ-ORC-004, and here it is stricter: a handoff would give such a process every session's
   master, which is more than the methods the API refuses it.
3. **The steps, in order, each with its name.** A failure names the step in the new daemon's
   last log line, and the new daemon exits with 75 (`EX_TEMPFAIL`):
   1. `connect`: the new daemon dials `handoff.sock`;
   2. `hello`: both daemons send their version and `protocol_version`. The handoff stops unless
      the majors are equal;
   3. `quiesce`: the old daemon refuses (`busy`) while a turn runs, is queued, or a session has
      an agent command it is waiting on. It checks before it pauses anything, so a refusal
      leaves every client connected, and checks again once accepting has stopped, for a turn
      started meanwhile. Otherwise it stops accepting connections, closes every
      client connection, pauses every drain between two reads, and stores the chunk being
      written;
   4. `transfer`: the old daemon sends the listening socket, the instance lock, the database
      lock, and then each live session: its PTY master and pid, its row, its screen as a
      snapshot, its sequence number and its block-recording state;
   5. `adopt`: the new daemon builds every session: an emulator fed the snapshot, which
      reproduces screen, scrollback and modes (`ghostty.Snapshot` is a fixed point), and a
      scanner and recorder in the state they were in. None of them reads its PTY yet;
   6. `start`: the new daemon opens the database and builds every module, with the
      differences in decision 5;
   7. `confirm`: the new daemon sends `ready`. The old daemon closes its copies of the
      descriptors without signalling any child, answers `released`, and exits 0. It exits even
      when that answer cannot be sent. The new daemon therefore commits on `released` or on the
      connection closing without one. It aborts only on an `error` frame, a timeout, or a
      failed send of `ready`, because in each of those cases the old daemon still holds its
      sessions. Only after committing does the new daemon start the drains and accept on the
      inherited socket.
4. **The old daemon resumes on any failure before `ready`.** That covers an error frame, a
   closed connection, or 30 s without the next frame. It resumes the drains, accepts again,
   and logs the step that failed. Its sessions were never touched: the PTYs, the emulators and
   the open blocks are as they were, and the bytes written meanwhile wait in the kernel. If the
   new daemon fails after its migrations ran, the old daemon keeps serving on the newer
   schema. Migrations are forward-only and additive (Art. 6), so the old daemon's queries still
   hold.
5. **What the new daemon does not do on a handoff.** Each of these would be wrong for sessions
   that are alive:
   - no `store.Recover`: the old daemon ran it at its own start, and every row it would
     repair is either an adopted session or was settled by the quiesce;
   - no bootstrap sweep: the directories belong to the live shells. An adopted session does
     not remove its directory when it exits, because the closure that would is the old
     daemon's. Those directories are left to the sweep of the next start that is not a
     takeover, which runs after every shell they served is gone;
   - no workspace restore. The panes keep their sessions, and only the focus is read back;
   - no new token.
   The new daemon's own modules start as on any start: providers, MCP servers and rules. The
   `pane_history` purge also runs (Data Model §2.4d), since it deletes rows only and its
   promise is "at the next start". The old daemon's own background jobs run until it exits,
   so its MCP supervisors may write a server's stored state after the new daemon has. The
   live state is in memory and is not affected.
6. **An adopted session's exit code is unknown.** The new daemon learns of the shell's end
   through a `pidfd` on Linux and through `kqueue` `NOTE_EXIT` on macOS, and the PTY's end of
   output. It records `exit_code` NULL and announces `session.exited` with `exit_code: null`.
   API §6 already shows a session's `exit_code` as nullable. Blocks keep their exit codes,
   because those come from OSC 133. A PTY on which deadlines do not work, such as a platform
   where the poller refuses it, fails `quiesce` with `unsupported`.
7. **No PTY output is lost (NFR-F2-05).** The old daemon reads nothing after the drains pause,
   and the new one reads from the first byte after them. Its sequence numbers continue the old
   ones, so a client that subscribes with a snapshot sees a gapless stream. The downtime clients
   see runs from the quiesce to the first accept on the inherited socket, and is measured by
   `TestHandoffDowntime_NFR_F2_05`.
8. **A test-only failure injection.** `main.handoffFailEnv`, empty in every release build, is
   set only by the test build with `-ldflags -X`, and names the environment variable the new
   daemon reads its injected step from. A release build reads no such variable. The injection
   makes the new daemon fail at the named step, so each step's REQ-HND-002 path is exercised
   against a real old daemon.
9. **A descriptor is sent as a duplicate.** `os.File.Fd` makes a file blocking, and blocking is
   a property of the open file description that every duplicate shares. A listener or PTY
   master sent through `Fd` therefore stops honouring deadlines in the old daemon. After a
   failed handoff, that daemon could then never pause its listener for the next attempt. The
   sender takes a `F_DUPFD_CLOEXEC` duplicate through `SyscallConn().Control`, sends it, and
   closes it.

## Tests

- `internal/handoff`: frames and descriptors round-trip, a closed peer is `io.EOF`, a stalled
  one times out, an oversized frame is refused, and a sent descriptor keeps its deadlines
  (decision 9). The peer-uid check needs a second user and is not tested.
- `internal/handoff`: an accepted peer has its pid from the kernel.
- `internal/api`: a suspended server hands its listener over, and a resumed one serves again.
- `internal/config`: a handed lock stays held.
- `internal/sessions/integration`: an adopted session continues its block, a thawed one loses
  nothing, and a freeze is refused while the agent waits on a command.
- `internal/sessions/domain`: recorder and plain-text state survive an export and import
  mid-block.
- `internal/sessions/adapters/pty`: a paused read wakes and resumes losing nothing; an adopted
  PTY reports its child's exit with an unknown code.
- `cmd/umbrald`:
  - `TestHandoffKeepsLivePanes_REQ_HND_001`: a real daemon with a running pane is taken over.
    The pid, the screen and the open block survive, the counter the pane prints has no gap,
    and the old daemon exits 0;
  - `TestHandoffFailureKeepsTheOldDaemon_REQ_HND_002`: one injected failure per step. The new
    daemon exits 75 naming the step, and the old one keeps serving the same session;
  - `TestHandoffRefusedWhileATurnRuns_REQ_HND_002`: the client connected during the refusal
    stays connected;
  - `TestAHandoffFromAnAgentsTerminalIsRefused_REQ_HND_002`;
  - `TestATakeoverForgetsScreensTheSettingRefuses_REQ_TERM_010`;
  - `TestHandoffDowntime_NFR_F2_05`.
