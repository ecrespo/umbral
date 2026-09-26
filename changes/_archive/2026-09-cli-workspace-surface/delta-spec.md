# Delta — the command line the constitution already promised: `umb` for the workspace tree

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-09-26 — applied to specs/ and archived` |
| **Date** | 2026-09-21 |
| **Task** | T-F0-20 (new) |
| **Raised by** | Gap 1 of the F0 final verification (`docs/checkpoints/2026-09-20-f0-closure.md`), left there for "the next session to raise". This is that raise. |

## Evidence

**An exit criterion that cannot be attempted.** The plan's fourth F0 criterion reads: *"a
script creates a workspace, splits, exports and reapplies a layout using only the CLI"*. It
is not failing — it cannot be performed. `umb` dispatches exactly five commands
(`cmd/umb/main.go:65-74`): `status`, `block`, `api`, `version`, `help`. There is no
`umb workspace`, `umb tab`, `umb pane` or `umb layout`.

**The daemon side is finished.** `workspace.create/list/focus/rename/close`,
`tab.create/list/focus/rename/close`, `pane.split/list/get/focus/rename/move/close`,
`layout.export` and `layout.apply` are all served, specified in API §5.4-5.8, and covered by
REQ-WS-001…007. The F0 verification drove every one of them end to end — over a hand-written
JSON-RPC client, *because `umb` cannot reach them*. The missing piece is a client, not a
capability.

**The constitution already spends this feature.** The Art. 6 amendment of 2026-09-20 permits
`w<n>`, `w<n>:t<m>` and `w<n>:p<m>` to break the type-prefixed-ULID rule, and its written
rationale is:

> *What it must be instead is typeable, because the command line is this design's addressing
> surface — `umb pane split w1:t1` is the feature, and the same command with a 26-character
> ULID is that feature with its usability removed.*

An exception to Art. 6 is justified by a command that nobody built. Either the command exists
or the rationale is false; leaving it as it stands means the constitution's own justification
does not survive contact with the repository.

**No task covers it and no REQ requires it.** `python3 tools/sdd_check.py` was green at
108/108 *before* this delta precisely because nothing asked for this. That is the failure mode the checker cannot
see: a criterion in the plan with no requirement under it. So this is a gap in the
specification package, which makes it a Delta rather than a task someone adds quietly.

## Decisions

**1. The CLI mirrors the served methods; it does not invent a model.** Every command is one
JSON-RPC call to the method of the same name. `umb pane split w1:p1 --direction right` is
`pane.split`. No client-side state, no local cache, no convenience that the daemon cannot
answer for — DD-001 says the daemon owns all state and the clients are thin, and a CLI that
composed several calls into one verb would be the first place that stopped being true.

| Command | Method |
|---|---|
| `umb workspace create\|list\|focus\|rename\|close` | `workspace.*` |
| `umb tab create\|list\|focus\|rename\|close` | `tab.*` |
| `umb pane split\|list\|get\|focus\|rename\|close` | `pane.*` |
| `umb layout export\|apply` | `layout.*` |

**2. Objects are addressed by their public identifiers, positionally.** `umb tab create w1`,
`umb pane split w1:p1`, `umb layout export w1:t1`. That is the grammar Art. 6 names, and the
positional argument is what makes it typeable; an object flag (`--pane-id w1:p1`) would meet
the letter of the rationale and lose its point. REQ-WS-007's aliases resolve here for free,
because resolution is the daemon's.

**3. `pane.move` stays out.** Its `destination` is a tagged object
(`{"type":"tab"|"new_tab"|"new_workspace", …}`), and flattening a tagged union into flags is
a design decision with more than one defensible answer. It is not needed by the criterion,
and inventing the syntax under time pressure is how a CLI acquires a verb it has to keep
forever. Deferred, explicitly, rather than forgotten.

**4. Human output by default, `--json` for scripts, already-specified exit codes.** The two
shapes every existing `umb` command has (REQ-CLI-002's `--json` and REQ-CLI-004's `0`/`1`/`69`)
are reused verbatim. Nothing new is defined about exit codes, because they are already
specified for the binary rather than per command.

**5. `layout export` writes stdout, `layout apply` reads a file or `-`.** The criterion is a
*script*: `umb layout export w1:t1 --json > l.json` then
`umb layout apply w2 --from l.json`, with `--from -` reading stdin so the round trip is a
pipe. Any other arrangement makes the daemon or a temp file part of a criterion that is about
the command line.

**6. Scope is F0's tree and nothing else.** No `umb session`, no `umb ai` (REQ-CLI-001 is
F1's), no `pane.report_state` (REQ-INT-002 is F1's). The delta closes one criterion and does
not become the place where the whole CLI gets designed.

## Requirements

Added to PRD §6.8 (applied 2026-09-21, PRD 1.11):

> **REQ-CLI-005** · MUST · event — WHEN `umb workspace`, `umb tab`, `umb pane` or `umb layout`
> runs with one of the subcommands the daemon serves, THE SYSTEM SHALL invoke the JSON-RPC
> method of the same name and print its result, addressing workspaces, tabs and panes by the
> public identifiers of REQ-WS-002 given as positional arguments, so that the addressing
> surface that Art. 6's exception is justified by exists. THE SYSTEM SHALL print the daemon's
> answer as JSON WHERE `--json` is given, and SHALL use the exit codes of REQ-CLI-004.

> **REQ-CLI-006** · MUST · event — WHEN `umb layout export --json` runs, THE SYSTEM SHALL
> write the layout to stdout in the form `umb layout apply` consumes, and WHEN
> `umb layout apply --from <file>` runs, THE SYSTEM SHALL read the layout from that file, or
> from stdin WHERE the file is `-`, so that a script reproduces a tab in another workspace
> through a pipe and without a shared filesystem location.

`--json` is named in REQ-CLI-006 rather than left to decision 5 because "write the layout to
stdout" alone is satisfied by a human-readable export, which `apply --from` could not read
back; the round trip is the requirement, so the interchange form belongs in it.

## Not modified

**The protocol.** No method, parameter, result or error code changes; `protocol_version`
stays at 1 and `task schema` compares the same 33 methods. This delta adds a *client*.

**The daemon.** Nothing under `internal/` changes. `T-F0-20` touches `cmd/umb/**` and its
tests only.

**Art. 6.** The amendment stands as written. This delta makes its rationale true rather than
rewriting it — which is the point, because the alternative repair (deleting the sentence and
justifying `w<n>` on something else) would leave the identifiers with no justification at all.

## Phase

**F0, as `T-F0-20`.** It closes an F0 exit criterion, against a daemon-side surface F0 already
finished, and the constitution's Art. 6 exception depends on it. Moving it to F1 would ship an
MVP whose constitution cites a feature the MVP does not have.

**Estimate:** 1.5 d. Four command families over one existing JSON-RPC client, no daemon work.

## Verification

- `TestWorkspaceCreatePrintsTheTree_REQ_CLI_005` — `umb workspace create <cwd> --json` prints
  the workspace, tab and root pane the method returns.
- `TestPaneSplitAddressesByPublicId_REQ_CLI_005` — `umb pane split w1:p1` sends `pane.split`
  with that `pane_id`, and an alias from REQ-WS-007 reaches the same pane.
- `TestUnknownSubcommandExitsOne_REQ_CLI_005` — a misspelled subcommand exits 1 with usage,
  and does not reach the daemon.
- `TestLayoutExportApplyThroughAPipe_REQ_CLI_006` — `layout export --json` piped into
  `layout apply --from -` reproduces the tab in a second workspace.
- `TestLayoutApplyReportsWarnings_REQ_CLI_006` — REQ-WS-005's warnings reach the user's
  terminal rather than being dropped by the printer.
- **The criterion itself, as the script it describes**, checked in as
  `scripts/cli_roundtrip.sh` and run against a real daemon: create a workspace, split it,
  export the layout, apply it into a second workspace, assert the second tab's tree matches
  the first. That script *is* exit criterion 4, and it is the only artifact that can close it.
