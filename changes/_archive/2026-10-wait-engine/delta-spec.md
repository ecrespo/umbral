# Delta — what a wait observes, what it pins, how it settles, and how it answers

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-10-03 — approved by E. Crespo as written, with the proposed options of decisions 3, 3a (answer `unknown`), 7 and 8. Folded into API 1.27, Tech Design 1.34 and the F1 tasks file` |
| **Date** | 2026-10-03 |
| **Task** | T-F1-23 |
| **Raised by** | T-F1-23: API §5.29 and §5.30 had a paragraph each; the decisions below are the ones the code had to take and no REQ states |

## Evidence

- **API §5.29** said only that the turn in progress is pinned, that a thread already in a
  target returns at once, and that expiry is `TIMEOUT` with `data.last_state`. It did not say what
  is observed (§4 says `attention_state` is "what waits observe", but `until` also takes
  `stopped`, which is a thread state), what is pinned when no turn runs, or what a wait does when
  its turn ends outside every target — the question delta `2026-10-thread-cancel` left open in its
  decision 7.
- **API §5.30** did not say what "recent output" is, how lines are numbered, whether a prompt
  without a newline can match, what `block_id` answers for a session wait, or what a `block_id`
  wait reads.
- **API §1 and the daemon** answered a connection's requests one at a time. A wait of up to an
  hour would have held its connection: the `approval.respond` that unblocks it could not be read,
  and a client that hung up would have left the wait running until its deadline.
- **T-F1-23's file list** names `internal/agents/wait/**` and `cmd/umb/wait.go`. Tech §4's tree
  and §5.2's rules name a top-level `internal/waits` module, and no REQ names an `umb` wait
  command.

## Decisions

1. **What a wait observes.** The thread's `attention_state` (`idle`, `working`, `blocked`,
   `done`), or `stopped` for a stopped thread. `until` holds one or more of `idle`, `done`,
   `blocked` and `stopped`. `working` is observed and reported — in `data.last_state` — but is not
   a target. An empty or unknown `until` is `VALIDATION_ERROR`.
2. **What is pinned.** `thread.wait` pins the thread's turn in progress or, when none runs, its
   latest turn: the turn of its newest user message, read in the same statement as its state. A
   thread that never had a turn pins none, and `turn_id` is `null`. `thread.send` with `wait` pins
   the turn the send started, or the one a repeated `client_msg_id` names.
3. **How an ended turn settles a wait (closes `2026-10-thread-cancel` decision 7).** Once the
   pinned turn has ended, the only change left without a new turn is a client viewing it, `done`
   to `idle`. When that cannot reach a target, the wait answers at once with the state the turn
   ended in, instead of running to its deadline:
   - a turn cancelled into `stopped` answers `stopped`, although `stopped` is not a target;
   - a turn that ended `done` answers `done` unless `idle` is a target, in which case it keeps
     waiting for a client to view it (T-F1-24).

   It is a result, not an error, and `final_state` follows the same rule. *Alternative:* run to the deadline and answer
   `TIMEOUT`. That makes a script wait out its whole timeout for an answer the daemon already has.
3a. **A replaced pinned turn answers with its own end, or `unknown`.**
   - When the daemon reads a later turn before it has seen the pinned turn end, that later turn's
     state never answers. This happens when the end event and the next turn race, when the bus
     drops the event, or with a `thread.send` whose repeated `client_msg_id` names an old turn.
   - The wait takes the pinned turn's end from its own end event, or from the runtime's memory of
     the last 4096 turns that ended during this daemon run.
   - When neither tells within two backstop ticks, the answer is `unknown`. A deadline reached
     first reports `unknown` as `data.last_state`. A turn whose end could not be written
     (`storage_error`) is remembered as `unknown`, since the store still says it is running. That covers a turn
     that ended before the daemon started, since no table stores a turn's end. `unknown` is
     already an `attention_state` value (§4).
   - *Alternative:* persist each turn's end in a migration `0006`, which this delta does not
     propose for a case only a replayed `client_msg_id` reaches.
   - Once a later turn has replaced it, the pinned turn can no longer be viewed into `idle`, so its
     end settles the wait whatever the targets. A thread that never had a turn answers at once
     with its state, whatever `until` says.
   - Nothing moves `done` to `idle` until T-F1-24, so until then a wait for `idle` alone after a
     turn ends runs to its deadline.
4. **`blocked` is never missed.** The pinned turn's `approval.requested` reports `blocked` even
   when the approval is answered before the daemon reads the thread again.
5. **Waits answer out of order** (API §1). The daemon does, in order, everything a request must
   do before the next one is read: validating, subscribing, pinning and, for `thread.send`,
   sending. It then answers the wait when it settles and keeps serving the connection meanwhile.
   Responses are matched by `id`. Closing the connection, or the daemon stopping, ends its waits
   and writes nothing for them. A wait sent as a JSON-RPC notification, which nobody can be
   answered about, ends at once. A `thread.send` sent that way has still sent its message. `client.Stream` still makes one call at a time, so a Go client that must act while it
   waits uses a second connection. The tests do.
6. **`thread.send` with `wait`.**
   - The wait is validated before anything is sent: an invalid wait persists nothing.
   - `THREAD_BLOCKED` is checked under the send's per-thread lock and **before** a repeated
     `client_msg_id` is looked up, so a retry against a paused thread is refused too. REQ-AUT-002
     says "without persisting the message", and answering the earlier turn instead would start the
     wait it forbids.
   - On a running thread that is not blocked, a send with `wait` is `CONFLICT`, as without one.
     An approval requested between the check and the turn's start gives `CONFLICT` rather than
     `THREAD_BLOCKED`. Nothing is persisted either way.
7. **`data.last_state` on `TIMEOUT`.**
   - For a thread wait it is the pinned turn's last observed state (`idle`, `working`, `blocked`,
     `done` or `stopped`).
   - For `block.wait_output` it is the last line evaluated, `""` when none was. A wait on output
     has no state other than what it last read. *Alternative:* the observed block's state, which
     tells a script less.
   - The field is set only on `TIMEOUT`, and never carries a trace id.
8. **`block.wait_output`'s window.**
   - **Start.** With `session_id`, the window is the last `lines` lines of the pane's screen as
     plain text. That is the screen as rendered, without the blank lines below the last one
     written, read together with the output sequence number it is current as of.
   - **Follow.** Then comes the output after that number, read through the stripper blocks use for
     `output_plain`. A gap in the numbers — the bus dropped output — re-reads the screen rather
     than miss a line.
   - **Numbering.** Line numbers count from the window's first line, from 1.
   - **The line still being written** (a prompt) is evaluated as well and can match, so
     `matched_line` may be the start of a longer line. *Alternative:* complete lines only. That
     cannot wait for `password:`.
   - **A line half on screen.** When the cursor is past the start of the last row written,
     that row is still open. The emulator reads it untrimmed up to the cursor, so a prompt's
     trailing space is kept: `Password: ` matches `Password: $` and `[y/N] ` followed by `y`
     reads `[y/N] y`. The output that follows continues it instead of starting a new line. A
     cursor elsewhere, mid-screen in a full-screen program, opens no line.
   - **A gap.** When the bus dropped output, the screen is read again and numbering restarts
     from that new window.
   - **Long output.** The follower hands committed lines out of the stripper as it goes
     (`PlainText.TakeCommitted`), so a wait reads output of any length. Earlier it went blind at
     the stripper's 1 MiB cap.
   - **`lines`.** It defaults to 200 only when absent; an explicit `0` is `VALIDATION_ERROR`.
   - **Live sessions only.** Only a live session can be waited on. A session of an earlier daemon
     run is `NOT_FOUND`.
   - **`block_id` naming a closed block.** Its window is the last `lines` lines of its stored
     output, and nothing follows.
   - **`block_id` naming a running block.** It reads its pane, as `session_id` does, and follows
     while that block is open.
   - **`block_id` in the result.** The block named; else the block running when the line was read;
     else the session's most recent block. `null` when the session has none.
   - **No early answer.** A closed block, an exited session or an ended block with no match runs to
     the deadline.
9. **Capability and clients.**
   - `thread.wait` and `block.wait_output` are advertised under §2's `waits`, which their prefixes
     would not give them. A method can now name its namespace.
   - `thread.wait` stays interactive-only, since §2's `cli` row does not list it.
   - `block.wait_output` is `block.*`, open to every kind.
10. **Where the code lives (Tech §4, §5.2).**
    - The module is `internal/waits` — `domain`, `ports`, the service — as Tech §4's tree and §5.2's
      row say, not `internal/agents/wait/**` as the task's file list did. `.go-arch-lint.yml` gains
      `waits-domain`, `waits-ports` and `waits-service`. The service uses the ports and domains of
      `agents` and `sessions`, plus `bus`, and never `api`.
    - The domain reuses the sessions domain's escape-sequence stripper. The service uses the
      ports **and the domains** of `agents` and `sessions`. Tech §5.2's row says "ports"; the
      fold makes it "ports and their domains".
    - `cmd/umbrald` implements the module's ports over the runtime and the terminal.
11. **The bus is lossy (Tech §3); waits are not.** A thread wait also re-reads the thread from the
    store once a second, as a backstop for a dropped event. This is not the client polling DD-011
    forbids: the client makes one call and gets one answer.
12. **Out of T-F1-23.**
    - `cmd/umb/wait.go` is dropped: no REQ-CLI names a wait command, and §2 does not give `cli`
      `thread.wait`. `umb ai` (T-F1-19) is the CLI's consumer of `thread.send`'s `wait`.
    - The 32-wait limit per connection (REQ-AUT-005), `wait.list` and `wait.cancel` remain
      T-F1-31's. Until then a connection's waits are bounded only by their deadlines.

## What changes in the specs on ratification

- **API 1.26** — already written by T-F1-23, including decision 3a:
  - §1, decision 5;
  - §3's `TIMEOUT`, decision 7;
  - §5.20, decision 6;
  - §5.29, decisions 1–4;
  - §5.30, decision 8.
- **API §2**, the capability list's text: `waits` is advertised when `thread.wait` or
  `block.wait_output` is served (decision 9).
- **Tech §3.x (DD-011):**
  - the backstop of decision 11;
  - the out-of-order answer of decision 5;
  - §4's tree and §5.2's row as decision 10 (`waits-domain` may use the sessions domain).
- **`specs/tasks/umbral-f1-tasks.md`:**
  - T-F1-23's files become `internal/waits/**`, `internal/api/waits.go` and
    `cmd/umbrald/waits.go`;
  - `cmd/umb/wait.go` moves to T-F1-19 as a note.
