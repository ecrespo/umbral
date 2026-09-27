# ADR-0001: Umbral's architectural style

- **Status**: Proposed
- **Date**: 2026-09-11
- **Deciders**: Ernesto Crespo (Tech Lead)
- **System / scope**: all of Umbral (daemon, clients, CLI)

## Context
Decision inputs:
- **Organization:** one person plus coding agents; no operations team.
- **Domain:** terminal + agent runtime. There are two rich subdomains (terminal/blocks and agent/policies); no regulation applies.
- **Scalability:** a single machine. The bottleneck is PTY I/O latency and model latency, not throughput.
- **Consistency:** local and ACID (SQLite). The agent history is auditable (Art. 5 and 7), with no event-sourcing requirement.
- **Evolution:** high rate of change; third-party extensibility through MCP, ACP and model providers.
- **Operations:** the blast radius is the user's machine. Closing the UI must not kill shells or agents.
- **Assumptions:** no own cloud backend; distribution as per-platform binaries.

## Decision
**Base**: local client-server, with the `umbrald` daemon as a **modular monolith** ·
**Interior per unit**: hexagonal (domain / ports / adapters) · **Complements**:
- microkernel for tools, providers and MCP/WASM plugins;
- in-process event bus;
- agentic style in the agent layer;
- pipes & filters in the context engine.

Diagram: `docs/ARCHITECTURE.md` §5.1 and `specs/technical/umbral-architecture.md` §3.1.

Boundary rules that will be enforced: `.go-arch-lint.yml` following Tech Design §5.2
(Constitution Art. 3). Task T-F0-01.

## Alternatives considered

| Alternative | Attributes in favor | Attributes against | Why not |
|---|---|---|---|
| Monolithic app without a daemon (the UI owns the PTYs) | simplicity | closing the UI kills shells and agents; a single client | contradicts resilience and multi-client |
| Local microservices (one process per module) | fault isolation | complex IPC, deployment and debugging | no input requires independent deployment |
| Electron + Node (Wave style) | web ecosystem | memory, extra runtime | Go + WebView (Wails v3) covers the same, lighter |

## Consequences
**Positive:**
- interchangeable clients;
- durable sessions;
- a base for background agents;
- standard protocols.

**Negative / accepted costs:**
- a client-daemon protocol to version;
- cgo because of libghostty.

**Anti-patterns to watch and their mitigation:**

| Anti-pattern | Mitigation |
|---|---|
| God module in `agents` | narrow ports + `go-arch-lint` |
| Events without a contract | versioned event types in `internal/bus` |
| Cascading fallback with meta-providers | no own fallback inside OpenRouter / OmniRoute |

## Adoption plan
1. F0: daemon + TUI.
2. F1: agent.
3. F2: desktop client over the same protocol.

**Success metrics:**
- added latency < 5 ms p95;
- 0 boundary violations in CI;
- US-003 offline ≥ 70 %.

## Revisit when
Live multi-user collaboration or agent execution on shared infrastructure appears. That is when
an own remote service emerges.

```
Recommendation: Local client-server + Modular monolith + Hexagonal (+ Microkernel, event bus, Agentic, Pipes & Filters in context)
Why: resilience of sessions and agents, extensibility via MCP/ACP/providers, small team with a single binary
Didn't choose an app without a daemon because: the UI cannot own shells or background agents
Didn't choose microservices because: there are no teams or independent scaling to justify them
Risks / anti-patterns to watch: god module in agents, unstable libghostty API, local tool calling, cascading fallback
Revisit when: live multi-user collaboration or shared remote execution appears
```
