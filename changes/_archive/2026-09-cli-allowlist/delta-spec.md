# Delta — let `umb` reach the workspace tree it was given commands for

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-09-26 — commissioned and approved by E. Crespo in the same instruction ("redacta ambos deltas e impleméntalos"); drafted, applied and archived by the implementing session, so it never sat pending` |
| **Date** | 2026-09-26 |
| **Task** | T-CAL-01 (specification), implemented under T-F0-20 |
| **Raised by** | The T-F0-21 session of 2026-09-26, driving a real daemon: every `workspace.*` call from a `cli` connection answered `METHOD_NOT_FOUND` |

## Evidence

**The CLI delta specified a client the protocol forbids.** Delta
`2026-09-cli-workspace-surface` (ratified 2026-09-26) adds REQ-CLI-005 — `umb workspace`,
`tab`, `pane` and `layout` each invoke "the JSON-RPC method of the same name" — and states
under *Not modified* that "no method, parameter, result or error code changes" and "nothing
under `internal/` changes". Both are false in combination with API §2, whose allowlist gives
the `cli` client kind

> `system.*`, `api.*`, `block.*`, `thread.create`, `thread.send`, `thread.cancel`, `model.list`

and nothing that arranges windows. `umb` identifies itself as `cli` (`internal/client`,
`DialKind(…, ClientKindCLI)`), so every command REQ-CLI-005 defines would be refused.

**Measured, not read.** An isolated `umbrald` on 2026-09-26, a `system.hello` with
`client_kind: "cli"`, then `workspace.create`:

```
workspace.create: {'code': -32601, 'message': 'method_not_found: workspace.create',
                   'data': {'domain_code': 'METHOD_NOT_FOUND'}}
```

The same call as `tui` succeeded. The five T-F0-20 tests could not see it, because they drive
a fake daemon; `scripts/cli_roundtrip.sh`, against a real one, would have failed on its first
line.

**Two tests pin the refusal on purpose.** `TestWorkspaceMethodsRequireAnInteractiveClient` and
`TestLayoutMethodsRequireAnInteractiveClient` (`internal/api`) assert that `cli` gets
`METHOD_NOT_FOUND` for the tree and for layouts, citing §2. They are correct against today's
text, which is why the text has to move first.

## Decisions

**1. `cli` gains exactly the surface REQ-CLI-005 and REQ-CLI-006 use.** `workspace.*`,
`tab.*`, `layout.*`, and `pane.*` **except `pane.move`**. The allowlist is a statement of
what each client is *for*, and the CLI delta's decision 3 keeps `pane.move` out of `umb`
deliberately; granting a method no `cli` command calls would make §2 describe a client that
does not exist. When a later delta gives `umb` a `pane move`, it widens this row in the same
change.

**2. `session.*` stays interactive-only.** `umb` addresses panes, and a pane owns its session;
it never drives a PTY directly — no `session.input`, no `session.subscribe`. The daemon's own
comment on `session.*` ("`umb` reads blocks and talks to threads, it does not drive
terminals") remains true.

**3. This is a role contract, not a security boundary, and the delta says so.** `client_kind`
is self-declared in `system.hello`; anything holding the token can declare `tui`. The
boundary is REQ-SEC-003's token and the `0700` directory around it (§2), and neither
changes. What the allowlist buys is that a client written against one role cannot drift into
another by accident — which is why it is worth keeping narrow — but widening it grants no
local process anything it could not already do. `pane.split` with a `command` runs a program
as the user; so does the shell the user typed `umb` into. One consequence for F1: an agent's
Exec of `umb pane split … -- X` runs `X` in a new pane, so the command policy of `security.Decide()`
must not classify `umb` as read-only by its argv[0].

**4. No notifications for `cli`.** `umb` does not subscribe; each command is one call and an
exit. Nothing in §6 changes.

## Specification changes

- **API §2, Client kinds**, the `cli` row becomes:
  `system.*`, `api.*`, `block.*`, `workspace.*`, `tab.*`, `pane.*` except `pane.move`,
  `layout.*`, `thread.create`, `thread.send`, `thread.cancel`, `model.list`.
- **API changelog:** 1.13, additive within `protocol_version = 1` — a client kind is allowed
  more, nothing is removed and no shape changes.

- **Tech Design §9.4** writes down the grammar of the tree commands — positionals, flags and
  their defaults, the `--` rule, what `layout apply --from` accepts, where warnings go — which
  the CLI delta left to the implementation; its own decision 3 argues that a CLI verb is kept
  forever, and so is a flag. Tech 1.11. Found by the `spec-guardian` review of T-F0-20.

The PRD is **not** touched: REQ-CLI-005 already says what should happen, and this delta makes
the protocol permit it. The published schema's `client_kinds` per method is generated from the
registry, so it follows the code without an edit.

## Verification

- `TestCliClientMayDriveTheWorkspaceTree_REQ_CLI_005` — a `cli` connection's
  `workspace.*`, `tab.*`, `pane.*` (bar `move`) and `layout.*` calls are dispatched, not
  refused with `METHOD_NOT_FOUND`.
- `TestCliClientStillMayNotMovePanesOrDriveSessions_REQ_CLI_005` — `pane.move` and every
  `session.*` method still answer `METHOD_NOT_FOUND` to `cli`.
- The two existing tests that pin the old row are replaced by these, not deleted silently.
- `scripts/cli_roundtrip.sh` passes against a real daemon, which is where the gap was found.

## Phase

**F0**, as part of `T-F0-20`: it is the half of that task the CLI delta left out, and exit
criterion 4 cannot be met without it.
