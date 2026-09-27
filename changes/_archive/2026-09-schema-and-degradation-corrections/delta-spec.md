# Delta — corrections to the capability rule and the published schema

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-09-20 — applied to specs/ and archived` |
| **Date** | 2026-09-20 |
| **Task** | T-F0-17 |
| **Raised by** | a `spec-guardian` round on T-F0-17: one sentence ratified the same day is not met by any build, one §9 bullet contradicts the §2 bullet the previous delta rewrote, and three methods publish a required parameter the daemon defaults |

## Evidence

Four separate problems, all found by reviewing `2026-09-capability-degradation` against the
code it was written for.

**1. A rule no build can satisfy.** §9 says "THE SYSTEM SHALL keep **every method of the
protocol** in its table whether or not this build serves it". The daemon's table holds 33
methods; §5 documents 52. `thread.send`, `model.list`, `mcp.*`, `wait.*`, `rules.*` and
`policy.explain` are not in it, so they answer `METHOD_NOT_FOUND` — which the sentence exists
to prevent. Registering nineteen names that no build implements would mean inventing
parameter and result shapes for methods nobody has written.

The rule is not wrong, it is too wide. Two situations look alike and are not:

- A daemon built **before a phase landed** genuinely does not know `thread.send`. That is
  word for word REQ-API-003's first sentence, "a method name that this daemon version does
  not know", and `METHOD_NOT_FOUND` is the correct answer.
- A daemon of a version that **has** the method, assembled or configured without the module
  behind it, does know the name. `NOT_IMPLEMENTED` tells the client something it can act on:
  another daemon of this same version, configured differently, would serve it.

So the two codes divide by *version* and by *build*. Claiming `NOT_IMPLEMENTED` for a method
nobody has written would send a client looking for a differently-configured daemon that
cannot exist.

**2. §9 still contradicts §2.** The previous delta rewrote §2's capability bullet to derive
from what a build *serves* and argued at length that deriving from the method table is wrong
now that every implemented method is registered. It left §9's "Optional capabilities" bullet
saying "what a given daemon returns is whatever its **method table holds**" — the exact
wording its own Evidence calls wrong. The delta's MODIFIED section named §2, §9's degradation
bullet and §5.37, and missed this one.

**3. Three parameters are published required that the daemon defaults.** `block.list` and
`block.search` published `limit` and `cursor` as required; `block.get` published `include`.
All three are optional in the daemon — an absent `limit` becomes §3's default of 50,
`ParseCursor("")` is the first page, an absent `include` returns no output section — and
`umbral-tui` calls `block.list` with no `cursor` at all, a request the published schema
declared invalid. The comparison passed because §5's shorthand omits the `?` in the same
three places: **two derivations agreeing on the same mistake**, which is the one failure mode
a two-sided check cannot catch by itself.

**4. The document was not a JSON Schema, and §5.37 was quietly reworded rather than the gap
being declared.** REQ-API-004 asks for "the JSON Schema of the protocol … so that clients and
tests can validate against it". What was printed had no `$schema`, used OpenAPI 3.0's
`nullable` keyword — which a JSON Schema validator ignores, so `focused.workspace_id` would
have been rejected on every fresh daemon — and emitted `{"type": ""}`, which no validator
accepts. §5.37's "the **JSON Schema** document" had become "the **protocol** document" in the
implementing commit, while §9 and the PRD still said JSON Schema: the specification
disagreeing with itself, with the reword doing the work a Delta should have done.

## Decision

**1. The rule names what a build implements, not what the protocol documents.** THE SYSTEM
SHALL keep in its method table every method **it implements**, whether or not the module
behind it is wired in. A method this version does not implement is unknown, and
`METHOD_NOT_FOUND` is correct for it. §9 says so explicitly, so the next reader does not have
to reconstruct the distinction from two error codes.

**2. §9's capability bullet is brought into line with §2.** What `system.hello` returns is
what the daemon can do, which is narrower than its method table exactly when a module is
absent.

**3. `limit`, `cursor` and `include` are optional in §5, as they are in the daemon.** Their
defaults were already stated in §3 and in §5.13's own prose; only the shorthand disagreed.

**4. The document publishes real JSON Schema.** Every `params`, every `result` and every
notification payload is a JSON Schema draft 2020-12 document carrying its own `$schema`, so a
client validates against one directly. Nullability is `"type": ["string", "null"]`. A member
with no constrained shape carries no `type`, which is the schema `{}`. CI validates all of
them against the metaschema — `tools/api_schema_check.py` does it and it is blocking, because
a schema nothing validates is a description with a misleading name.

**5. `client_kinds` is written out in full.** An empty list meant "every kind", so a client
filtering on it would have concluded `block.list` was callable by nobody. §5.37 says the list
is complete and never a sentinel.

**6. A served method with no documented parameters is a failure, not a note.** Seventeen of
thirty-three methods were being reported as "not compared" because §5.4-§5.6, §5.1, §5.2,
§5.10, §5.12 and §5.15 never wrote their request shapes down. The missing lines are added, all
33 are compared, and the checker now fails on an uncompared method: half a gate reported as a
green tick is the failure mode this project keeps finding.

## MODIFIED

### specs/api/umbral-daemon-api-v1.md → §9, "The mechanism the second case needs"
- **Before:** "keep every method of the protocol in its table".
- **After:** "keep every method **it implements**", plus a bullet stating when
  `METHOD_NOT_FOUND` is the right answer and why `NOT_IMPLEMENTED` would mislead.

### specs/api/umbral-daemon-api-v1.md → §9, "Optional capabilities"
- **After:** derived from what the daemon serves, narrower than its method table whenever a
  module is absent. Resolves the contradiction with §2.

### specs/api/umbral-daemon-api-v1.md → §2, client kinds
- **After:** the `cli` row grants `api.*`. `api.schema` was already served to every kind and
  the allowlist did not say so.

### specs/api/umbral-daemon-api-v1.md → §5.12 `block.list`, §5.13 `block.get`, §5.18 `block.search`
- **After:** `limit?`, `cursor?` and `include?`, matching §3's stated defaults and the daemon.

### specs/api/umbral-daemon-api-v1.md → §5.37 `api.schema`
- **After:** says the embedded schemas are JSON Schema draft 2020-12 with their own `$schema`,
  how nullability is spelled and why, what an absent `type` means, that CI validates them, and
  that `client_kinds` is always the full list.

### specs/api/umbral-daemon-api-v1.md → §5.1, §5.2, §5.4, §5.5, §5.6, §5.10, §5.12, §5.15
- **After:** the request shapes of seventeen methods that had none written down, so the CI
  comparison covers all 33 served methods instead of 16.

## NOT MODIFIED

No PRD change: REQ-API-003 and REQ-API-004 are met as written once the above are true, and
REQ-API-004's "JSON Schema" is now accurate rather than being softened. No data-model change.
`protocol_version` stays at 1: `client_kinds` gaining entries and parameters becoming optional
both widen what a client may send.

## Verification

- `python3 tools/api_schema_check.py` — 33 of 33 served methods compared, none skipped, and
  every embedded schema validated against the 2020-12 metaschema. Verified by mutation: an
  emitted `{"type": ""}`, a removed `**Params:**` line and a wrong error number each fail it.
- `TestBlockListAcceptsNoCursor_REQ_BLK_006` — the request `umbral-tui` actually sends.
- `TestSchemaEmbedsValidJSONSchema_REQ_API_004` — the generated schemas carry `$schema`,
  express nullability as a type union, and never name an empty type.
- `TestDeclaredResultsMatchTheHandlers_REQ_API_004` — via the dispatcher hook: every wire test
  in the package now checks the result shape it exercises against the one published.
