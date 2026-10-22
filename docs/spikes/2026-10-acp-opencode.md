# Spike T-F2-00 — OpenCode over ACP with `coder/acp-go-sdk`

> 2026-10-22 · OpenCode 1.18.33 (`opencode acp`) · `github.com/coder/acp-go-sdk` v0.13.5 · ACP
> protocol 1 · `gpt-oss:20b` on the local Ollama · program `docs/spikes/acp/main.go`, transcripts
> under `docs/spikes/acp/transcripts/`.

## Setup

The spike program is a minimal ACP client.
- It launches `opencode acp` in a scratch git repository and logs every frame in both directions.
- It advertises `fs.readTextFile`, `fs.writeTextFile` and `terminal`.
- It grants every permission once.

It runs three turns:
1. "Reply with pong."
2. "Create `hello.txt` containing `hi`, then `cat` it."
3. A long answer, cancelled after 3 s.

OpenCode ran isolated: its own `HOME` and XDG directories, and a config with only the Ollama
provider and `gpt-oss:20b`. That way the user's own MCP servers and plugins played no part.

The scenario ran twice:
- **Run A:** OpenCode's default permissions.
- **Run B:** `"permission": {"edit": "ask", "bash": "ask", "webfetch": "ask"}`.

## What was observed

| | Run A, defaults | Run B, `ask` |
|---|---|---|
| `initialize` | 1.9 s; protocol 1; `loadSession`, MCP `http`/`sse`, `embeddedContext`, `image`; `session` `close`/`fork`/`list`/`resume` | same |
| `session/request_permission` | **0** | 2: the edit (with a unified diff, `locations` and `content.diff`) and the command (`rawInput.command`) |
| `fs/write_text_file` | **0**: OpenCode wrote `hello.txt` itself | 1, after the permission |
| `fs/read_text_file` | 0 | 0 |
| `terminal/create` | **0**: OpenCode ran `cat` in its own shell | **0**: it still ran the command itself, after asking |
| Turn 1 (`pong`) | `end_turn` after 77 s; a cold model and OpenCode's ~6 k-token system prompt | `end_turn` after 63 s |
| Turn 2 (file + command) | `end_turn` after 45 s | `end_turn` after 56 s |
| `session/cancel` | the turn ended `cancelled` **0.36 s** later | 0.39 s later |
| `session/update` kinds | `agent_thought_chunk` (most frames), `agent_message_chunk`, `tool_call`, `tool_call_update`, `available_commands_update` | same |

The permission options OpenCode offers are `once` (`allow_once`), `always` (`allow_always`) and
`reject` (`reject_once`). A tool call carries `kind`: `edit` for writes, `execute` for shell
commands.

## Conclusions for F2a

1. **An ACP agent mediates only what it chooses to.** With its default config, OpenCode never asks
   the client for anything: files and shell commands happen inside its own process. Analyze finding
   F-01 holds.
   - REQ-ACP-004 and REQ-ACP-005, as drafted, would cover nothing in practice.
   - §4's goal of "0 bypass" cannot be met by mediation alone.
2. **With its permissions set to `ask`, every write and every command reaches
   `session/request_permission` before it happens.** The request carries:
   - the target path (`locations`);
   - the diff;
   - the command (`rawInput.command`).

   That is what the policy engine needs to decide `WriteFS` or `Exec` on the real target. Writes then
   arrive through `fs/write_text_file`, which Umbral performs itself. Commands still run in OpenCode's
   own shell, so they never become Umbral PTY blocks; their output reaches Umbral only through
   `tool_call_update`.
3. **Umbral can impose that configuration.** It launches the agent, so it can launch it with its
   own config (`OPENCODE_CONFIG`, or an isolated `XDG_CONFIG_HOME` as here) that sets every
   side-effect tool to `ask`.
   - This is per agent, so a detection/ACP manifest needs a "how to force ask" entry for each one.
   - An agent with no such switch cannot be mediated, and would need a sandbox or a refusal.
4. **Reads are not mediated.** OpenCode reads files itself, even with `fs.readTextFile`
   advertised. Keeping secrets outside the worktree away from the agent needs either:
   - its own permission switch (OpenCode's `external_directory`, not verified in this spike); or
   - a sandbox.
5. **Cancellation is fast and clean:** about 0.4 s to a `cancelled` stop reason, well inside
   REQ-ACP-008's 500 ms for the turn.
6. **Throughput:** OpenCode's system prompt makes even "pong" a 6 k-token request. US-F2-001's
   budget must allow minutes per turn on `gpt-oss:20b`, and REQ-ORC-005's per-provider queue
   matters, since three threads share one local Ollama.
7. **Q-F2-05: `coder/acp-go-sdk` v0.13.5 is sufficient.**
   - It covers the client side used here: `initialize`, sessions, prompt, cancel, permission, `fs/*`
     and `terminal/*`.
   - It has typed unions for every update.
   - It is maintained (13 releases).

   Recommendation: adopt it and pin it in the constitution's stack table.

## Recommendation for the F2 PRD

Rewrite REQ-ACP-003…005 around observed behaviour:

- **A hosted agent is launched with Umbral's own configuration, which forces `ask` on every
  side-effect tool.** Every `session/request_permission` is decided by the policy engine:
  `edit` → `WriteFS` on `locations`, `execute` → `Exec` on `rawInput.command`, fetch →
  `Network`. Writes Umbral receives through `fs/write_text_file` are performed by Umbral.
- **An agent whose manifest cannot force `ask` is refused**, unless a sandbox confines it. Both the
  sandbox and the refusal are the Tech Lead's call (Analyze F-01).
- **The agent's own reads and model calls are disclosed, not mediated.** This extends Q-F2-02's
  decision.
- **Commands the agent runs itself are recorded from `tool_call_update`** as audited entries, not
  as PTY blocks. `terminal/create`, when an agent uses it, still runs in an Umbral session.
