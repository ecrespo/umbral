# Delta — the agent panel's keys, its thread, and what it sends

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-10-03` (amended after review, ratified again the same day) |
| **Date** | 2026-10-03 |
| **Task** | T-F1-20 |
| **Raised by** | T-F1-20: REQ-TUI-001…003 name the panel, the mode shortcut and "attach to agent", and leave the rest to the client |

## Evidence

- **REQ-TUI-002** fixes `ctrl+space` and nothing else. **REQ-TUI-003** says "picks 'attach to
  agent' on a block" without a key.
- **T-F1-20** names `[a]pprove`, `[d]eny` and `a[l]ways` and a diff view, but not when those
  letters answer rather than type. It names `internal/tui/agent/**` and "teatest tests".
- **API §5.20** takes attachments as `[{kind, ref}]`. The daemon does not read `@…` tokens out of
  the text, so a client that offers `@block:<id>` in its input (REQ-TUI-003, REQ-CTX-002) has to
  turn it into one.
- No spec says which thread the panel talks to, or where that thread works.

## Decisions

> **Amended 2026-10-03, after the spec-guardian review.** The first version was ratified the
> same day. The review found that it let typing answer an approval, left expired approvals on
> screen, hid other threads' approvals, and started the thread in `$HOME`. Decisions 1–4 below
> are the amended text. The changes are marked **(amended)** and need ratifying again.

1. **Keys.**
   - ctrl+space toggles input between the focused shell and the panel. Entering agent mode opens
     the panel; leaving it keeps the panel open, so a running turn stays visible. **(amended)**
     The key is fixed in F1: no setting changes it.
   - **(amended)** **alt+a** is "attach to agent" on the block selected in the block list, and
     acts only while the list is open. Otherwise it goes to the shell as ESC a, which readline
     and zsh bind.
   - **esc** in agent mode clears the input, or closes the panel when the input is empty.
   - **ctrl+c** in agent mode stops a turn (`thread.cancel`) and never reaches the shell. It stops
     the turn whose approval is shown, else the panel's own. **(amended)** If the message is still
     waiting for `thread.create`, ctrl+c takes it back and nothing is sent. If the daemon answers
     `stopped_at: null` (the panel missed the turn's end), the panel stops showing a turn as
     running and drops that thread's approvals. With nothing to stop, ctrl+c clears the input.
     In shell mode ctrl+c is the shell's, as before.
   - **(amended)** Text pasted in agent mode goes into the input. Newlines become spaces and
     control characters are dropped.
2. **Approvals.** **(amended)**
   - Approvals are answered with control chords, never letters:
     - ctrl+y approves `once`;
     - ctrl+r approves for the `thread`;
     - ctrl+l approves `always`;
     - ctrl+x denies `once`;
     - ctrl+e shows or hides the diff.
   - Every printable key is typed, so the next letter of a word can never approve a write or
     persist an `always` rule when an approval arrives mid-word.
   - Approvals are answered oldest first. If an answer never reaches the daemon (a timeout or a
     lost connection), its approval is put back to be answered again. If the daemon answers
     `CONFLICT` or `NOT_FOUND`, the approval was already decided or has expired, and it stays
     off the panel.
   - A thread's `thread.turn_finished` removes its approvals from the panel. A cancel leaves them
     `expired` (API §5.25), and answering one would be a `CONFLICT`.
   - An approval opens the panel if it was closed. While any is pending and the panel is not in
     agent mode, the status line says the agent is waiting, however many keys the shell gets.
   - *Alternative:* a modal that steals the keyboard. That would interrupt typing in the shell.
3. **One thread per TUI run; the approvals of every thread.**
   - The first message creates the thread with `thread.create`, in the default mode.
     **(amended)** It is created in the directory of the focused pane's last command (the `cwd`
     of its latest `block.closed`). If the pane has run nothing yet, it is created where the
     pane's session started, which is `$HOME` for a TUI session.
   - A block's `cwd` is where its command *started*, so a `cd` takes effect from the next
     command: after `cd ~/repo`, the thread starts in `~/repo` only once another command has
     run there. Following the shell's live directory (its last OSC 7) would add a field to
     the protocol; this delta does not add it.
   - Every later message goes to the same thread. The transcript shows only that thread's events.
   - **(amended)** Approvals are every thread's. In F1 only the TUI can answer them (API §2).
     `approval.list` at start lists the ones left by `umb` or by an earlier run, and an
     `approval.requested` of any thread is shown. An approval of another thread names it.
   - A thread list and switching between threads are not part of T-F1-20.
4. **What is sent, and what is drawn.**
   - The text goes as typed.
   - Every `@block:<id>`, `@file:<path>` and `@directory:<path>` token in it also becomes an
     attachment of that kind, with trailing punctuation dropped. **(amended)** `@directory:` is
     REQ-CTX-002's word. `@dir:` is accepted as a short form.
   - Every `thread.send` call carries a fresh ULID `client_msg_id` (REQ-AGT-015). The panel never
     retries a send.
   - A message typed while a turn runs is refused in the client, with a status line, rather than
     sent into a `CONFLICT`.
   - **(amended)** Tool calls are one row each, matched by their id.
   - **(amended)** The model's text, an approval's summary and its diff can carry escape and
     control characters, and invisible format characters such as bidi overrides and zero-width
     spaces. All of these are dropped before drawing, and tabs become spaces.
   - **(amended)** The panel and the block list each take their width plus a one-column
     separator from the panes. A long approval keeps its head (the tool, its risk and the start
     of its target), and it and its diff are cut to the room left with `… n more lines`. The
     keys and the input line stay on screen.
   - Rows are measured in runes, not terminal cells. Wide characters (CJK, emoji) can make a
     row wider than its column. Measuring cells needs a width table this delta does not add.
5. **Where the code lives, and how it is tested.**
   - The panel is part of the model, `internal/tui/agent.go`, rather than a package under
     `internal/tui/agent/`. The panel shares the model's layout, key routing and event loop, and
     a separate package would need a component in `.go-arch-lint.yml` for no boundary anyone
     enforces.
   - The tests use the package's existing deterministic harness, not teatest, which is not a
     dependency.
   - A real-daemon test drives the TUI's own model and adapter through a turn and an approval.

## What changes in the specs on ratification

- **`specs/tasks/umbral-f1-tasks.md`, T-F1-20:**
  - its Files become `internal/tui/agent.go`, `internal/tui/ports/ports.go` and
    `internal/tui/adapters/daemon/daemon.go`;
  - its Done says "the model's harness tests".
- **Tech Design**, the TUI's section: decisions 1–4 as the panel's behaviour. Folded as §5.3e
  in Tech 1.35.
