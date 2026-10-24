# Delta — the ACP client: manifest, launch, session, prompt

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-23 by the implementing agent under the Tech Lead's standing F2 instruction`; folded into PRD F2 0.6 (REQ-ACP-009), API 1.48 (§3, §4, §5.19, §5.20, §6) and Tech 1.63 (§3.2, §5.1, §5.2, §5.3f, DD-012) |
| **Date** | 2026-10-23 |
| **Task** | T-F2-07 |
| **REQ** | REQ-ACP-001, REQ-ACP-002, REQ-ACP-006, REQ-ACP-009 |

## What the PRD and the task left open

REQ-ACP-001 says the agent is launched when the thread is created, but not what happens when it
does not start after the thread and its worktree are written. REQ-ACP-009 names OpenCode's
permissions and its repository files, but not how the generated configuration reaches the agent,
how it learns its model once the user's global configuration is out of reach, or how far up a
repository's configuration is looked for. REQ-ACP-006 names three failures, and the SDK does not
report the second: it logs a line that is not JSON-RPC and reads on. Nothing said what an ACP
tool call's row is named, or how the Mediator behaves before T-F2-08.

## Decisions

1. **A thread with `agent` is checked before anything is written.** An unknown entry
   (`unknown_agent`), one whose manifest Umbral does not have (`no_manifest`), a `config` key the
   manifest does not allow (`config_key`) and a repository configuration in `cwd` or above it
   (`project_config`) are `VALIDATION_ERROR` with that `data.reason`. `model` or `model_class`
   with `agent` is `VALIDATION_ERROR`: a hosted thread's model is its agent's.
2. **A thread whose agent does not start is kept.** Once the thread (and its worktree) are
   written, the agent is launched in the turn's directory with `initialize` and `session/new`.
   If that fails, `thread.create` still answers the thread, with `agent_unavailable: {reason,
   message}` — `project_config` when the worktree holds the agent's configuration, `launch_failed`
   otherwise — and the next `thread.send` launches it again, as after a crash. A thread is not
   taken back after its worktree exists, and the same send-time relaunch already covers it.
   This changes REQ-ACP-009's response for a configuration found only in the new worktree, so
   PRD F2 0.6 amends it: `VALIDATION_ERROR` before anything is written, `agent_unavailable`
   after the worktree is made, `agent_error` at a later launch.
3. **Every launch checks again.** The worktree comes from the same repository, and the agent
   itself could write `opencode.json` between two launches; a launch over one is refused, and the
   turn ends `agent_error`.
