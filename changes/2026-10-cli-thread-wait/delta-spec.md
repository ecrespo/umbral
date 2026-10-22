# Delta — let a script drive a thread through `umb`

| Field | Value |
|---|---|
| **Status** | `PROPOSED 2026-10-22 — awaits the Tech Lead's ratification` |
| **Date** | 2026-10-22 |
| **Task** | T-F1-38 |
| **Raised by** | F1's closing measurement of 2026-10-22: Done criterion 4 could only be met by a script that connects as `tui` |

## Evidence

**F1 criterion 4 is not reachable through `umb`.** Plan §F1: "A shell script drives a full
thread with `thread.send --wait` and `thread.wait` without polling." API §2's `cli` row gives
`umb` `thread.create`, `thread.send` and `thread.cancel`, and §5.29 says of `thread.wait`
"Interactive clients only (§2)". `umb` identifies itself as `cli`, so it can send with a wait but
cannot wait on a turn it did not start in the same call, nor read what the thread answered:
`thread.get` is interactive-only too (§5.23). And `umb` has no command for any of it — `umb ai`
is the only thread command, and it makes an ephemeral `ask` thread.

**Measured, not read.** On 2026-10-22, against an isolated daemon built from the tree,
`e2e/f1close/criteria.sh` met criterion 4 only because its client, `rpc.py`, says
`client_kind: "tui"` (a local measuring script, `e2e/f1close/rpc.py`, which this task replaces
with `umb thread` in `e2e/f1close/criteria.sh`):

```
send+wait: turn trn_01M5HP2XVJKGTK56QXANKH6G9P final_state=done in 6.1s
send, then wait: turn trn_01M5HP33S971K9CZJ50JWJ3J9X state=done pinned=True waited_ms=3475 in 3.5s
```

The same `thread.wait` from a `cli` connection answers `METHOD_NOT_FOUND`
(`TestThreadWaitIsInteractive`, which pinned it on purpose). A script that has to pretend to be
the TUI is not what US-008 ("a script (or another agent) … launch a thread and wait until it
finishes or asks something") describes.

## Decisions

**1. `cli` gains `thread.wait` and `thread.get`, nothing else.** `thread.wait` is the criterion
itself. `thread.get` is how a script reads what the turn answered; without it a script can wait
for `done` and never learn the result. Both only read: they change no thread, no mode and no
attention state — `thread.get` is not viewing (§5.23, "reading is not viewing"), so a `cli` read
does not move `done` to `idle`. `thread.list`, `thread.update` and `thread.focus` stay
interactive: `umb` must not switch a thread to `auto-edit`, and it views nothing.
`approval.*` stays interactive too: a script sees `blocked` and hands over to a person; it does
not answer approvals.

**2. As in delta `2026-09-cli-allowlist`, this is a role contract, not a security boundary.**
`client_kind` is self-declared; the boundary is REQ-SEC-003's token. `cli` could already read
every block, the agent's included.

**3. `umb thread` mirrors the methods, one call per command** (new REQ-CLI-009):
- `umb thread create [CWD] [--mode M] [--title T] [--model M]` prints the thread's id. `CWD` is
  made absolute, as for `umb workspace create`; the other flags are sent only when given.
- `umb thread send THREAD TEXT… [--wait STATES] [--timeout D]` joins the text with spaces and
  prints the turn id, or `<turn id> <final_state>` with `--wait`.
- `umb thread wait THREAD --until STATES [--timeout D]` prints the state the wait answered —
  which may be outside `--until`, as §5.29 allows; the script compares. `--until` is required and
  checked before connecting.
- `umb thread get THREAD` follows `next_cursor` through pages of 1000, so a thread longer than one
  page still shows the turn's answer, and prints the thread's id and attention state, then
  `role: content` per message, oldest first, with control runes escaped as `umb skill show` does
  and a message's extra lines indented, so model text cannot pass for another message. `--json`
  holds every page's messages and is what a script parses.
- `umb thread cancel THREAD` prints `cancelled THREAD`.
- `STATES` is comma-separated (`done,blocked`). `--timeout` defaults to 10 minutes and the
  daemon validates its 1 s–1 h range (§8); the call itself is given that plus the usual 10 s.
- `--json` prints the method's result. A `TIMEOUT` is exit 1 naming `data.last_state`
  (REQ-AUT-004); `THREAD_BLOCKED` and every other error are exit 1 too (REQ-CLI-004).

## Specification changes

- **PRD** 1.21: REQ-CLI-009 — WHEN `umb thread create`, `send`, `wait`, `get` or `cancel` runs,
  THE SYSTEM SHALL invoke the `thread.*` method of the same name and print the value the next step
  of a script needs (the thread id, the turn id and, with `--wait`, its final state, the state a
  wait answered, the messages), in JSON with `--json`, with REQ-CLI-004's exit codes; a wait that
  reaches its timeout SHALL exit 1 naming the state it last observed.
- **API** 1.41: §2's `cli` row adds `thread.wait` and `thread.get`; §5.23 and §5.29 replace
  "Interactive clients only" with what they now say. Additive within `protocol_version = 1`.
- **Tech** 1.54 §9.4: `umb thread` in the command list, with the grammar of decision 3.
- Positionals and flags interleave, as in every `umb` command; a word of `send`'s text that
  starts with `-` goes after `--`.
- **Tasks:** T-F1-38 in `specs/tasks/umbral-f1-tasks.md`; REQ-CLI-009 in its matrix, and T-F1-38
  beside T-F1-23 for REQ-AUT-001 and REQ-AUT-004.
- **Plan:** a T-F1-38 row in the F1 table, and criterion 4's record corrected, are written with
  F1's closing paperwork (branch `chore/f1-close`), which already rewrites that table.

## Not modified

No method, parameter, result or error code changes shape. No notification is sent to `cli`.
