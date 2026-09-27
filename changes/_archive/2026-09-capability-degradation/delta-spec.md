# Delta — what a daemon advertises, and what it answers for a method it cannot serve

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-09-20 — applied to specs/ and archived` |
| **Date** | 2026-09-20 |
| **Task** | T-F0-17 |
| **Raised by** | T-F0-17: §2 derives capabilities from "registered methods", and REQ-API-003 needs the unserved ones to stay registered |

## Evidence

REQ-API-003 asks for two different refusals:

> IF a client invokes a method name that this daemon version does not know, THEN THE SYSTEM
> SHALL reply `METHOD_NOT_FOUND` … IF the name is known but its capability is switched off in
> this build, THEN THE SYSTEM SHALL reply `NOT_IMPLEMENTED`.

The distinction is worth having: the first says "this daemon is older than your client", the
second says "this daemon was built without that module". A client can retry against another
daemon for the second and cannot for the first.

To answer `NOT_IMPLEMENTED`, the daemon has to keep the name in its method table when the
module behind it is absent — otherwise the name is unknown and the only honest answer is
`METHOD_NOT_FOUND`. And that collides with how §2 words the capability derivation:

> THE SYSTEM SHALL derive the list from its method table rather than declaring it statically,
> and SHALL NOT advertise a namespace whose methods are not registered.

Once every method is registered in every build, "whose methods are not registered" constrains
nothing. Taken literally it is worse than empty: a daemon built without the workspace module
would register all seventeen `workspace.*`, `tab.*` and `pane.*` methods and could advertise
`workspaces` while every one of them answered `NOT_IMPLEMENTED` — the precise drift the
sentence was written to prevent.

The two requirements are not in conflict. The clause is simply measuring the wrong thing: the
question a client asks is not "is the name registered" but "can this daemon do it".

## Decision

**1. Capabilities name what the daemon can serve, not what its table contains.** THE SYSTEM
SHALL advertise a namespace when at least one of its methods is served by this build, and
SHALL NOT advertise one whose methods are all unserved. The derivation stays mechanical — it
is still read from the method table, now together with whether each method's module is wired
in — because a hand-written list drifts.

**2. An unserved method keeps its name.** THE SYSTEM SHALL keep every method of the protocol
in its table whether or not this build serves it, so that `METHOD_NOT_FOUND` keeps its single
meaning: this daemon has never heard of the name. That is what makes the two codes of
REQ-API-003 distinguishable rather than two spellings of one refusal.

**3. The grain of a capability is the namespace; the grain of an answer is the method.**
A client asks `system.hello` whether this daemon has the tree at all, and calls the method
when it wants to know about that method. `api.schema` answers the finer question — it reports
`served` per method — which is why the capability list does not have to.

**4. `system` and `api` are never advertised.** Every client may always call `system.*`, and
`api.schema` is how a client discovers what the daemon speaks: a capability a client would
have to be granted before it could ask what it has been granted is circular.

## MODIFIED

### specs/api/umbral-daemon-api-v1.md → §2, the `capabilities` bullet

- **Before:** "…and SHALL NOT advertise a namespace whose methods are not registered".
- **After:** the derivation is over the methods this build *serves*; `system` and `api` are
  excluded by rule; and the sentence says what a client may conclude from an absent namespace.

### specs/api/umbral-daemon-api-v1.md → §9, the degradation bullet

- **After:** states that the name stays in the method table when its module is absent, which
  is the mechanism that makes `NOT_IMPLEMENTED` reachable at all, and points at
  `api.schema`'s `served` for the per-method answer.

### specs/api/umbral-daemon-api-v1.md → §5.37 `api.schema`

- **After:** the result is described: the protocol version, the methods with their client
  kinds, whether each is served and its parameter and result schemas, the notifications and
  the error table. Says the document is generated from the daemon's own types and that CI
  compares it with this specification.

## NOT MODIFIED

No data-model change and no new error code: `NOT_IMPLEMENTED` (-32012) is already in §3's
table and was simply unreachable. `protocol_version` stays at 1 — a namespace disappearing
from `capabilities` on a build that could not serve it was already the documented intent.

## Verification

- `TestUnknownMethodKeepsConnection_REQ_API_003` — an unknown name is refused and the
  connection keeps working, which is the half of the requirement the error code does not say.
- `TestUnservedMethodIsNotImplemented_REQ_API_003` — a daemon with no workspace module
  answers `workspace.create` with `NOT_IMPLEMENTED`, not `METHOD_NOT_FOUND`.
- `TestCapabilitiesFollowTheMethodTable` — a registered method whose module is absent does
  not put its namespace in the handshake. Its previous form asserted the opposite.
- `TestSchemaReportsWhatThisBuildServes_REQ_API_003` — `served` is per method and tracks the
  configuration.