4. **A repository configuration is looked for up to the repository's top.** OpenCode reads
   `opencode.json`, `opencode.jsonc` and `.opencode/` from the directory up to the git top, so
   the check walks up to the nearest directory holding a `.git` git would take for one — a
   gitdir (`HEAD`, and `objects` and `refs` there or in the directory its `commondir` names, as
   a linked worktree's), a symlink to one, or a `gitdir:` file naming one — or
   to `/` outside a repository. A `.git` git would refuse does not stop it, since the agent,
   asking git for its top, reads on past it.
5. **The launch directory is where Umbral's turn would run:** the active worktree's directory
   matching `cwd` (the worktree's top when `cwd` is the repository's), or `cwd` itself, which is
   then the write root — not the repository around it — as REQ-ACP-001 says.
6. **The OpenCode manifest.** The generated `opencode.json` sets `"*"` first and then `edit`,
   `bash`, `webfetch`, `websearch`, `external_directory` and `task` to `ask` (OpenCode's last
   matching rule wins, so the wildcard comes first), `mcp: {}`, `plugin: []`, `autoupdate: false`
   and `share: "disabled"`. It is written to `<isolation>/config/opencode/opencode.json`, which is
   both the isolated global configuration and the file `OPENCODE_CONFIG` names.
7. **An entry's `config` carries the model.** With the user's global configuration out of reach,
   an entry says how to reach a model through `[acp.agents.config]`, limited to the keys the
   manifest allows (for OpenCode `model`, `small_model`, `provider`, `enabled_providers`,
   `disabled_providers`) and merged under the manifest's own keys. A credential goes through
   `env_refs` and OpenCode's `{env:VAR}`, never into the file.
8. **The isolation** is `acp/<thread_id>/` of the daemon's runtime directory, made `0700` afresh
   at every launch and removed when the process ends, with `home`, `config`, `data`, `cache` and
   `state`. The environment is `PATH`, `LANG`, `HOME` and `XDG_CONFIG_HOME`, `XDG_DATA_HOME`,
   `XDG_CACHE_HOME`, `XDG_STATE_HOME` of the isolation, `OPENCODE_CONFIG`, and the entry's
   resolved `env_refs`, which may not name `PATH`, `HOME`, `LANG`, `XDG_*` or `UMBRAL_*`. They
   resolve as an MCP server's do: the keyring, or `env:` under REQ-SEC-012's fallback.
9. **`[acp]` and `[[acp.agents]]` are read with `go-toml`.** The subset parser refuses arrays and
   tables, so their lines are split out — each leaving a blank line behind, so errors keep the
   file's line numbers — and decoded strictly. `silence_timeout` is 5–86400 s (600),
   `max_terminals` 1–32 (4).
10. **An invalid line ends the connection.** A line on the agent's stdout that is not a JSON-RPC
    2.0 object naming a method or an id, or one over 16 MiB, ends the turn `agent_error` at once,
    read by a filter between the pipe and the SDK.
11. **The silence clock restarts while waiting.** The silence timer counts from the agent's last
    line; while one of its requests of Umbral is unanswered, or an approval of its thread is
    pending, the clock is held, so a full window starts again when the wait ends.
12. **Each failure kills the process.** On `agent_error` the process group is killed and the
    isolation removed, so the next send relaunches it. A relaunch opens a new ACP session; the
    agent does not remember earlier turns (`session/load` is not used in F2a).
13. **A hosted tool call's row** is `tool = acp:<kind>` — a closed set, never the agent's free-text
    title — `args = {title, raw_input?}`, the kind's REQ-ACP-003 risk, `via = acp` and
    `decision = unmediated`, `pending` until the agent reports it `completed` (`ok`) or `failed`
    (`error`); a late update does not reopen an ended call. Umbral's own calls now record their
    `decision` too (`allow`, `deny`, `ask`), so `NULL` stays "a row from before 0007".
14. **Until T-F2-08 the mediator refuses.** A permission is answered with the agent's `reject_once`
    option — never `allow_always`, and cancelled when it offers no option of that kind — and
    `fs/*` and `terminal/*` with an error. `initialize` still advertises files and terminals,
    which T-F2-08 serves; a hosted agent can talk but not act in between.
15. **Cancel.** A cancelled turn sends `session/cancel` and kills the process if the turn has not
    ended 5 s later; the terminals' 500 ms and the traces are T-F2-09's. The daemon closes the
    runtime before the host, so a shutdown ends a hosted turn `cancelled`, not killed.
16. **Stop reasons:** `end_turn` and `refusal` → `end_turn`, `max_tokens` → `budget`,
    `max_turn_requests` → `max_steps`, `cancelled` → `cancelled`, anything else `agent_error`.
17. **The agent's stderr is discarded.** It holds the `env_refs`' credentials, and a provider's
    error often prints the key it refused; like an MCP server's, it reaches no log and no
    `agent_unavailable.message` (Art. 7). For the same reason an error the agent answers a
    request with — `initialize`, `session/new` or `session/prompt` — is logged and reported by
    its JSON-RPC code alone, never its message or data.
18. **A manifest reserves the agent's own configuration variables.** OpenCode reads
    `OPENCODE_CONFIG_CONTENT`, `OPENCODE_CONFIG_DIR`, `OPENCODE_PERMISSION` and more, any of which
    would set a permission around the generated file. An `env_refs` key with a manifest's prefix
    (`OPENCODE_`) stops the daemon at start.
19. **A hosted call's kind is ACP's closed set.** The SDK passes any string; a kind outside API
    §4's nine, `switch_mode` included, is `other`, and its risk `Exec`.
20. **A hosted call stays `unmediated` in F2a** even when its permission was refused: the
    refusal is not tied to a row until T-F2-08 decides requests.
21. **What a hosted turn sends.** The prompt is rendered as Umbral's own: an attachment is read
    by Umbral and its text goes to the agent, whose model call Umbral neither sees nor redacts
    (`unobserved`). The handshake's deadline is `silence_timeout`, so `thread.create` can wait
    that long for an agent that starts and never answers. The process runs until it fails, its
    launch directory changes or the daemon stops. An answer read just before the process exits
    ends the turn normally; an exit waits 200 ms for it.

## Open

- A refusal at relaunch (decision 3) ends the turn `agent_error` with the reason in the log only;
  the client sees no message saying why. Worth a `system_note` once T-F2-09 discloses at creation.

## Tests

- `internal/acp/adapters/sdk`, against `acptest`, the test binary re-executed as an ACP agent
  (selected by its first argument, since the environment is an allowlist):
  `TestAnACPAgentCrashEndsTheTurn_REQ_ACP_006`, `TestAnInvalidACPMessageEndsTheTurn_REQ_ACP_006`,
  `TestASilentACPAgentEndsTheTurn_REQ_ACP_006`, `TestAnAgentWaitingOnUmbralIsNotSilent_REQ_ACP_006`,
  `TestAPendingApprovalPausesTheSilenceTimer_REQ_ACP_006`,
  `TestAnAgentWithoutAManifestIsRefused_REQ_ACP_009`, `TestTheACPEnvironmentHoldsNoSecrets_REQ_ACP_009`,
  `TestTheOpenCodeManifestAsksForEveryPermission_REQ_ACP_009`,
  `TestARepositoryAgentConfigIsRefused_REQ_ACP_009`, `TestTheUsersGlobalAgentConfigIsNotRead_REQ_ACP_009`,
  `TestAnACPAgentIsLaunchedInItsDirectory_REQ_ACP_001`, `TestAnACPTurnStreamsItsReports_REQ_ACP_002`
  and `TestACancelledACPTurnSendsSessionCancel_REQ_ACP_008`.
- `cmd/umbrald`, against a real daemon whose `[[acp.agents]]` launches the test binary:
  `TestAnACPAgentIsLaunchedInItsWorktree_REQ_ACP_001`,
  `TestAnACPThreadWithoutAWorktreeRunsInItsDirectory_REQ_ACP_001` (with `project_config` and
  `unknown_agent` on the wire), `TestAnACPThreadStreamsLikeAnyOther_REQ_ACP_002` and
  `TestAnACPAgentErrorEndsTheTurnAndKeepsTheThread_REQ_ACP_006`.
- `cmd/umbrald`, too: `TestAnACPAgentThatDoesNotStartKeepsItsThread_REQ_ACP_001` (an agent whose
  first launch exits: `agent_unavailable` with `launch_failed`, then a send relaunches it).
- `internal/acp/domain`: `TestAHostedToolCallIsNamedFromACPsKinds_REQ_ACP_002`.
- `internal/workspaces/integration`: `TestAnACPThreadIsItsPanesStateAuthority_REQ_ACP_002`, which
  pins DD-012's resolver for a hosted thread's pane; no hosted thread owns a pane session before
  T-F2-08's terminals, so it does not exercise this task's code.
- `internal/config`: `TestTheACPAgentsAreRead_REQ_ACP_001`, `TestABadACPEntryStopsTheDaemon_REQ_ACP_009`.
- Twenty-two mutations each reddened one of these — after spec-guardian, the agent's stderr in
  its error, `OPENCODE_*` env refs accepted, an agent's own kind kept, and the thread dropped when
  its agent does not start, besides the nineteen first ones: no silence check, no outstanding-request check, no
  pending-approval check, the whole daemon environment inherited, no frame check, no relaunch
  check, `bash` dropped from the permissions, an MCP server declared, any `config` key accepted,
  `HOME` not isolated, a missing manifest accepted, an exit not ending the turn, the agent run in
  the main copy, the repository root taken for `cwd`, no check at creation, no text delta, no
  appended text, `agent_error` reported as `provider_error`, and the external turn skipped.
