# Integration prompts — Umbral

Part of the cross-project integration map (2026-10-31): #1 AG-UI prismal → agentkit,
**#2 OTel GenAI → agentlens**, **#3 ACP/A2A Umbral ⇄ prismal**, **#4 JSON-RPC Umbral → xtermjs**,
**#5 shared MCP host (mcphost)**. Umbral touches #2, #3, #4 and #5.

Counterparts: `prismal/docs/integration-prompts.md`, `prismal-server/docs/integration-prompts.md`,
`reflex-agentlens/docs/integration-prompts.md`, `reflex-xtermjs/docs/integration-prompts.md`,
`reflex-mcphost/docs/integration-prompts.md`.

Every prompt below follows the repository's rules: tests first, every test named after
its REQ, one branch and one PR per task, undecided points settled in a delta under
`changes/` before merge (Constitution, `specs/tasks/*.md` conventions).

---

## Prompt I2-B · GenAI attributes on the agent's spans

```
You are working in Umbral (Go ≥1.25). Read specs/constitution.md, internal/obs
(T-F1-18, OTel), internal/agents, internal/llmgw and the OpenTelemetry GenAI
semantic conventions (pin the version in a constant).

Goal: the spans Umbral already exports for a thread's turns carry the gen_ai.*
attributes, so reflex-agentlens shows an Umbral thread next to a prismal run
in the same timeline.

Scope:
1. llmgw: every provider call span gets gen_ai.operation.name=chat,
   gen_ai.provider.name (ollama, llama.cpp, lm-studio, openrouter, hf, omniroute,
   yzma), gen_ai.request.model, gen_ai.usage.input_tokens / output_tokens.
2. tools: every tool call span gets gen_ai.operation.name=execute_tool and
   gen_ai.tool.name; run_command and terminal_interact additionally carry the
   block id so a trace links to a block.
3. agents: the turn span gets gen_ai.agent.name=umbral and the thread id;
   an ACP-driven thread gets the third-party agent's name.
4. Content capture stays OFF (REQ-SEC redaction rules apply before any attribute
   is set; a redacted value never reaches an exporter).
5. `umb obs export --thread w1:t1 --json` writes the spans of a thread as a
   fixture, for agentlens tests.
6. Tests named after a new REQ-OBS-00x per attribute group; the existing OTel
   tests stay green.

Acceptance (EARS):
- WHILE OTel is disabled THE SYSTEM SHALL change nothing (NFR gates stay green).
- THE SYSTEM SHALL never export a value the redaction layer would have masked.
```

## Prompt I3-A · Run a prismal agent as an ACP thread

```
You are working in Umbral. Read internal/acp (T-F2-07..09: client, mediation,
cancel, traces, egress_visibility), T-F2-10 (third-party agent detection) and
docs/spikes/2026-10-acp-opencode.md. The counterpart is the `prismal-acp`
entry point described in prismal-server/docs/integration-prompts.md (I3-B).

Goal: `umb agent add prismal` registers prismal as a detected ACP agent and a
thread can be opened on it with the same permission mediation Umbral applies to
OpenCode, Claude Code or Gemini CLI.

Scope:
1. Detection manifest for prismal-acp (command, version probe, capabilities,
   egress_visibility declared by the agent); shipped under the same registry
   the other agents use.
2. Mediation: file reads/writes and terminals requested by prismal go through
   Umbral's policy (ask / normal / auto-edit / full-auto inside a worktree);
   verify that a denial reaches prismal as an ACP error and the turn continues.
3. Permission layering delta: record in changes/ the decision that, inside
   Umbral, the client's policy is the gate and the agent's own interceptor is
   audit-only; link the matching flag on the prismal side.
4. Live test (build tag `live`): US-F2-001 variant "three agents, one repo"
   where one of the three is prismal, with the same F2a budgets.
5. docs/user: one page "Using prismal inside Umbral".

Acceptance (EARS):
- WHEN prismal requests a terminal THE SYSTEM SHALL give it a pane in the
  thread's worktree, owned like any run_command.
- IF prismal declares egress THE SYSTEM SHALL show it in the agent panel's
  extensions view before the first prompt.
```

## Prompt I4-A · A loopback-authenticated bridge for web clients

```
You are working in Umbral. Read internal/api (JSON-RPC server, 0600 socket,
per-installation token), specs/api/umbral-daemon-api-v1.md §2 (client kinds
and allowlists) and T-F2-41 (remote daemons over SSH, done 2026-10-31). The consumer
is reflex-xtermjs (see reflex-xtermjs/docs/integration-prompts.md, I4-B).

Goal: a web client on the same machine can speak the published protocol over a
WebSocket, with the same authentication strength as the Unix socket, so a Reflex
app can render Umbral sessions and blocks.

Scope:
1. `umbrald --web-bridge` (off by default): a loopback-only WebSocket listener
   that proxies JSON-RPC frames 1:1 to the socket; the client must present the
   installation token in the first frame (system.hello), never in the URL.
2. Client kind `web` with an allowlist: session.*, block.*, workspace read
   methods; no agent methods in this iteration.
3. Respect the frame limit and the seq semantics of session.subscribe
   (REQ-TERM-004, T-F1-33); a slow web client is dropped the way a slow socket
   client is, with the same notification.
4. `umb api schema --json` already publishes the protocol; add the bridge to
   the schema's transports section.
5. Tests named after REQ-API-00x: token required, loopback only, allowlist
   enforced, seq strictly increasing across the bridge.

Acceptance (EARS):
- IF the bridge is reached from a non-loopback address THEN THE SYSTEM SHALL
  refuse the connection before the handshake.
- THE SYSTEM SHALL add no measurable latency to the REQ-TERM-006 gate when the
  bridge is off.

Out of scope: TLS and remote access (that is T-F2-41); a UI.
```

## Prompt I5-B · Read the shared MCP config and consent policy

```
You are working in Umbral. Read internal/mcp and `umb mcp` (T-F1-17, T-F1-37),
the policy engine and signed rule bundles (T-F1-03, T-F1-28..30), and, in
reflex-mcphost, specs/data-model/mcphost-schema.md.

Goal: `umb mcp import` reads the standard `mcpServers` JSON used by mcphost,
Claude Desktop, Cursor and VS Code, and `umb mcp policy import` turns an
exported mcphost consent policy into Umbral rules, so one config and one policy
serve the terminal, the engine and the host.

Scope:
1. Importer for stdio and Streamable HTTP servers; env references map to
   env_refs backed by the keyring (REQ-SEC), never inlined.
2. Policy translation: allow/ask/deny per server+tool → Umbral rule bundle,
   signed on import by the local trust store; conflicts with existing rules are
   reported, not merged silently.
3. Round trip: `umb mcp export` writes the same JSON shape back so mcphost can
   read Umbral's servers.
4. Tests: fixtures copied from mcphost's suite; REQ-MCP-00x per behaviour.

Acceptance (EARS):
- IF an imported server declares an env var the keyring does not hold THEN
  THE SYSTEM SHALL import it disabled and say which reference is missing.

Out of scope: sharing code with mcphost (file formats only).
```
