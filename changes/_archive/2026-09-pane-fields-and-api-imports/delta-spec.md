# Delta — the two fields `Pane` does not list, and the imports `api` cannot avoid

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-09-20 — folded into specs/` |
| **Date** | 2026-09-20 |
| **Task** | T-F0-15 |
| **Approved by** | Ernesto Crespo |
| **Raised by** | `spec-guardian` review of T-F0-15, findings 12 and its second half |

Both are the specification having fallen behind code that is already correct. Neither
changes behaviour; both change what an artifact claims, so both belong here rather than in
a commit message.

## Evidence

**1. `Pane` carries two fields API Spec §4 does not list.**

`toWirePane` emits thirteen keys. §4's `Pane` example shows eleven: `command` and `env` are
missing from it. They are not incidental — Data Model §2.4b gives `panes` a `command_json`
and an `env_json`, REQ-WS-005 names both among the things `layout.apply` reproduces, and
§5.8's `panes[]` result is the first documented place a client receives them. A client
generating its types from §4 would drop exactly the two fields that say what a pane runs.

The omission dates from T-F0-14, which added the wire type; T-F0-15 is where it started to
matter, because `layout.apply` makes the pair part of a result a client is expected to read.

**2. `api` imports two `domain` packages that §5.2's row does not grant.**

§5.2 reads: "`api` | `bus`, `store`, `config` and the `ports` of the modules it serves".
`internal/api` also imports `sessions/domain` and `workspaces/domain`, and cannot not: a
port's method signatures are written in domain types, so importing `sessions/ports` without
`sessions/domain` does not compile. The rule as written describes something Go cannot do.

`.go-arch-lint.yml` has papered over this since T-F0-03 by granting `api` **every**
`*-domain` in the tree — `agents`, `tools`, `context`, `llmgw`, `security` included, none of
which `api` imports. So the enforced rule is wider than the written one in a way that would
let `api` reach into a module it does not serve without anything noticing. That is the
opposite of what the file's own header promises: "no implicit permissions".

## Decision

**1. §4's `Pane` lists `command` and `env`.** They are omitted from the JSON when empty,
which is worth stating: absent and `[]` would otherwise look like a client's choice rather
than the daemon's, and "this pane runs a plain shell" is the common case.

**2. §5.2's `api` row admits the domain packages, and the rules file narrows to the ones
actually imported.** The row gains "and their `domain` packages, which a port's signatures
are written in"; `.go-arch-lint.yml` drops the five `*-domain` grants nothing uses and the
three `*-ports` grants for modules that do not exist yet. A task that wires a new module
into `api` adds its two rows, which is the point: the grant is where someone reads it.

The alternative for the second — leaving the rules file wide and calling the spec sentence
approximate — was rejected because the file is the enforcement. A sentence that is only
approximately what is enforced cannot be used to review anything.

## MODIFIED

### specs/api/umbral-daemon-api-v1.md → §4 `Pane`

- **Before:** the example carries `id`, `tab_id`, `workspace_id`, `session_id`, `thread_id`,
  `label`, `cwd`, `aliases`, `attention_state`, `state_source`, `metadata`, `created_at`,
  `closed_at`.
- **After:** the same, plus `command` and `env`, with a bullet saying what they are, where
  they come from (`pane.split`, `layout.apply`) and that both are omitted when empty.

### specs/technical/umbral-architecture.md → §5.2, the `api` row

- **Before:** "`bus`, `store`, `config` and the `ports` of the modules it serves".
- **After:** the same, plus "and their `domain` packages — a port's signatures are written
  in domain types, so the two arrive together", and the note that the grant is per module
  rather than blanket.

### .go-arch-lint.yml

- **After:** `api` may depend on `bus`, `store`, `config`, `sessions-ports`,
  `workspaces-ports`, `sessions-domain` and `workspaces-domain`, and on nothing else. The
  eight grants for modules `api` does not import are removed.

## NOT MODIFIED

No behaviour changes and no code changes beyond the rules file. `protocol_version` stays at
1: §4 gains no field the daemon was not already sending, so a client written against the old
text keeps working — this is the spec catching up, not the wire moving.

## Verification

- `task arch` green with the narrowed rules. An injected `api` → `agents/domain` import is
  **rejected** under them and was **accepted** under the old ones, which is the hole this
  closes; `tools/domain` likewise, and `sessions/domain` and `workspaces/ports` still pass.
  Worth recording how that was measured: the first probe reported "rejected" under both rule
  sets, and the reason was a malformed probe file that go-arch-lint could not parse — a
  non-zero exit that says nothing about boundaries. The runs above distinguish a parse
  failure from a rejection before believing either.
- `TestPaneCarriesItsCommandAndEnv` asserts the two fields on the wire and that they are
  omitted for a pane that runs a plain shell.
