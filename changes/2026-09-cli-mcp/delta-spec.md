# Delta — `umb mcp`: the CLI manages MCP servers as it will manage skills

| Field | Value |
|---|---|
| **Status** | `PROPOSED 2026-09-26 — awaiting the Tech Lead's approval` |
| **Date** | 2026-09-26 |
| **Task** | T-F1-37 |
| **Raised by** | The Tech Lead, 2026-09-26: skills and MCP should reach Umbral's agent with the same features ("sí, añádelos") |

## Evidence

**MCP servers can be managed from every client except the CLI.**

- API §5.27 serves `mcp.server.add`, `remove` and `list`.
- API §2's `cli` row does not include `mcp.*`, so `umb` answers `METHOD_NOT_FOUND` for all three.
  That is the same kind of gap `2026-09-cli-allowlist` closed for the workspace tree.
- No `umb mcp` command exists, in the code or in Tech Design §9.4.
- Delta `2026-09-skills-cli` gives skills `umb skill`. Without this delta, a user can script the
  installation of a skill and cannot script the connection of an MCP server, though both extend
  the same agent.

**Nobody can see a server's state.**

- REQ-MCP-003 marks a server `unavailable` and emits `mcp.server_state`.
- No client shows it. When a server goes down, the only sign is a failing tool call in a thread.

## Decisions

**1. `umb mcp` mirrors `mcp.server.*` one to one, with no client-side model (DD-001).**

| `umb` | Method |
|---|---|
| `umb mcp add <name> --stdio -- <command> [args…]` | `mcp.server.add {name, transport:"stdio", command, args}` |
| `umb mcp add <name> --http <url>` | `mcp.server.add {name, transport:"http", url}` |
| `umb mcp list [--json]` | `mcp.server.list`, printing name, transport, trust, state and last error |
| `umb mcp remove <name>` | `mcp.server.remove {name}` |

The command also follows these rules:

- **`--env NAME=<ref>`** can be repeated, and fills `env_refs`. `umb` accepts the two reference
  forms §5.27 accepts: `keyring:<path>`, and `env:<VAR>`, which the daemon admits only under the
  keyring fallback of REQ-SEC-012 and otherwise refuses with `CONFIG_INVALID`. Anything else is a
  plaintext value.
- **A plaintext value is refused by `umb`** before anything is sent, with the REQ-SEC-004 message.
  The daemon still refuses it too (`CONFIG_INVALID`). The CLI check is only there so a secret
  typed on the command line is never written to the socket.
- **`--trusted` is not offered.** A server added from the CLI is always `untrusted`, the default
  of §5.27 and REQ-MCP-004. Trusting a server is a decision about the policy engine, and it
  belongs where policies are reviewed, not in a flag that can end up in a copied script.
- `--json` and REQ-CLI-004's exit codes apply, as for every `umb` command.
- **The agent cannot add servers for itself.** `umb mcp add` joins the destructive-pattern list
  of Tech §5.3, beside `umb skill install` (delta `2026-09-skills-cli`). If the agent types it,
  it always asks and `always` is ignored.

**2. API §2's `cli` row gains `mcp.server.list`, `mcp.server.add` and `mcp.server.remove`.** It
does not gain `mcp.*` as a wildcard, so any later MCP method has to be allowed for the CLI
explicitly.

**3. The TUI shows MCP servers and skills in one place in the agent panel.** Two things are
shown:

- **Everything the agent can reach, beyond its built-in tools:**
  - MCP servers, with their state live from `mcp.server_state`;
  - skills, enabled or disabled, kept current by `skill.changed`.
- **For each turn, what it used:** the MCP tools it called, and the skills it loaded through
  `skill_load`.

The data is all there already: calls are rows in `tool_calls`. The view is read-only. Adding or
removing is done with `umb`, or later through a TUI action, but not in this delta.

## Specification changes

- **PRD §6.8** gains:
  - **REQ-CLI-008** · MUST · event — WHEN `umb mcp add`, `umb mcp list` or `umb mcp remove` runs, THE SYSTEM SHALL invoke the `mcp.server.*` method of the same name and print its result, in JSON with `--json`, with REQ-CLI-004's exit codes; a server added this way is always `untrusted`, and a plaintext `--env` value SHALL be refused before anything is sent.
  - **REQ-TUI-004** · MUST · state — WHILE the agent panel is open, THE SYSTEM SHALL show the configured MCP servers with their current state and the installed skills with whether they are enabled, and, for each turn, which MCP tools were called and which skills were loaded.
- **API §2**, the `cli` row as in decision 2.
- **API §5.27** gains the `remove` params, `{name}`, and `list`'s, `{}`. Until now only `add`'s
  params were written.
- **Versions** are assigned in the order deltas are ratified. After the other three, this is PRD
  1.16, API 1.18 and Tech 1.16.
- **Tech Design §9.4**, `umb mcp`.

## Verification

- `TestMcpCommandsMirrorTheMethods_REQ_CLI_008`: every row of decision 1, against a recording
  daemon. `keyring:` and `env:` references pass through; a plaintext `--env` exits 1 and sends
  nothing.
- The destructive-pattern case for `umb mcp add` is in `TestASkillCannotRunOrWidenAnything_REQ_SKL_005`
  (delta `2026-09-skills-cli`, T-F1-36), which owns the pattern list. If that delta is not
  ratified, T-F1-37 adds the pattern and the case itself.
- `TestCliMayManageMcpServers_REQ_CLI_008`: the `cli` kind may call the three methods and still
  gets `METHOD_NOT_FOUND` for `thread.get` and `approval.respond`, which its row does not name.
- `TestAgentPanelShowsServersAndSkills_REQ_TUI_004` (teatest): a server going `unavailable` and a
  skill being disabled are both reflected, and a turn that called an MCP tool and loaded a skill
  lists both.

## Phase

**F1.** `T-F1-37` depends on T-F1-17 (MCP client), T-F1-20 (the agent panel) and T-F1-35
(`umb skill`, so both commands share one idiom). Versions are assigned in the order deltas are
ratified. On ratification, the task, the matrix rows and the plan row go into the F1 files.
