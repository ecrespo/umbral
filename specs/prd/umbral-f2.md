# Umbral F2 — the agentic development environment

## Product Requirements Document (PRD)

| Field | Value |
|---|---|
| **Author** | Ernesto Crespo (Tech Lead) · assisted draft |
| **Status** | `RATIFIED` 2026-10-22 by the Tech Lead (PR #32), with Q-F2-01…05, 08 and 09 and the Analyze decisions on Art. 4, Art. 5 and `full-auto`. Slice F2a implemented and closed on 2026-10-24 (delta `2026-10-f2a-close`); F2b … F2f not started |
| **Version** | 0.9 |
| **Date** | 2026-10-22 |
| **Reviewers** | spec-guardian (Analyze and two verify passes); Tech Lead, ratified 2026-10-22 |
| **Source** | `docs/ARCHITECTURE.md` §5.4–5.8 and §10 (F2 ADE row); `specs/prd/umbral-mvp.md` §5.2–5.3; Plan 1.14 §4 |
| **Builds on** | PRD MVP 1.21, API 1.41, Tech 1.55, Data Model 1.23, constitution 1.3 |

---

## 1. Executive Summary

The MVP (F0 + F1) gave Umbral a durable terminal, semantic blocks and **one** agent of its own that
fixes an error offline. F2 turns it into an agentic development environment: **several agents work
at once, each in its own git worktree, one of them a third-party agent (Claude Code, Gemini CLI,
Codex, Goose, OpenCode) hosted through ACP, under the same approvals, audit and traces as Umbral's
own.** Around that core, F2 adds the desktop client, the agent driving interactive programs, the
proactive suggestions of an embedded model, sessions that survive a daemon upgrade and an SSH hop,
Windows, and declarative plugins.

Success is measured by the exit criterion of `docs/ARCHITECTURE.md` §10: **three agents in parallel,
one external through ACP, without interference, with a trace per turn** — offline, with local
models for Umbral's own agents.

F2 is too large for one batch of work. §8 splits it into six slices, each closed by its own
criterion; slice F2a alone meets the phase's exit criterion.

## 2. Context and Problem

### 2.1 Current situation

After F1 a developer can run one Umbral thread per directory. Two threads on the same repository
write the same files; nothing isolates them. A third-party agent started in a pane is an opaque
process: Umbral sees its output and, at best, an external `pane.report_state`, but neither its
permission requests nor its file writes go through Umbral's policy engine or `egress_log`.
`full-auto` mode exists in the policy engine but Art. 5 forbids it outside a sandbox or disposable
worktree, which the MVP does not have.

### 2.2 Problem

The developer who wants to run "Claude Code on the refactor, a local model on the flaky test, and a
second local thread on the docs" today juggles three terminals, three working copies by hand, three
permission models and no shared audit trail.

### 2.3 Opportunity

ACP (agentclientprotocol.com) is the open protocol through which an editor hosts an external agent:
the agent can ask the host to grant permissions, read and write files and create terminals. An ACP
agent still runs its own tools in its own process, and asks only what it is configured to ask (spike
T-F2-00, `docs/spikes/2026-10-acp-opencode.md`). Launched by Umbral with a configuration that makes it
ask before every side effect, a third-party agent sits behind the same policy engine, approval
queue and traces as Umbral's own, without Umbral reimplementing it (Art. 8). What it does not ask —
its reads and its own model calls — is disclosed, not audited.

## 3. Target Users

The two MVP personas, plus:

### Persona 3: Multi-agent developer
- **Description:** uses two or three coding agents a day, local and remote, often on the same repo.
- **Main need:** run them side by side without them overwriting each other, see at a glance which one
  needs a decision, and review each one's diff before it lands.
- **Usage frequency:** daily.
- **Technical level:** high.

## 4. Goals and Success Metrics

| Goal | Metric | Target | Slice |
|---|---|---|---|
| Parallel agents without interference | Reference scenario US-F2-001 (§9): three threads, one ACP, on one repo | 3/3 finish; 0 files changed outside each thread's worktree; each diff merges cleanly or reports its conflict | F2a |
| Every side effect of a hosted agent decided by Umbral | Writes, commands and fetches of an ACP thread performed without a `session/request_permission` decided by Umbral's policy engine, in US-F2-001 | 0 | F2a |
| Traces for every agent | Turns, own or ACP, without an OTel trace | 0 | F2a |
| Interactive programs | US-F2-002: the agent answers a `psql` prompt and a `git rebase -i` editor through `terminal_interact` | ≥ 70 % over 10 runs with `gpt-oss:20b` | F2b |
| Proactive help stays local | Active AI suggestions produced by a remote model | 0 | F2b |
| Upgrades keep work alive | Live panes surviving a daemon upgrade with handoff | 100 % on Linux and macOS | F2d |

## 5. Scope

### 5.1 In scope (F2)
- [ ] Worktrees as a daemon primitive: create, list, diff, merge, discard, with provenance.
- [ ] Parallel threads, each with its own worktree as write root, and a concurrency limit.
- [ ] `full-auto` mode, only inside a thread's disposable worktree (Art. 5).
- [ ] ACP client: external agents launched by Umbral with a configuration that makes them ask before every side effect; their permissions, file writes and terminals decided by Umbral's policy engine.
- [ ] Third-party agent detection by declarative manifest, with local override and `umb agent explain`.
- [ ] Policy router: capability filter, local-first / cost / quality policy, per-provider circuit breakers.
- [ ] Full Terminal Use: the `terminal_interact` tool and the input ownership lock with take-over.
- [ ] Embedded model through yzma for the `fast` class.
- [ ] Active AI: one-line suggestions on failed blocks from a local model.
- [ ] Wails v3 desktop client: panes, blocks, threads, per-hunk diff approval, file tree.
- [ ] Live PTY handoff between daemon versions.
- [ ] Remote daemons over SSH and federation of several machines in one client.
- [ ] Windows: ConPTY, named pipe, PowerShell integration.
- [ ] Plugins with a declarative manifest; pane graphics.
- [ ] The packaging requirements reserved for F2: REQ-PKG-004, 005, 007.

### 5.2 Out of scope (F3 or never)
- Background agents with triggers (cron, webhooks, files, CI) and containers: F3.
- ACP **server** and MCP **server** (Umbral as an agent or tool provider for others): F3.
- WASM plugins, workflows and notebooks: F3.
- Pushing branches or opening pull requests on the user's behalf: F3, with the background agents.
- Session sharing and real-time collaboration: outside the product.

### 5.3 Future considerations
- Semantic code index (tree-sitter + embeddings): moved from F2 to F3 unless §10 Q-F2-07 decides
  otherwise; nothing in F2's exit criterion needs it.

## 6. Functional Requirements (EARS)

Format as in the MVP PRD: **ID** · priority · EARS pattern — criterion. New prefixes: `WT`
(worktrees), `ORC` (parallel threads), `ACP`, `XAG` (third-party agents), `RTR` (router), `FTU`
(Full Terminal Use), `AAI` (Active AI), `DSK` (desktop), `HND` (handoff), `RMT` (remote and
federation), `WIN` (Windows), `PLG` (plugins and graphics). Existing prefixes continue their
numbering.

### 6.1 Worktrees (WT) — F2a

- **REQ-WT-001** · MUST · event — WHEN a client invokes `worktree.create` for a thread whose cwd is inside a git repository, THE SYSTEM SHALL create a git worktree on a new branch `umbral/<thread_id>` from the repository's current `HEAD` (or the given `base`), under `worktrees/<repo_id>/<thread_id>` beside the daemon's database — `$XDG_DATA_HOME/umbral/worktrees/<repo_id>/<thread_id>` for the default one — outside the repository, so neither the repository's status nor its tools see it; `<repo_id>` is the first 16 hex digits of the SHA-256 of the real path of `git rev-parse --git-common-dir` — and record its repository, base commit, branch, path, thread and creation time.
- **REQ-WT-002** · MUST · state — WHILE a thread has a worktree, THE SYSTEM SHALL use the worktree as the thread's cwd and write root, so that the policy engine treats every write outside it as outside the write root (REQ-AGT-013).
- **REQ-WT-003** · MUST · event — WHEN a client invokes `worktree.diff`, THE SYSTEM SHALL return the unified diff between the worktree's base commit and its working tree, including untracked files, split into files and hunks with stable hunk ids.
- **REQ-WT-004** · MUST · event — WHEN a client invokes `worktree.merge`, THE SYSTEM SHALL squash the worktree's changes into one commit on the branch the worktree was created from, with a generated message the request may replace, in the user's main working copy, running the repository's own commit hooks and signing configuration as `git commit` would, and without checking out any branch. IF the main working copy has uncommitted changes, is on a different branch than the worktree's origin, or is on a detached `HEAD`, THEN THE SYSTEM SHALL refuse with `CONFLICT` and `data.reason` (`dirty`, `other_branch`, `detached`) and change nothing. IF the squash conflicts, THEN THE SYSTEM SHALL abort it, leave both the main working copy and the worktree as they were, and reply `CONFLICT` with `data.reason = conflict` and `data.paths`. THE SYSTEM SHALL run that commit with no terminal (standard input closed, `GIT_TERMINAL_PROMPT=0`) and at most `[worktrees] commit_timeout` (default 60 s); IF a hook rejects the commit, signing fails or asks for a passphrase it cannot get, or the timeout passes, THEN THE SYSTEM SHALL leave both the main working copy and the worktree as they were and reply `CONFLICT` with `data.reason = commit_failed` and the first 4 KiB of git's error output.
- **REQ-WT-005** · MUST · event — WHEN a client invokes `worktree.discard`, THE SYSTEM SHALL require `confirm: true`, remove the worktree directory and its branch, and keep the worktree record with `state = discarded`.
- **REQ-WT-006** · MUST · unwanted — IF a worktree's directory or branch was removed or moved outside Umbral, THEN THE SYSTEM SHALL mark it `state = missing` at the next start or access, and SHALL NOT recreate it or touch any other branch.
- **REQ-WT-007** · MUST · event — WHEN a client closes a group of threads (`worktree.close_group` with a workspace — the threads whose `workspace_id` is that workspace — or a list of threads), THE SYSTEM SHALL report each worktree with its `state` (`active`, `merged`, `discarded`, `missing`) and, for an `active` one, whether it holds changes not merged (`changed`) or none (`clean`), and SHALL discard only the `active` worktrees the request names, so that closing a group never loses an unmerged change silently. A `missing` worktree is reported and has nothing to discard.
- **REQ-WT-008** · MUST · unwanted — IF the target repository is not a git repository, is a bare repository, or its git is older than 2.34, THEN THE SYSTEM SHALL refuse `worktree.create` with `VALIDATION_ERROR` naming the reason, and the thread SHALL keep working without isolation.

### 6.2 Parallel threads (ORC) — F2a

- **REQ-ORC-001** · MUST · ubiquitous — THE SYSTEM SHALL run turns of different threads concurrently, each with its own PTY, write root, approvals and token budget, and SHALL serialize turns only within one thread.
- **REQ-ORC-002** · MUST · unwanted — IF starting a turn would exceed `[agents] max_concurrent_turns` in `config.toml` (default 3), THEN THE SYSTEM SHALL accept `thread.send`, hold the turn with `queued = true` in `thread.get` and attention `working`, and start it when a slot frees, in arrival order. A second `thread.send` to that thread SHALL get `CONFLICT` as for a running turn; `thread.cancel` SHALL drop the held turn with `stop_reason = cancelled`; a held turn interrupted by a daemon stop SHALL be recovered `stopped` like a running one (REQ-AGT-017).
- **REQ-ORC-003** · MUST · state — WHILE several threads are working, THE SYSTEM SHALL show in the TUI's agent panel (and the desktop client) every thread of the workspace (the threads whose `workspace_id` is the focused workspace; `thread.create` takes the workspace, and defaults to the one owning the creating pane) with its attention state, its worktree and its pending approvals, so that the user sees which one needs a decision without opening each.
- **REQ-ORC-004** · MUST · state — WHILE a thread is in `full-auto` mode and its worktree is `active`, THE SYSTEM SHALL allow without approval `WriteFS` tools whose target is inside the worktree and `Exec` tools whose cwd is inside it (Art. 5's disposable worktree), SHALL keep `ask` for `Network` tools, for REQ-SEC-005's destructive list and under REQ-SEC-006's taint, and SHALL refuse to enter `full-auto` with `VALIDATION_ERROR` for a thread without an active worktree. This applies to an ACP thread's permission requests too (REQ-ACP-003), where an `execute` request's cwd is the one it names or, if none, the agent's session directory. WHEN the worktree leaves `active` (merged, discarded, missing), THE SYSTEM SHALL clear `full-auto`, leaving the thread in the mode it held under it (`ask`, `normal` or `auto-edit`).
- **REQ-ORC-005** · MUST · unwanted — IF two threads' local-model requests compete for one local provider, THEN THE SYSTEM SHALL queue them per provider (a `max_parallel` key in that provider's `[[providers]]` entry of `models.toml`, default 1 for local providers) rather than send them concurrently, and SHALL count queue time separately from `first_token_timeout`.

### 6.3 ACP client (ACP) — F2a

An ACP agent runs its own tools in its own process, and asks the client only what its configuration
makes it ask (spike T-F2-00). So Umbral:
- launches every hosted agent with a configuration that turns each side effect into a
  `session/request_permission`;
- decides those requests with the policy engine;
- discloses what still escapes it.

- **REQ-ACP-001** · MUST · event — WHEN a client creates a thread with `agent = <name>` naming an entry of `[[acp.agents]]` in `config.toml` (command, args, env references), THE SYSTEM SHALL launch that command in the thread's worktree — or, for a thread without one (none was asked for, or REQ-WT-008 refused it), in the thread's directory, which is then its write root — with the environment REQ-ACP-009 defines, complete the ACP `initialize` handshake advertising Umbral's file-system and terminal capabilities, and open an ACP session for the thread.
- **REQ-ACP-002** · MUST · event — WHEN the user sends a message to an ACP thread, THE SYSTEM SHALL forward it as `session/prompt` and map the agent's `session/update` notifications — message and thought chunks, tool calls and their updates — onto the thread's existing notifications and persisted messages, so that an ACP thread is read, waited on and attended like any other (REQ-AGT-011, REQ-AGT-016). Plans and command lists are not mapped in F2. An ACP thread is the level-1 state authority (DD-012) of the pane that owns it, exactly as Umbral's own thread is; its terminals (REQ-ACP-005) are sessions outside the workspace tree.
- **REQ-ACP-003** · MUST · event — WHEN an ACP agent sends `session/request_permission`, THE SYSTEM SHALL decide the action with the policy engine and, for an `ask`, emit `approval.requested` in the same queue as Umbral's own agent:
  - a tool call of kind `edit`, `delete` or `move` is `WriteFS` on its `locations`;
  - `execute` is `Exec` on its command;
  - `fetch` is `Network` on its URL;
  - `read`, `search` and `think` are `ReadOnly`;
  - any other kind is `Exec`, so an unknown kind is never decided more loosely than a command.

  THE SYSTEM SHALL answer the agent with its `allow_once` option for an allow and its `reject_once` option for a deny, and SHALL NEVER select an `allow_always` option. An allowed `fetch` is made by the agent, not by Umbral, so it is recorded on the thread's `tool_calls` row with its URL (REQ-ACP-007) and not in `egress_log` (Art. 4 amendment of 2026-10-22).
- **REQ-ACP-004** · MUST · event — WHEN an ACP agent asks to read or write a file (`fs/read_text_file`, `fs/write_text_file`), THE SYSTEM SHALL decide a write as `WriteFS` and a read inside the write root as `ReadOnly`, SHALL apply `ask` to a read outside the write root, and SHALL perform the operation itself only when allowed. A write to a path that an allowed permission of the same tool call or, lacking a tool call id, of the same turn listed in its `locations` is covered by that decision and is not decided again. Umbral records each `fs/*` and `terminal/create` request it decides as a `tool_calls` row of its own (`via = acp`), so its approval has a tool call to point at; like every ACP row, it belongs to the turn's assistant message, the one the agent's output is streamed into when the request arrives.
- **REQ-ACP-005** · MUST · event — WHEN an ACP agent asks to create a terminal (`terminal/create`), THE SYSTEM SHALL decide the command as `Exec` (with `ask` for a `cwd` outside the write root), run it in a new session owned by the thread (`owner_thread_id`) as a block with `origin = agent` and `via = acp`, and serve `terminal/output`, `wait_for_exit`, `kill` and `release` from that session. THE SYSTEM SHALL allow at most `[acp] max_terminals` (default 4) per thread and refuse the next one with an ACP error.
- **REQ-ACP-006** · MUST · unwanted — IF an ACP agent's process exits, sends a message that is not valid ACP, or sends no message of any kind for `[acp] silence_timeout` (default 600 s) while no approval of its thread is pending and no request of the agent's to Umbral (`fs/*`, `terminal/*`, a permission) is unanswered or waiting (`terminal/wait_for_exit`), THEN THE SYSTEM SHALL end the turn with `stop_reason = agent_error`, keep the thread and its history, and let the next `thread.send` relaunch the agent.
- **REQ-ACP-007** · MUST · ubiquitous — THE SYSTEM SHALL:
  - record every ACP turn as an OTel trace, with one span per agent tool call and one per mediated request;
  - record each tool call the agent reports in `tool_calls`, with `via = acp`, the risk REQ-ACP-003 maps its kind to, and its `decision`: `allow`, `deny` or `ask` with its approval when Umbral decided it, `unmediated` when the agent ran it without asking (reads, searches);
  - set the thread's `egress_visibility = unobserved`;
  - show the user, when the thread is created, that Umbral does not record the agent's own network traffic (its model calls and anything else its process sends) or its file reads, and that the agent can read the repository's main copy and every other thread's worktree.
- **REQ-ACP-008** · MUST · event — WHEN the user invokes `thread.cancel` on an ACP thread, THE SYSTEM SHALL send `session/cancel`, terminate the thread's ACP terminals within 500 ms, and terminate the agent process if it has not ended the turn within 5 s.
- **REQ-ACP-009** · MUST · ubiquitous — THE SYSTEM SHALL launch an ACP agent only through a manifest that declares how to make the agent ask before every side effect, and SHALL refuse with `VALIDATION_ERROR` an `[[acp.agents]]` entry whose agent has no such manifest. The manifest:
  - generates a configuration that sets **every** permission the agent has to `ask` — a wildcard where the agent supports one, otherwise the full list. For OpenCode that covers at least `edit`, `bash`, `webfetch`, `websearch`, `external_directory`, `task` and every MCP tool, and the generated configuration declares no MCP servers and no plugins of its own;
  - isolates the agent from configuration Umbral did not write: the process gets its own `HOME` and XDG directories under the daemon's runtime directory, so the user's global agent configuration is not read;
  - names the files through which a repository configures the agent (for OpenCode, `opencode.json`, `opencode.jsonc` and `.opencode/`). IF the launch directory or a directory above it up to the repository's top holds one, THEN THE SYSTEM SHALL refuse to launch, unless the manifest declares a mechanism that overrides it and a test proves that a repository's file setting a permission to `allow` is still asked. Found in the thread's directory before anything is written, the refusal is `thread.create`'s `VALIDATION_ERROR` with `data.reason = project_config`; found only in the worktree just made, the thread and its worktree are kept and `thread.create` answers `agent_unavailable` with `reason = project_config`; found at a later launch, the turn ends `agent_error`.

  The process's environment is an allowlist like an MCP stdio server's (Tech §6.1): `PATH`, `LANG` and the isolated `HOME` and XDG variables, the entry's resolved env references, and the generated configuration's location; no other inherited variable, no secret and no `UMBRAL_*`. An env reference may not name a variable the manifest reserves as the agent's own configuration (for OpenCode, `OPENCODE_*`), which would set a permission around the generated file; such an entry stops the daemon at start. The agent's stderr is discarded, since it can print the credentials its env references carry.

### 6.4 Third-party agents (XAG) — F2a

- **REQ-XAG-001** · MUST · event — WHEN a process in a pane matches a detection manifest (executable name, argv pattern), THE SYSTEM SHALL set the pane's `detected_agent` field to the agent's name and its declared state source (hooks, ACP or none), without deriving any semantic state from it or from the screen: under DD-012 detection labels a pane, and state still comes from the pane's own thread, then external reports, then the shell.
- **REQ-XAG-002** · MUST · optional — WHERE `$XDG_CONFIG_HOME/umbral/agents/*.toml` defines a manifest, THE SYSTEM SHALL load it over the built-in manifests by name, and an invalid file SHALL be skipped whole and reported.
- **REQ-XAG-003** · MUST · event — WHEN a client invokes `agent.explain` for a pane (`umb agent explain <pane>`), THE SYSTEM SHALL return the matched manifest, where it was loaded from, the rule that matched, and the pane's current state authority.

### 6.5 Router (RTR) — F2a

- **REQ-RTR-001** · MUST · ubiquitous — THE SYSTEM SHALL discard, before calling, every candidate that lacks a capability the request needs (tool calling, vision, a context window large enough for the assembled prompt).
- **REQ-RTR-002** · MUST · unwanted — IF a provider fails `[router] breaker_failures` consecutive calls (default 3), THEN THE SYSTEM SHALL open its circuit breaker for `breaker_cooldown` (default 60 s), skip it as a candidate meanwhile, and close it after one successful probe.
- **REQ-RTR-003** · MUST · optional — WHERE `[router] policy` is `local-first`, `cost` or `quality`, THE SYSTEM SHALL order a class's candidates by that policy after REQ-LLM-004 and REQ-SEC-006's hard constraints, and WHEN a client invokes `model.route` with a class and a request shape, THE SYSTEM SHALL return the ordered candidates and why each one was kept or dropped.

### 6.6 Full Terminal Use (FTU) — F2b

- **REQ-FTU-001** · MUST · ubiquitous — THE SYSTEM SHALL offer the agent a `terminal_interact` tool with the actions `send_keys` (keys encoded by the emulator's key encoder), `read_screen` (plain text of the current screen) and `wait_for` (a regex, an idle time or the process's exit, bounded by a timeout), risk `Exec`.
- **REQ-FTU-002** · MUST · state — WHILE the agent owns a pane's input, THE SYSTEM SHALL refuse the user's input with `INPUT_LOCKED` (REQ-TERM-008) except the take-over chord; WHEN the user takes over, THE SYSTEM SHALL move ownership to the user at once and return `taken_over` to the agent's next `terminal_interact` call.
- **REQ-FTU-003** · MUST · unwanted — IF the line the pane's shell would receive when `send_keys` sends Enter — the input assembled across every `send_keys` call since the last Enter — matches the destructive-pattern list, THEN THE SYSTEM SHALL require approval before sending the Enter, like `run_command` (REQ-SEC-005).

### 6.7 Embedded model and Active AI (AAI) — F2b

- **REQ-LLM-009** · MUST · optional — WHERE a provider of `type = "yzma"` is configured with a GGUF path, THE SYSTEM SHALL load it in process and serve the `fast` class with it, without a server and without network. (Supersedes MVP REQ-LLM-008, a COULD.)
- **REQ-AAI-001** · MUST · optional — WHERE `[active_ai] enabled = true` in `config.toml` (default false), WHEN a block closes with a non-zero exit, THE SYSTEM SHALL ask a **local** `fast`-class model for a one-line suggestion, rate-limited to `[active_ai] per_minute` (default 4), and publish it as `block.suggestion`.
- **REQ-AAI-002** · MUST · event — WHEN the user accepts a suggestion, THE SYSTEM SHALL open a thread with the block attached and the suggestion as the first message's draft, without sending it.
- **REQ-AAI-003** · MUST · unwanted — IF no local `fast` candidate is available, THEN THE SYSTEM SHALL produce no suggestion, and SHALL NEVER send block content to a remote model for Active AI.

### 6.8 Desktop client (DSK) — F2c

- **REQ-DSK-001** · MUST · ubiquitous — THE SYSTEM SHALL provide a Wails v3 desktop client that is a thin client of `umbrald` (DD-001): workspaces, tabs, split panes rendered from the daemon's screen state, blocks, threads, approvals and worktrees, using only the published protocol.
- **REQ-DSK-002** · MUST · event — WHEN an approval carries a diff, or the user opens a worktree's diff, the desktop client SHALL show it per hunk and let the user accept or reject individual hunks; THE SYSTEM SHALL apply only the accepted hunks.
- **REQ-DSK-003** · MUST · ubiquitous — the desktop client SHALL show the thread's worktree as a file tree with changed files marked, and open a file read-only, or editable when the file is inside a worktree.
- **REQ-PKG-004** · MUST · event — WHEN the desktop client creates its window, THE SYSTEM SHALL set the Wayland `app_id` and the X11 `WM_CLASS` to `io.github.ecrespo.Umbral`.
- **REQ-PKG-005** · MUST · ubiquitous — THE SYSTEM SHALL embed in the Windows executable an `.ico` with entries at 16, 20, 24, 32, 40, 48, 64 and 256 px, where the 16 and 24 px entries come from the hand-drawn sources.
- **REQ-PKG-007** · MUST · ubiquitous — THE SYSTEM SHALL, on macOS, include an `Assets.car` compiled from `appicon.icon` (Icon Composer) with `CFBundleIconName = appicon`, and a fallback `icons.icns` for macOS ≤ 15.
- **REQ-PKG-008** · SHOULD · ubiquitous — THE SYSTEM SHOULD provide an icon variant for nightly builds, distinguishable at 48 px.

The four `REQ-PKG` requirements above were held in `specs/prd/umbral-f2-desktop.md` since
2026-09-11 (delta `2026-09-visual-identity`, Analyze finding A-12), which this PRD absorbs and
replaces. The kit already carries what they consume: `wails-build/`, `macos/icon-composer-layers/`
and `windows/umbral.ico`. REQ-PKG-007 needs Icon Composer on a Mac, which no CI runner provides,
so its verification is a manual QA step.

### 6.9 Live handoff (HND) — F2d

- **REQ-HND-001** · MUST · event — WHEN a newer `umbrald` is started with `--takeover` against a running daemon of the same protocol major, THE SYSTEM SHALL transfer every live PTY (file descriptors and child processes), the emulator state and the open blocks to the new daemon, and the old daemon SHALL exit only after the new one confirms; clients SHALL reconnect and resume from a snapshot (REQ-API-001).
- **REQ-HND-002** · MUST · unwanted — IF the handoff fails at any step, THEN THE SYSTEM SHALL keep the old daemon serving with every session intact, and the new daemon SHALL exit with a non-zero status naming the step.

### 6.10 Remote and federation (RMT) — F2d

- **REQ-RMT-001** · MUST · event — WHEN the user adds a remote host (`umb remote add NAME ssh://user@host`), THE SYSTEM SHALL reach the remote `umbrald` over SSH (forwarding its socket, never a TCP port), authenticate with the remote token, and show its workspaces in the client prefixed by the host name.
- **REQ-RMT-002** · MUST · unwanted — IF a remote connection drops, THEN THE SYSTEM SHALL keep the remote sessions alive on the remote daemon, show the host as `disconnected`, reconnect with backoff, and resume every remote pane from a snapshot — independently of every other host.
- **REQ-RMT-003** · MUST · ubiquitous — THE SYSTEM SHALL keep agents, egress and approvals of a remote daemon on that daemon; the local daemon SHALL only relay its protocol, so a remote agent's egress is recorded in the remote `egress_log`.

### 6.11 Windows (WIN) — F2e

- **REQ-WIN-001** · MUST · ubiquitous — THE SYSTEM SHALL run `umbrald`, `umb` and the clients on Windows 11 x86_64, with PTYs through ConPTY and the protocol over a named pipe whose ACL admits only the current user.
- **REQ-WIN-002** · MUST · ubiquitous — THE SYSTEM SHALL pass the VT conformance suite's MUST cases and the block tests on Windows with PowerShell 7. (Promotes REQ-BLK-008 to MUST on Windows.)
- **REQ-WIN-003** · MUST · ubiquitous — THE SYSTEM SHALL read secrets from the Windows Credential Manager as the keyring (Art. 5).

### 6.12 Plugins and graphics (PLG) — F2f

- **REQ-PLG-001** · MUST · event — WHEN the user installs a plugin (`umb plugin install PATH`), THE SYSTEM SHALL read its declarative manifest — commands, MCP servers, skills, detection manifests, key bindings — show it with its digest, and install exactly that digest after confirmation, executing no plugin code in the daemon (as REQ-SKL-* do for skills).
- **REQ-PLG-002** · MUST · ubiquitous — THE SYSTEM SHALL treat an MCP server a plugin declares as `trust = untrusted` until the user changes it, and a plugin's commands as user commands subject to the policy engine.
- **REQ-PLG-003** · SHOULD · optional — WHERE a client advertises the `graphics` capability, THE SYSTEM SHOULD pass Kitty graphics protocol images from a pane to that client, and SHOULD replace them with a placeholder for clients that do not.

## 7. Non-functional requirements

| ID | Requirement | Target |
|---|---|---|
| NFR-F2-01 | Creating a worktree on a 1 GiB repository | < 2 s p95 (no checkout of untracked files) |
| NFR-F2-02 | Overhead of one more idle parallel thread on the daemon | < 15 MiB RSS |
| NFR-F2-03 | ACP mediation latency (agent request → Umbral answer, policy `allow`) | < 20 ms p95 |
| NFR-F2-04 | Active AI suggestion with the embedded model | < 3 s p95 on the reference laptop |
| NFR-F2-05 | Handoff downtime for clients | < 1 s, no PTY output lost |
| NFR-F2-06 | Desktop client input-to-paint latency | < 16 ms p95 for typed text |

The F1 budgets (REQ-TERM-001, REQ-TERM-006, REQ-BLK-006, idle memory) keep applying and keep their gates.

## 8. Slices and exit criteria

| Slice | Content | Closes when |
|---|---|---|
| **F2a Orchestration** | WT, ORC, ACP, XAG, RTR | **US-F2-001 passes** — the phase's exit criterion: the daemon's properties hold in every run measured and at least one run completes every task within its files (delta `2026-10-f2a-close`). Closed 2026-10-24 |
| F2b Agent in the terminal | FTU, LLM-009, AAI | US-F2-002 ≥ 70 %; Active AI offline only |
| F2c Desktop | DSK, PKG-004/005/007 | the desktop QA walk (`docs/qa/f2-desktop.md`) with no failing step |
| F2d Durability and remote | HND, RMT | an upgrade with live panes and a remote host survive a dropped link, measured |
| F2e Windows | WIN | the CI gate green on `windows-latest` |
| F2f Extensibility | PLG | a plugin installed by digest adds an MCP server and a command |

Recommended order: F2a first (the exit criterion and what F2b–F2c build on), then F2b and F2c in
parallel, then F2d, F2e, F2f. Release 0.1's Hardening gates (T-REL-01, T-REL-02, T-HRD-01…03) are
independent of F2 and can close alongside F2a.

## 9. Reference scenarios

- **US-F2-001 — three agents, one repo.** A repository with a failing test, a stale README and a
  function to rename. The user creates three threads in one workspace: a local `gpt-oss:20b` thread
  "fix the failing test", a second local thread "update the README", and an ACP thread with a
  third-party agent "rename `Mean` to `Average` everywhere". Each gets a worktree. All three run
  concurrently. Success: each finishes; `git status` of the main working copy is untouched until a
  merge; each worktree's diff touches only its task's files; merging the three in any order succeeds
  or reports a conflict without losing either side; every turn of every thread has a trace; every
  write, command and fetch of the ACP thread was a `session/request_permission` decided by Umbral's
  policy engine and recorded in `tool_calls`. The ACP agent is OpenCode pointed at the same local
  Ollama, so its model calls stay on the machine; anything else its own process sends is its own
  traffic — measured on OpenCode 1.18.33, its provider package from npm and a model catalogue at
  start — and its thread reads `egress_visibility = unobserved` (REQ-ACP-007), because Umbral
  sees neither (delta `2026-10-usf2-live`).
- **US-F2-002 — interactive programs.** The agent is asked to "answer the prompts": a `psql` session
  waiting for a query, then `git rebase -i` opening an editor with three picks to squash into one.

## 10. Decisions and open questions

Decided by the Tech Lead on 2026-10-22:

- **Q-F2-01 — the reference ACP agent is OpenCode**, pointed at the local Ollama, so its model
  calls stay local. *(0.7: the live run found that the agent's own process still fetches its
  provider package and a model catalogue at start — see §9; whether that should be switched off is
  T-HRD-02's.)*
- **Q-F2-02 — Art. 4 and hosted agents.** A hosted agent's own network traffic (its model calls,
  an allowed `fetch` and anything else its process sends) and its file reads are disclosed, not
  audited: the thread's `egress_visibility = unobserved` (REQ-ACP-007). This is written
  into Art. 4 as a clarifying amendment (§11), not left as an unjustified exception.
- **Q-F2-03 — worktrees live outside the repository**, under `$XDG_DATA_HOME/umbral/worktrees/`
  (REQ-WT-001), departing from architecture §5.8's `.umbral/worktrees/`.
- **Q-F2-04 — a worktree lands as a squash merge** (REQ-WT-004); opening a pull request stays F3.
- **Q-F2-05 — the ACP SDK is `github.com/coder/acp-go-sdk`**, on the spike's evidence
  (`docs/spikes/2026-10-acp-opencode.md`); it enters the constitution's stack table.
- **Q-F2-08 — Art. 5 and hosted agents (Analyze F-01).** Umbral launches a hosted agent only with a
  configuration that makes it ask before every side effect, and decides each request with the policy
  engine; an agent that cannot be made to ask is refused (REQ-ACP-009). A sandbox around the agent's
  process (bubblewrap, landlock, seatbelt) is a later hardening, not an F2 requirement.
- **Q-F2-09 — what `full-auto` allows (Analyze F-07):** `WriteFS` inside the worktree and `Exec` with
  its cwd there; `Network`, the destructive list and taint still ask; a worktree that stops being
  `active` clears `full-auto` and leaves the thread's own mode (REQ-ORC-004).

Still open, with the draft's default:
- **Q-F2-06 — Wails v3 maturity.** v3 is still beta; F2c starts after F2a either way.
- **Q-F2-07 — Semantic index.** Kept in F3 unless decided otherwise.

## 11. Constitution check

- **Art. 2, 3:** every MUST above gets a task and a named test; the new modules (`worktrees`, `acp`)
  get `.go-arch-lint.yml` rules before their code. REQ-PKG-007's Icon Composer step needs a Mac and is
  checked by inspection of the built bundle (`Info.plist`, `Assets.car` present) in a test; only the
  compile itself is manual.
- **Art. 4 — amended (clarification, 2026-10-22):** `egress_log` records every request Umbral itself
  sends off the machine. An external agent Umbral hosts makes its own network requests (model calls,
  an allowed `fetch`, anything else) and reads files in its own process; Umbral cannot observe them, so it discloses them on the thread
  (`egress_visibility = unobserved`, shown when the thread is created) rather than auditing them.
  Umbral's own agents keep the full guarantee, and Active AI is local-only (REQ-AAI-003).
- **Art. 5:** `full-auto` exists only inside an active worktree and frees only `WriteFS` and `Exec`
  there (REQ-ORC-004). Every side effect of a hosted agent is a permission request the policy engine
  decides (REQ-ACP-003, REQ-ACP-009); the agent runs isolated from configuration Umbral did not
  write, and an agent that cannot be made to ask, or a repository that would reconfigure it, is
  refused.
- **Art. 6:** no table is rebuilt: new states and flags are new columns, written by code that enforces
  their values, as migration 0005 did (Data Model §2.5).
- **Art. 7:** ACP turns are traced like Umbral's own (REQ-ACP-007).
- **Art. 8:** ACP is the extension protocol for external agents, as the article requires.
- **Stack constraints:** `github.com/coder/acp-go-sdk` (ACP) and `git` ≥ 2.34 (worktrees) join the
  table with F2a; yzma joins with T-F2-21.

## Change History

| Version | Date | Author | Changes |
|---|---|---|---|
| 0.1 | 2026-10-22 | E. Crespo (assisted draft) | First proposal: F2 scope, 48 requirements in 13 areas, six slices, two reference scenarios, seven open questions |
| 0.2 | 2026-10-22 | E. Crespo (assisted draft) | 52 requirements: 48 new and 4 absorbed. The Tech Lead decides Q-F2-01…04: OpenCode as the reference ACP agent, offline; ACP egress disclosed as `external`; worktrees under `$XDG_DATA_HOME`; squash merge. §6.8 absorbs REQ-PKG-004, 005, 007 and 008 from `umbral-f2-desktop.md`, which is deleted |
| 0.3 | 2026-10-22 | E. Crespo (assisted draft) | Analyze (spec-guardian, FIX FIRST, 22 findings) and spike T-F2-00 applied. ACP rewritten around observed behaviour: hosted agents launched to ask before every side effect (new REQ-ACP-009), reads outside the write root ask, ACP terminals in their own sessions, `agent_error` on silence; `egress_visibility` replaces the overloaded `exposure`. WT-004's refusals, ORC-002's queued turn, ORC-004 per risk, `workspace_id` on threads, FTU-003 on the assembled line, config keys placed in `config.toml`/`models.toml`. Q-F2-05, Q-F2-08 and Q-F2-09 decided; Art. 4 clarified by amendment |
| 0.4 | 2026-10-22 | E. Crespo (assisted draft) | spec-guardian verify pass (FIX FIRST: N-01…N-10 and the partly closed F-items) applied. REQ-ACP-009 forces `ask` on every permission, isolates the agent's `HOME`/XDG and refuses a repository-level agent config; the Art. 4 amendment covers all of a hosted agent's own network; ACP-003 maps `move`/`read`/`search`/`think`; ACP-004 does not decide a write twice and gives `fs/*`/`terminal/create` their own `tool_calls` row; ACP-006's timer pauses on the agent's outstanding requests (default 600 s); ACP-007 records a `decision`; WT-004's `commit_failed` and `commit_timeout`; WT-007's report uses `worktrees.state`; ORC-004 clears `full-auto` without forcing `normal` and covers ACP threads; ACP-001 without a worktree; DD-012 for ACP threads in ACP-002 |
| 0.5 | 2026-10-22 | E. Crespo (assisted draft) | Delta `2026-10-worktree-methods` (T-F2-03): REQ-WT-001's worktrees sit beside the daemon's database, which is `$XDG_DATA_HOME/umbral` by default, so a daemon given `--db` keeps them with its data |
| 0.6 | 2026-10-23 | E. Crespo (assisted draft) | Delta `2026-10-acp-client` (T-F2-07): REQ-ACP-009's `project_config` refusal is a `VALIDATION_ERROR` when found before anything is written, `agent_unavailable` when found only in the new worktree and `agent_error` at a later launch; env references may not name the agent's own configuration variables (`OPENCODE_*`), and the agent's stderr is discarded |
| 0.7 | 2026-10-24 | E. Crespo (assisted draft) | Delta `2026-10-usf2-live` (T-F2-12): §9's US-F2-001 no longer says the scenario runs offline — the hosted agent's model calls stay local, but at start OpenCode fetches its provider package and a model catalogue, its own unobserved traffic; Q-F2-01 says the same |
| 0.8 | 2026-10-24 | E. Crespo (assisted draft) | Delta `2026-10-f2a-close`: §8 says what "US-F2-001 passes" means — the daemon's properties in every run and one run completing every task within its files — and F2a is closed; the status line no longer says nothing is implemented |
| 0.9 | 2026-10-24 | E. Crespo (assisted draft) | Delta `2026-10-hrd02-open-questions` (T-HRD-02, Q11): REQ-ACP-007's disclosure says a hosted agent can read the main copy and every other thread's worktree |
