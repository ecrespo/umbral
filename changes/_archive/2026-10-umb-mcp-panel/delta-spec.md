# Delta — `umb mcp`, the `cli` kind's MCP methods, and the agent panel's extensions view

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-18` by the implementing agent under the Tech Lead's standing instruction for F1; **pending the Tech Lead's review**. Folded into API 1.40, Tech Design 1.52 and the F1 tasks file |
| **Date** | 2026-10-18 |
| **Tasks** | T-F1-37 |
| **Builds on** | `2026-09-cli-mcp` (ratified 2026-09-26) decisions 1–3; `2026-10-mcp-client` (T-F1-17); `2026-10-skill-catalog` (T-F1-36) |

## Evidence

`2026-09-cli-mcp` fixes the commands, that a server added from the CLI is untrusted, that a
plaintext `--env` is refused before anything is sent, the `cli` row's three methods, and what
the panel shows. Writing T-F1-37 needed answers on how the CLI keeps a refused value out of its
own error output, what it does with combinations §5.27 refuses, how `list` prints, and how the
panel learns of a change it is not told about.

## Decisions

1. **`trust: "untrusted"` is sent explicitly** by every `umb mcp add`, rather than left to
   the daemon's default, and there is no flag to change it. A change of default on the daemon
   side cannot make the CLI add a trusted server.
2. **`--env` is taken out of the arguments before the flag parser runs.** Go's `flag` package
   quotes an invalid value in its error, and the value refused is the one that may be a
   secret; the error names the variable and the rule, never the value. `keyring:<path>` and
   `env:<VAR>` with something after the prefix are the only accepted forms; whether an `env:`
   reference is acceptable is still the daemon's decision (§5.27, REQ-SEC-012).
3. **What §5.27 refuses, the CLI refuses first:** a `stdio` server needs a command after
   `--`, an `http` server takes none and no `--env`, exactly one transport is named, and
   `list` and `remove` take no `--env`. Each
   such mistake exits 1 without connecting, so a malformed line never reaches the socket.
4. **`list` prints `name  transport  trust  state[  last_error]`,** two spaces apart, with
   every field escaped as `umb skill` escapes bundle text (Tech §9.4): a server's name is
   bounded by §5.27, but its last error is text the remote side chose.
5. **The panel lists again instead of patching.** `mcp.server_state` and `skill.changed`
   trigger `mcp.server.list` and `skill.list`; the daemon stores before it publishes (DD-007),
   so the listing sees the change. `mcp.server.remove` publishes nothing, so every opening of
   the panel lists too — ctrl+space, attaching a block, an approval arriving — and so does the
   TUI's start. A daemon without the module answers
   `NOT_IMPLEMENTED`, drawn as `none` rather than as an error.
6. **The view is under the header, at most two rows per list,** `mcp:` and `skills:`, broken
   between items so a name is never split from its state; what does not fit is counted as
   `… n more` on the second row, so 64 skills cannot push the transcript off the panel. Every
   name, state and error is made one line — sanitized, then every run of whitespace, newlines
   included, one space — and a server's last error is cut to 40 runes after that. It is
   read-only.
7. **A turn's use is one row at its end,** `mcp tools: … · skills loaded: …`, before
   `turn ended`, for the panel's own thread only. An MCP tool counts as called when its call
   reached the server — status `ok` or `error`; a call `denied_by_user`, `denied_by_policy` or
   `invalid_args` was not. A skill counts as loaded only when its `skill_load` call ended `ok`,
   named by the call's `args.name`, which `thread.tool_call` already carries: a failed call
   may name a skill that does not exist. Each appears once, in the order first used; a turn
   that used neither adds no row.

## Open for the Tech Lead

Two findings from an automated security review of T-F1-36, recorded rather than acted on:

- **The catalog's descriptions reach every turn's system prompt without tainting it.** The
  user saw each description in the manifest and confirmed it by digest at install, which is why
  `2026-09-skills-cli` decision 5 taints only `skill_load` output; tainting every turn with an
  enabled skill would leave taint permanently on. A description written to instruct the model
  is still the bundle author's text in Umbral's own prompt. Options: a delimited untrusted
  block, names only with descriptions served through a tainting tool, or accepting the
  confirmation as the control.
- **`umb skill install|enable|remove` and `umb mcp add` are guarded by command-line
  patterns.** The approval they force is out of band — the agent's PTY cannot give it — but a
  pattern can be dodged by forms the normaliser does not see (`2026-10-skill-catalog`'s open
  list). Refusing those methods in the daemon on a connection that comes from an agent's own
  terminal, for example by a token the thread's PTY does not inherit, would close it there.

- **`umb mcp remove` is not a destructive pattern.** This task opens `mcp.server.remove` to
  the `cli` kind, so an agent's `run_command` in a permissive mode could remove the user's
  MCP servers; the ratified list (`2026-09-skills-cli` decision 6) names only `umb mcp add`.
  Adding `umb_mcp_remove` beside it is a one-line change to the built-in list.

## Specification changes

- **API 1.40:** §2's `cli` row gains `mcp.server.list`, `mcp.server.add` and
  `mcp.server.remove`; §5.27 says so and that `umb mcp add` sends `trust: "untrusted"`; the
  pending-from-F1 note is removed. `remove`'s and `list`'s params were written by T-F1-17.
- **Tech Design 1.52:** §9.4 writes `umb mcp` (decisions 1–4); §5.3e gains the extensions
  view (decisions 5–7); the pending-from-F1 note is closed.

## Verification

| Test | What it proves |
|---|---|
| `TestMcpCommandsMirrorTheMethods_REQ_CLI_008` | decisions 1–4: `add --stdio` and `add --http` send §5.27's params with `trust: "untrusted"` and the `env_refs`; plaintext, malformed and empty references, a trust flag, no or both transports, a stdio server without a command, an http server with one or with `--env`, and wrong arities exit 1 sending nothing and never echoing the value; `list` and `--json` print as decision 4 says; an unknown `remove` exits 1 |
| `TestEnvIsTakenOutBeforeTheFlags_REQ_CLI_008` | decision 2: a trailing `--env` is an error, `-env` and `--env=` are read, nothing after `--` is |
| `TestCliMayManageMcpServers_REQ_CLI_008` | the `cli` kind reaches the three methods; `thread.get` and `approval.respond` stay `METHOD_NOT_FOUND` |
| `TestAgentPanelShowsServersAndSkills_REQ_TUI_004` | decisions 5–7: servers with state and skills on or off on opening; a server going `unavailable` with its error and a skill disabled after the notifications; a turn's MCP tools (`ok` or `error`) and loaded skills (`ok`) once each, not a refused or malformed call, a failed `skill_load`, a built-in tool or another thread's; a turn that used nothing adds no row; reopening drops a removed server |
| `TestTheExtensionsViewStaysInItsRows_REQ_TUI_004` | decisions 5 and 6: a last error holding a newline and an escape stays one row without either; 64 skills take two rows ending `… n more`; attaching a block and an approval arriving each list again |
| `TestToEventMapsTheExtensionsNotifications_REQ_TUI_004` | decision 5 and 7 in the adapter: the two notifications, `skill_load`'s `args.name`, and another tool's `name` argument not taken for a skill |
| `TestADaemonWithoutTheModuleListsNothing` | decision 5: `NOT_IMPLEMENTED` is an empty list, any other error is not |
