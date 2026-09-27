# Delta — the `unknown` rollup, and where a pane's attention state comes from in F0

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-09-20 — folded into specs/` |
| **Date** | 2026-09-20 |
| **Task** | T-F0-14 |
| **Approved by** | Ernesto Crespo |
| **Raised by** | T-F0-14: REQ-WS-006 produces a value the wire type cannot carry, and nothing in F0 assigns the input it rolls up |

## Evidence

**1. `rollup_state` cannot express the value REQ-WS-006 propagates.**

REQ-WS-006 ends: "…in the order `blocked` > `working` > `done` > `idle` > `unknown`. A
workspace with no panes or threads reports `idle`, and **`unknown` propagates only when
every child is `unknown`**." So `unknown` is a rollup outcome, named twice.

API Spec §4 `Workspace` says: "`rollup_state`: `blocked` | `working` | `done` | `idle`
(REQ-WS-006)". Four values. The fifth, the one the requirement's own sentence propagates,
is missing.

This is not a corner case in F0, it is *the* case. A pane's semantic state comes from
`pane_state_reports`, which is migration **0004** (Data Model §2.4c, F1), or from a thread,
which is also F1. So in F0 every pane is `unknown`, every workspace's children are all
`unknown`, and REQ-WS-006's propagation clause fires on every single `workspace.list`.
Whatever the daemon puts on the wire there, it is either a value §4 forbids or a value
REQ-WS-006 forbids.

**2. Nothing in F0 gives a pane a state other than `unknown`.**

API Spec §4 `Pane` documents `state_source` as "`umbral:agent` for Umbral's own agent,
`umbral:shell` **when it is derived from the block lifecycle**, or the `source` of an
external integration (REQ-INT-002)". The block lifecycle is F0 — T-F0-09 built it — so
`umbral:shell` reads like an F0 source. But no requirement mandates that derivation, no
task in `specs/tasks/` claims it, and no artifact says what it maps: which block state
becomes `working`, whether a non-zero exit is `blocked` or `done`, what an interactive
block means. T-F0-14's REQ list is REQ-WS-001, 002, 003, 006 and 007; none of them
produces a pane state.

A single sentence in a type description is not a specification of a derivation, and
inventing one here would put behaviour in the code that no artifact asked for — the thing
Art. 9 exists to prevent.

## Decision

**1. `unknown` joins the `rollup_state` type.** The requirement is the normative sentence
and §4's list is a summary that lost a value. The alternative considered was mapping
`unknown` to `idle` on the wire, which was rejected for two reasons: it contradicts
REQ-WS-006's propagation clause directly, and it makes the daemon claim knowledge it does
not have. "Nothing is happening in this workspace" and "nobody has told me what is
happening in this workspace" are different facts, and a sidebar that renders the second as
the first is lying to the user at exactly the moment an integration has gone quiet.

**2. In F0 every pane's `attention_state` is `unknown` and its `state_source` is `null`.**
The rollup is implemented exactly as REQ-WS-006 writes it and unit-tested over the full
ordering; what F0 lacks is not the rollup but any producer of an input to it. So F0
workspaces report `rollup_state: "unknown"` unless they are empty, in which case
REQ-WS-006's own sentence makes them `idle`.

**3. The `umbral:shell` derivation is deferred, and recorded as deferred.** It stays
undefined until a task owns it with a requirement behind it. The natural home is alongside
REQ-INT-002's `pane.report_state` in F1, where the other state sources live and where
`pane_state_reports` exists to hold the answer.

## MODIFIED

### specs/api/umbral-daemon-api-v1.md → §4 `Workspace`

- **Before:** "`rollup_state`: `blocked` | `working` | `done` | `idle` (REQ-WS-006)".
- **After:** the same list with `unknown` appended, and a sentence saying when it appears:
  every child is `unknown`, which in F0 is every workspace that has a pane.

### specs/api/umbral-daemon-api-v1.md → §4 `Pane`

- **After:** a note that `attention_state` is `unknown` and `state_source` is `null` until a
  source reports one, that `pane_state_reports` (migration 0004) is that source's store, and
  that F0 therefore has no producer.

### specs/prd/umbral-mvp.md → REQ-WS-006

- **After:** unchanged in substance; the criterion already says what happens. A parenthesis
  notes that `unknown` is a value clients must render, since the contradiction arose from a
  reader taking §4's list as the complete set.

## NOT MODIFIED

No data-model change: `pane_state_reports` already carries `CHECK (state IN
('idle','working','blocked','done'))` and is right to, because a *report* of `unknown` is
not a report. `unknown` is the absence of a row, not a row. No migration changes.
`protocol_version` stays at 1: adding a value a client must already tolerate under §9's
additive rule is not a break.

## Verification

- `TestRollupPrefersBlocked_REQ_WS_006` walks the whole ordering including the two clauses
  §4 obscured: all-`unknown` children roll up to `unknown`, and an empty workspace is `idle`.
- `TestWorkspaceCreateReturnsTree_REQ_WS_001` asserts the fresh workspace's `rollup_state`
  is `unknown` and not `idle`, which is the observable difference between this decision and
  the rejected alternative.
