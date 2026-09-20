# ADR-0002: Orchestration surface — workspaces, waits and integrations

- **Status**: Proposed
- **Date**: 2026-09-20
- **Deciders**: Ernesto Crespo (Tech Lead)
- **Scope**: `umbrald` protocol and the `workspaces`, `waits` and `integrations` modules
- **Supersedes**: nothing. It extends ADR-0001 without changing its architectural style.

## Context

The specs written on 2026-09-11 covered the terminal (sessions, blocks) and the agent (threads,
tools, permissions, models), and left the structure of the work — tabs, splits, projects — inside
the TUI, with no presence in the contract.

A review of Herdr (Rust, Apache-2.0, ~37k stars), which solves the adjacent problem of being the
runtime for third-party coding agents, showed that the part we left out of the contract is exactly
the part that makes a daemon like this usable:

1. Its object model is `session → workspace → tab → pane`, all of it addressable from the CLI and
   the socket API, and the sidebar rolls each workspace up to its most urgent agent state.
2. Its automation primitives are waits: a wait owned by the server, pinned to the resolved
   occupant of a pane, plus a combined "submit prompt and wait" request that removes the race
   between two calls.
3. Its clients bootstrap with a one-shot snapshot and then apply a buffered event stream.
4. Its integrations report state over the socket with a stable `source` and a sequence number, and
   display metadata travels on a separate channel that never affects waits or rollups.
5. Client and server negotiate capabilities instead of requiring matching builds; a missing method
   disables one action rather than the connection.

Three problems the reviewed design has, which we do not want to inherit:

- Screen heuristics act as a state authority for most agents, which is fragile by construction: an
  unfamiliar prompt shows as idle instead of blocked until the rules learn that screen shape.
- Detection rules update themselves from the project's servers, which would conflict with our
  Art. 4.
- There is no permission engine, no secret redaction and no egress audit: on that axis it is not a
  model to copy.

## Decision

Add to the MVP an orchestration surface of our own:

- **Structure in the daemon** (DD-009): workspaces, tabs, panes, stable public identifiers,
  portable layouts and rollup state (REQ-WS-001 to 007).
- **Snapshot plus sequenced events** (DD-010): `session.snapshot` with a `seq` and a documented
  bootstrap protocol (REQ-API-001, REQ-API-002).
- **Server-owned waits** (DD-011): `thread.wait`, `thread.send` with `wait`, and
  `block.wait_output` (REQ-AUT-001 to 004).
- **One state authority per pane with fixed precedence** (DD-012): our own agent, then an external
  integration, then our own derivation from the block lifecycle. Screen heuristics stay out of the
  MVP; third-party detection arrives in F2 as the lowest-priority layer.
- **Semantics and presentation as separate channels** (DD-013): `pane.report_state` versus
  `pane.report_metadata` with TTL and bounded size.
- **Tiered restore with written guarantees** (DD-014), including optional pane screen replay,
  disabled by default because output contains secrets.
- **Schema generated from code and checked in CI** (DD-015), plus capability degradation
  (REQ-API-003, REQ-API-004).
- **Explainability applied to permissions** rather than to state detection: `policy.explain`
  (REQ-SEC-009). This is where our differentiator lives, so this is where the trace is worth
  having.
- **Rule overrides**, with local precedence and remote updates disabled by default
  (REQ-SEC-010, REQ-SEC-011).

## Alternatives considered

| Alternative | In favor | Against | Why not |
|---|---|---|---|
| Keep the structure inside the TUI | less MVP work | not scriptable, the F2 client duplicates it, nowhere to attach the rollup | it is the gap this ADR exists to close |
| Adopt screen heuristics for third-party agents now | immediate compatibility with every CLI agent | a second source of truth, fragile, maintenance per agent | F2, and behind hooks and ACP |
| Postpone waits to F2 | shorter F1 | without waits nothing can be automated, and MCP/ACP integration suffers | waits are the cheapest primitive with the highest leverage |
| Multi-machine federation in the MVP | attractive feature | multiplies the failure surface before the core is proven | F2, as an extension of durable SSH |

## Consequences

**Positive**

- The TUI, the CLI, the F2 desktop client and an external agent all speak the same surface.
- An agent can drive Umbral without Umbral having to embed that agent.
- The restore guarantees stop being implicit.

**Costs accepted**

- F0 grows by about a week and F1 by about a week.
- One more contract to version, although everything added is additive and `protocol_version` stays
  at 1.
- `workspaces` becomes a central module and is the first candidate to degenerate into a god module;
  the Art. 3 boundaries and `go-arch-lint` are the control.

## Revisit when

Third-party agents (via ACP) become the main workload rather than our own agent. At that point the
precedence of DD-012 and the placement of detection have to be re-examined.

## Sources

- Herdr documentation: concepts, socket API, agents, agent automation, session state, connecting
  machines, integrations and agent skill — <https://herdr.dev/docs/>
- Herdr repository (Apache-2.0) — <https://github.com/herdrdev/herdr>
