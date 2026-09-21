# Umbral MVP — local-first agentic terminal

## Product Requirements Document (PRD)

| Field | Value |
|---|---|
| **Author** | Ernesto Crespo (Tech Lead) · assisted draft |
| **Status** | `DRAFT` |
| **Version** | 1.8 |
| **Date** | 2026-09-11 |
| **Reviewers** | pending |
| **Last updated** | 2026-09-20 |
| **Source** | `docs/ARCHITECTURE.md` v0.1 |

---

## 1. Executive Summary

We will build **Umbral**, an agentic terminal written in Go. It is meant for developers who live in
the terminal and want an agent that can read their context: command blocks, files, git and project
rules. The agent proposes and executes changes with explicit approval, using **local models**.
Umbral removes the dependency on agentic terminals whose agent requires a cloud account.

Success is measured by one concrete criterion: completing the flow "command fails → ask for a fix →
approve the diff → green test" end to end, **offline**, with a local model on a laptop.

The MVP covers phases F0 (terminal core and blocks) and F1 (agent, model gateway, MCP, CLI and
security) from the architecture document. The desktop GUI, multi-agent orchestration and background
agents come in later versions.

## 2. Context and Problem

### 2.1 Current Situation

Mature ADEs (Warp) combine blocks, universal input, an agent with MCP and cloud agents, but their
cloud orchestration does not support bringing your own key. Wave Terminal offers AI with your own
provider and Ollama, but its agent is mostly a chat on top of the terminal. Crush (Go) is an
excellent agent, but it is not a terminal emulator.

### 2.2 Problem

No terminal combines these four properties at once:

- a full emulator with semantic blocks;
- an agent with tools and fine-grained permissions;
- offline operation with local models as first-class citizens;
- extension through open protocols (MCP, ACP) without lock-in.

### 2.3 Opportunity

Open models of around 20B parameters (e.g. `gpt-oss:20b`) already run on laptops with an iGPU and
unified memory. The Go ecosystem already provides the building blocks:

- libghostty-vt for emulation;
- Fantasy for multi-provider access;
- the official MCP SDK;
- yzma for embedded inference.

## 3. Target Users

### Persona 1: Backend engineer / Tech Lead
- **Description:** senior developer working on Linux, handling several repos and operating services.
- **Main need:** diagnose and fix failures (tests, builds, deployments) without leaving the terminal and without sending code to the cloud.
- **Usage frequency:** daily.
- **Technical level:** high.

### Persona 2: Operator / SRE
- **Description:** works over SSH with many long-running sessions.
- **Main need:** sessions that survive closing the window, searchable history and explanations of output.
- **Usage frequency:** daily.
- **Technical level:** high.

## 4. Goals and Success Metrics

### 4.1 Product Goals

| Goal | Metric | Target | Deadline |
|---|---|---|---|
| Offline fix flow | % of runs of the reference scenario (§9, US-003) that end with a green test using local `gpt-oss:20b` | ≥ 70 % over 20 runs | end of F1 |
| Terminal usable daily | VT conformance suite (Technical Design §8) | 100 % of MUST cases green | end of F0 |
| Tool reliability | Rate of invalid tool calls after repair, per supported local model | < 5 % | end of F1 |
| Verifiable privacy | Remote requests without an `egress_log` entry | 0 | continuous |

### 4.2 User Goals

| User goal | Indicator |
|---|---|
| Fix an error without copy-pasting | The failed block is attached with one action, without manual selection |
| Trust the agent | Every action with side effects goes through a visible, audited approval |
| Resume work | Reopening the client shows previous sessions, blocks and threads |

## 5. Scope

### 5.1 In Scope
- [ ] `umbrald` daemon with locally durable PTY sessions and VT emulation.
- [ ] Workspace / tab / pane model owned by the daemon, with portable layouts and rollup state.
- [ ] Protocol surface for clients and scripts: bootstrap snapshot, sequenced events, waits and published JSON Schema.
- [ ] Integration surface: environment variables, external state reports and display metadata.
- [ ] Structure restored after a daemon restart, with optional pane screen replay.
- [ ] Blocks through shell integration (bash, zsh, fish) and FTS search.
- [ ] TUI client (Bubble Tea v2) with tabs, splits, block navigation and an agent panel.
- [ ] Agent runtime with built-in tools, modes, permissions and limits.
- [ ] Context: rules files, `@` attachments, git and compaction.
- [ ] Model gateway: Ollama, llama.cpp, LM Studio, OpenRouter and generic OpenAI-compatible; presets for the Hugging Face router and OmniRoute.
- [ ] MCP client (stdio and streamable HTTP).
- [ ] `umb` CLI (`ai`, `block`, `status`).
- [ ] Secret redaction with signed rule updates, key management and recovery.
- [ ] `egress_log`, keyring with an opt-in environment fallback, and an authenticated socket.
- [ ] Wait monitoring: inventory, safe cancellation and stalled-turn detection.
- [ ] Icons and `.desktop` file for release 0.1.
- [ ] OpenTelemetry traces per turn.

### 5.2 Out of Scope
- Wails v3 desktop GUI: phase F2. The TUI validates the core first.
- Full Terminal Use (the agent driving interactive programs): F2.
- Active AI and the embedded yzma model: F2.
- ACP client and server, MCP server: F2/F3.
- Parallel threads with worktrees and background agents: F2/F3.
- SSH with a remote agent and remote durability: F2.
- Windows: F2. It requires ConPTY and its own tests.
- Detection of third-party agents by process and declarative manifests: F2, once ACP lands.
- Worktrees as a daemon primitive and multi-machine federation over SSH: F2.
- Plugins with a declarative manifest and pane graphics: F2.
- Live handoff of PTYs between daemon versions: F2.
- Real-time collaboration and any cloud backend: outside the product.

### 5.3 Future Considerations
- Full policy router (cost/quality, per-provider circuit breakers): F2.
- Third-party agent runtime: detection manifests with local override, `agent explain`, and lifecycle authority delegated to hooks or ACP: F2.
- Semantic code index (tree-sitter + embeddings): F2.
- WASM plugins, workflows and notebooks exportable to Obsidian: F3.

## 6. Functional Requirements (EARS)

Format: **ID** · priority · EARS pattern — criterion. Every MUST has a task and a test in
`specs/tasks/`.

### 6.1 Sessions and emulation (TERM)

- **REQ-TERM-001** · MUST · event — WHEN an authenticated client invokes `session.create`, THE SYSTEM SHALL launch the requested shell in a PTY and reply with a `session_id` in under 300 ms p95.
- **REQ-TERM-002** · MUST · ubiquitous — THE SYSTEM SHALL process PTY output with the VT emulator and pass 100 % of the MUST cases of the conformance suite (alt-screen, truecolor, bracketed paste, resize and reflow).
- **REQ-TERM-003** · MUST · state — WHILE a session is alive, THE SYSTEM SHALL keep the PTY and screen state even when no client is connected.
- **REQ-TERM-004** · MUST · event — WHEN a client subscribes to an existing session, THE SYSTEM SHALL send a screen snapshot and up to 10,000 lines of scrollback before the first live chunk.
- **REQ-TERM-005** · MUST · unwanted — IF the shell process exits, THEN THE SYSTEM SHALL emit `session.exited` with the exit code and keep the session's blocks in the database.
- **REQ-TERM-006** · MUST · ubiquitous — THE SYSTEM SHALL forward PTY output to subscribed clients adding under 5 ms p95 of latency, measured inside the daemon.
- **REQ-TERM-007** · MUST · event — WHEN a client invokes `session.resize`, THE SYSTEM SHALL apply the new size to the PTY and the emulator, and notify `session.resized` to every subscribed client.
- **REQ-TERM-008** · MUST · unwanted — IF a client sends input to a session whose input lock belongs to the agent, THEN THE SYSTEM SHALL reject it with `INPUT_LOCKED` without writing it to the PTY.
- **REQ-TERM-009** · MUST · event — WHEN the daemon starts after a shutdown, THE SYSTEM SHALL restore the saved workspace, tab and pane structure with their labels and cwd, launch a fresh shell in each restored pane, and mark the sessions of the previous run as `exited`.
- **REQ-TERM-010** · MUST · optional — WHERE `[experimental] pane_history = true` in `$XDG_CONFIG_HOME/umbral/config.toml`, THE SYSTEM SHALL replay the stored recent screen of each restored pane before its new shell output; the setting SHALL be disabled by default because pane output can contain secrets. Capture happens every 10 s and on clean shutdown, so a crash loses at most the last window.
- **REQ-TERM-011** · MUST · unwanted — IF a restored pane has a stored launch command, THEN THE SYSTEM SHALL leave it visible in the pane without running it, and SHALL run it only after the user confirms, so that a restart never re-executes commands on its own. `layout.apply` behaves the same way: it returns the commands as pending, never as launched.

### 6.2 Blocks (BLK)

- **REQ-BLK-001** · MUST · event — WHEN the shell emits `OSC 133;C`, THE SYSTEM SHALL create a block in state `running` with the command (`OSC 633;E`), the cwd (`OSC 7`) and the start time.
- **REQ-BLK-002** · MUST · event — WHEN the shell emits `OSC 133;D;<exit>`, THE SYSTEM SHALL close the block with its exit code and duration, and publish `block.closed`.
- **REQ-BLK-003** · MUST · unwanted — IF a session emits no shell-integration sequences within the first 5 s, THEN THE SYSTEM SHALL mark it `integration: none` and keep delivering output without creating blocks. WHEN a shell-integration sequence arrives after that window, THE SYSTEM SHALL set `integration: osc133` and record blocks from it; THE SYSTEM SHALL NOT move a session from `osc133` back to `none`. The window is a heuristic about silence, not a verdict about the shell: without the promotion a shell slower than five seconds is advertised as having no integration while its blocks are being recorded, and `session.get` and `block.list` disagree about the same session.
- **REQ-BLK-004** · MUST · event — WHEN a `running` block enables the alternate screen, THE SYSTEM SHALL mark it `interactive` and exclude alternate-screen content from its stored output.
- **REQ-BLK-005** · MUST · ubiquitous — THE SYSTEM SHALL provide shell-integration bootstrap scripts for bash, zsh and fish, injected when the session is created.
- **REQ-BLK-006** · MUST · event — WHEN a client invokes `block.search`, THE SYSTEM SHALL return command and output matches in under 200 ms p95 with 100,000 stored blocks.
- **REQ-BLK-007** · MUST · ubiquitous — THE SYSTEM SHALL store, for every closed block, a plain-text version without escape sequences, which is the one used as agent context.
- **REQ-BLK-008** · SHOULD · ubiquitous — THE SYSTEM SHOULD provide a shell-integration bootstrap for PowerShell.

### 6.3 Agent (AGT)

- **REQ-AGT-001** · MUST · event — WHEN the user sends a message to a thread with `thread.send`, THE SYSTEM SHALL run the agent loop and emit the response chunks as `thread.delta` notifications.
- **REQ-AGT-002** · MUST · ubiquitous — THE SYSTEM SHALL offer the model the built-in tools `run_command`, `read_file`, `write_file`, `edit_file`, `grep`, `glob`, `list_dir` and `fetch_url`, each with a JSON Schema and a declared risk level.
- **REQ-AGT-003** · MUST · event — WHEN the agent runs `run_command`, THE SYSTEM SHALL execute it in a PTY dedicated to the thread and record it as a block with `origin = agent`.
- **REQ-AGT-004** · MUST · event — WHEN the policy engine returns `ask` for a tool, THE SYSTEM SHALL emit `approval.requested` and pause the turn until it receives `approval.respond`.
- **REQ-AGT-005** · MUST · unwanted — IF the user denies an approval, THEN THE SYSTEM SHALL return a `denied_by_user` tool result to the model and continue the turn without running the tool.
- **REQ-AGT-006** · MUST · unwanted — IF the arguments of a tool call do not validate against its schema, THEN THE SYSTEM SHALL retry once with a repair message and, if it fails again, end the turn with `stop_reason = tool_error`.
- **REQ-AGT-007** · MUST · event — WHEN the user invokes `thread.cancel`, THE SYSTEM SHALL stop the turn and terminate the processes it launched in under 500 ms.
- **REQ-AGT-008** · MUST · unwanted — IF a turn reaches `max_steps` (50 by default) or the thread's token budget, THEN THE SYSTEM SHALL stop it with `stop_reason = max_steps` or `budget`.
- **REQ-AGT-009** · MUST · state — WHILE a thread is in `ask` mode, THE SYSTEM SHALL expose only `ReadOnly` tools to the model.
- **REQ-AGT-010** · MUST · event — WHEN the user changes a thread's model, THE SYSTEM SHALL use the new model from the next turn on, keeping the full history.
- **REQ-AGT-011** · MUST · ubiquitous — THE SYSTEM SHALL persist every message, tool call, result and approval decision of a thread before sending it to the client.
- **REQ-AGT-013** · MUST · state — WHILE a thread is in `auto-edit` mode, THE SYSTEM SHALL allow without approval the `WriteFS` tools whose path is inside the thread's **write root** and SHALL request approval (`ask`) for those pointing outside it. The deny reason reported for the second case is `outside_write_root`.
- **REQ-AGT-014** · MUST · state — WHILE a thread is in `normal` mode (the default), THE SYSTEM SHALL apply `ask` to every tool that is not `ReadOnly`, unless the user has persisted `allow` rules.
- **REQ-AGT-015** · MUST · unwanted — IF `thread.send` arrives with a `client_msg_id` already processed in the same thread, THEN THE SYSTEM SHALL reply with the original `turn_id` and `message_id` without creating a new turn or running any tool again.
- **REQ-AGT-016** · MUST · state — WHILE a thread has finished a turn that no client has viewed, THE SYSTEM SHALL expose its attention state as `done`, and SHALL change it to `idle` when a client focuses the thread.
- **REQ-AGT-017** · MUST · event — WHEN the daemon starts after a shutdown, THE SYSTEM SHALL restore every thread with its full message history, mark interrupted turns `stopped`, and accept `thread.send` on them without losing previous context.
- **REQ-AGT-018** · MUST · ubiquitous — THE SYSTEM SHALL restrict `fetch_url` to the `http` and `https` schemes, a 10 s timeout and 2 MiB of body, SHALL refuse redirects whose target resolves to loopback, link-local or private address ranges, and SHALL mark the result as untrusted content.
- **REQ-AGT-012** · SHOULD · event — WHEN `edit_file` or `write_file` requires approval, THE SYSTEM SHOULD include the unified diff of the proposed change in `approval.requested`.

### 6.4 Context (CTX)

- **REQ-CTX-001** · MUST · event — WHEN a turn's prompt is assembled, THE SYSTEM SHALL include the rules files `AGENTS.md`, `CLAUDE.md`, `WARP.md` and `CRUSH.md` (and their `.local.md` variants), searched from the repo root down to the thread's cwd. Precedence is by depth — the file closest to the cwd wins — and, at equal depth, `AGENTS.md` → `CLAUDE.md` → `WARP.md` → `CRUSH.md`, with each `.local.md` above its own file.
- **REQ-CTX-002** · MUST · event — WHEN a message includes `@file`, `@directory` or `@block:<id>` attachments, THE SYSTEM SHALL add their content: file text, directory listing or the block's plain text.
- **REQ-CTX-003** · MUST · state — WHILE the thread's cwd is inside a git repository, THE SYSTEM SHALL include the current branch, `git status --short` and `git diff --stat` in the context.
- **REQ-CTX-004** · MUST · unwanted — IF the estimated context exceeds the model window minus the response reserve, THEN THE SYSTEM SHALL compact the history with a summary before sending and record the `context.compacted` event.
- **REQ-CTX-005** · MUST · unwanted — IF an attachment exceeds 256 KiB, THEN THE SYSTEM SHALL truncate it and state in the context how many bytes were omitted.

### 6.5 Models (LLM)

- **REQ-LLM-001** · MUST · ubiquitous — THE SYSTEM SHALL support the providers `ollama` (native API), `llamacpp`, `lmstudio`, `openrouter` and generic `openai-compat`.
- **REQ-LLM-002** · MUST · event — WHEN the daemon starts, or a client invokes `model.list` with `refresh = true`, THE SYSTEM SHALL discover the models of every configured provider (`/v1/models`, `/api/tags`) and update the catalog.
- **REQ-LLM-003** · MUST · unwanted — IF a provider replies 429 or 5xx, or does not deliver the first token within `first_token_timeout` (30 s remote, 120 s local), THEN THE SYSTEM SHALL try the next candidate of the task class and record the failure in `usage`.
- **REQ-LLM-004** · MUST · optional — WHERE `router.offline = true`, THE SYSTEM SHALL discard every remote candidate and, if no local one remains, reply `PROVIDER_UNAVAILABLE`.
- **REQ-LLM-005** · MUST · ubiquitous — THE SYSTEM SHALL record, for every model call, the input tokens, output tokens, time to first token and cost in micro-USD.
- **REQ-LLM-006** · MUST · ubiquitous — THE SYSTEM SHALL send Ollama the model's configured `num_ctx` in every request.
- **REQ-LLM-007** · SHOULD · ubiquitous — THE SYSTEM SHOULD include configuration presets for the Hugging Face router (`https://router.huggingface.co/v1`) and for a self-hosted OmniRoute instance.
- **REQ-LLM-008** · COULD · ubiquitous — THE SYSTEM MAY run an embedded model through yzma for the `fast` class.

### 6.6 Security (SEC)

- **REQ-SEC-001** · MUST · ubiquitous — THE SYSTEM SHALL apply the redaction rules to all content before sending it to a provider, replacing every match with `[REDACTED:<rule>]`. The rules are prefix patterns (`sk-`, `ghp_`, `AKIA`, PEM blocks, JWT) plus a generic detector that only fires on strings with entropy ≥ 4.5 bits per character and length ≥ 20.
- **REQ-SEC-002** · MUST · event — WHEN a request is sent to a remote provider, THE SYSTEM SHALL record in `egress_log` the destination host, bytes sent, SHA-256 of the payload, provider and thread.
- **REQ-SEC-003** · MUST · unwanted — IF a socket connection does not present the valid local token in `system.hello`, THEN THE SYSTEM SHALL reply `UNAUTHORIZED` and close the connection.
- **REQ-SEC-004** · MUST · unwanted — IF the configuration contains a plaintext API key, THEN THE SYSTEM SHALL reject that provider entry and indicate that `keyring:<path>` must be used.
- **REQ-SEC-005** · MUST · ubiquitous — THE SYSTEM SHALL require approval for commands matching the destructive-pattern list (`rm -rf`, `git push --force`, `mkfs`, `dd of=`, `kubectl delete`, …), regardless of `allow` rules and the thread mode.
- **REQ-SEC-006** · MUST · state — WHILE a turn's context contains content marked as untrusted (results from `fetch_url` or from MCP servers with `trust = untrusted`), THE SYSTEM SHALL require approval for tools with risk `Exec` and `Network`.
- **REQ-SEC-007** · MUST · ubiquitous — THE SYSTEM SHALL create the socket with permissions `0600` in `$XDG_RUNTIME_DIR/umbral/`.
- **REQ-SEC-008** · MUST · unwanted — IF the operating system keyring is unavailable when the daemon starts (headless Linux without Secret Service, locked keychain), THEN THE SYSTEM SHALL start without aborting, disable the providers whose credential is `keyring:<path>`, mark their models `health = down` with reason `keyring_unavailable`, and show that reason in `umb status`.
- **REQ-SEC-009** · MUST · event — WHEN a client invokes `policy.explain` with a candidate action, THE SYSTEM SHALL return the resulting decision and the ordered trace of the rules evaluated (destructive pattern, `deny` rule, taint, thread mode, `allow` rule, mode default), naming the rule that decided.
- **REQ-SEC-010** · MUST · optional — WHERE the rule override directory `$XDG_CONFIG_HOME/umbral/rules/` contains valid files, THE SYSTEM SHALL load the redaction rules and destructive patterns from it, SHALL give it precedence over the rules built into the binary, and SHALL ignore invalid files with a warning without falling back to an empty rule set.
- **REQ-SEC-011** · MUST · optional — WHERE `[update] rules_check = true` (disabled by default), THE SYSTEM SHALL fetch the rule bundle from the configured URL, verify its Ed25519 signature against a key in the trust store, verify that its version is strictly greater than the installed one, record the request in `egress_log`, and apply it only when every check passes; it SHALL never override the local rule directory.
- **REQ-SEC-012** · MUST · optional — WHERE the keyring is unavailable and `[secrets] allow_env = true`, THE SYSTEM SHALL accept `env:<VAR>` as a credential source, SHALL report the affected providers as `degraded` with reason `env_secret` in `umb status`, and SHALL never write the value to logs, traces or `egress_log`.
- **REQ-SEC-013** · MUST · unwanted — IF a bundle arrives unsigned, with an unknown key, with an invalid signature or with a version lower than or equal to the installed one, THEN THE SYSTEM SHALL discard it, keep the rules in force, record the reason in `egress_log` and notify the user; it SHALL NOT retry that bundle.
- **REQ-SEC-014** · MUST · event — WHEN the user manages the trust store (`umb rules key add|list|remove|rotate`), THE SYSTEM SHALL show the key's SHA-256 fingerprint and require explicit confirmation before adding or rotating, SHALL refuse to remove the last valid key unless `--force` is given, and SHALL record every change with its timestamp.
- **REQ-SEC-015** · MUST · unwanted — IF the trust store ends up without a valid key, or three consecutive verifications fail, THEN THE SYSTEM SHALL disable remote updates (fail closed), keep working with the rules in force, and require a manual `umb rules key add` to re-enable them.
- **REQ-SEC-016** · MUST · event — WHEN the user invokes `umb rules rollback` or `umb rules reset`, THE SYSTEM SHALL return to the previous verified bundle, or to the rules built into the binary, without network access and without needing a valid key, keeping the local override directory untouched.

### 6.7 MCP

- **REQ-MCP-001** · MUST · event — WHEN an MCP server is configured (stdio or streamable HTTP), THE SYSTEM SHALL connect it and expose its tools to the model with the prefix `mcp_<server>_<tool>`.
- **REQ-MCP-002** · MUST · event — WHEN a server is added with `mcp.server.add` during a thread, THE SYSTEM SHALL offer its tools from the next turn on without restarting the thread.
- **REQ-MCP-003** · MUST · unwanted — IF an MCP server crashes or does not respond within 10 s, THEN THE SYSTEM SHALL mark it `unavailable`, return an error to pending tool calls and retry the connection with exponential backoff (at most 5 attempts).
- **REQ-MCP-004** · MUST · ubiquitous — THE SYSTEM SHALL apply the `ask` policy to MCP tools by default, unless there are explicit per-server or per-tool rules.

### 6.8 CLI and TUI

- **REQ-CLI-001** · MUST · event — WHEN `umb ai "<prompt>"` runs with data on stdin, THE SYSTEM SHALL create an ephemeral thread in `ask` mode with stdin as an attachment (at most 1 MiB) and stream the response to stdout.
- **REQ-CLI-002** · MUST · event — WHEN `umb block last --json` runs, THE SYSTEM SHALL print the last closed block of the current session as JSON, following the API `Block` schema. The current session is the one named by `UMBRAL_SESSION_ID`, the variable the daemon injects into every managed pane (REQ-INT-001). WHERE that variable is absent, because the command was run outside a managed pane, THE SYSTEM SHALL print the last closed block of the whole history instead, which is what "the last thing that ran" means to someone in a plain terminal; THE SYSTEM SHALL NOT fail for want of a session.
- **REQ-CLI-004** · MUST · ubiquitous — THE SYSTEM SHALL use these exit codes in `umb`, so that a script can tell the three outcomes apart: `0` the command answered, `1` the daemon reported an error or the request was rejected, `69` (`EX_UNAVAILABLE`) the daemon is unavailable and could not be started. A command whose output could not be written SHALL exit `1` rather than `0`, except when the failure is a closed pipe, which is what `| head` does deliberately.
- **REQ-CLI-003** · MUST · unwanted — IF the daemon is not running, THEN `umb` SHALL try to start it and, if that fails within 3 s, exit with code 69 and an actionable message.
- **REQ-TUI-001** · MUST · ubiquitous — THE SYSTEM SHALL provide in the TUI tabs, splits, jumping between blocks and an agent panel with pending approvals.
- **REQ-TUI-002** · MUST · event — WHEN the user presses the mode shortcut (`ctrl+space` by default), the TUI SHALL toggle input between shell and agent.
- **REQ-TUI-003** · MUST · event — WHEN the user picks "attach to agent" on a block, the TUI SHALL open the agent panel with `@block:<id>` preloaded in the input.

### 6.9 Workspaces, tabs and panes (WS)

- **REQ-WS-001** · MUST · event — WHEN a client invokes `workspace.create`, THE SYSTEM SHALL create the workspace together with its first tab and its root pane, and return the three records in a single response.
- **REQ-WS-002** · MUST · ubiquitous — THE SYSTEM SHALL address workspaces, tabs and panes with public identifiers of the form `w<n>`, `w<n>:t<m>` and `w<n>:p<m>`, unique and stable within a session while the object exists.
- **REQ-WS-003** · MUST · event — WHEN a client invokes `pane.split` with a direction and a ratio, THE SYSTEM SHALL create the new pane in that position, attach a terminal session to it and return the resulting pane and layout.
- **REQ-WS-004** · MUST · event — WHEN a client invokes `layout.export`, THE SYSTEM SHALL return the tab layout as a binary tree of pane and split nodes including each pane's label, cwd and launch command.
- **REQ-WS-005** · MUST · event — WHEN a client invokes `layout.apply` with such a tree, THE SYSTEM SHALL create a tab that reproduces the structure, labels, cwd, env and commands, and SHALL state in the response that live processes and scrollback are not reproduced.
- **REQ-WS-006** · MUST · state — WHILE a workspace contains panes or threads, THE SYSTEM SHALL expose a rollup state equal to the most urgent state among them, in the order `blocked` > `working` > `done` > `idle` > `unknown`. A workspace with no panes or threads reports `idle`, and `unknown` propagates only when every child is `unknown` — so `unknown` is one of the five values a client must render, not an internal placeholder.
- **REQ-WS-007** · MUST · unwanted — IF a pane is moved to another workspace, THEN THE SYSTEM SHALL assign it a new public identifier, keep the previous identifier resolvable as an alias for the life of that terminal, and emit `pane.moved` instead of a close/create pair.

### 6.10 Protocol and clients (API)

- **REQ-API-001** · MUST · event — WHEN a client invokes `session.snapshot`, THE SYSTEM SHALL return the focused workspace, tab and thread, the workspace, tab, pane and thread records, the layout of each tab, and the `seq` of the last notification included in that snapshot.
- **REQ-API-002** · MUST · ubiquitous — THE SYSTEM SHALL number every notification with a `seq` that is monotonic per daemon run and shared by every connection, so that a client that subscribed before requesting its snapshot can discard the events already contained in it. The number lives in the notification's envelope and is distinct from `session.output`'s per-session `seq`, which anchors bytes to a screen (API Spec §1, §6).
- **REQ-API-003** · MUST · unwanted — IF a client invokes a method name that this daemon version does not know, THEN THE SYSTEM SHALL reply `METHOD_NOT_FOUND` and keep the connection and every other capability usable. IF the name is known but its capability is switched off in this build, THEN THE SYSTEM SHALL reply `NOT_IMPLEMENTED` under the same guarantee (API Spec §9).
- **REQ-API-004** · MUST · ubiquitous — THE SYSTEM SHALL be able to print the JSON Schema of the protocol built into the binary, covering requests, responses, errors and notifications, so that clients and tests can validate against it. The comparison against the API Spec is blocking for method names, required parameters and error codes, and non-blocking for descriptions and added optional fields.

### 6.11 Automation and waits (AUT)

- **REQ-AUT-001** · MUST · event — WHEN a client invokes `thread.wait` with one or more target states, THE SYSTEM SHALL pin the thread's current turn and return as soon as that pinned turn reaches one of those states, without a replacement turn satisfying the wait.
- **REQ-AUT-002** · MUST · unwanted — IF `thread.send` is invoked with `wait` on a thread that is already `awaiting_approval`, THEN THE SYSTEM SHALL return `THREAD_BLOCKED` without persisting the message and without starting the wait.
- **REQ-AUT-003** · MUST · event — WHEN a client invokes `block.wait_output` with a regular expression, THE SYSTEM SHALL evaluate the pane's recent output line by line and return the first matching line with its block.
- **REQ-AUT-004** · MUST · unwanted — IF a wait reaches its timeout, THEN THE SYSTEM SHALL return `TIMEOUT` including the last observed state, and SHALL NOT resend the prompt or retry the operation.

- **REQ-AUT-005** · MUST · unwanted — IF a connection exceeds the limit of concurrent waits, THEN THE SYSTEM SHALL reject the new wait with `VALIDATION_ERROR` naming the limit; IF a `source` exceeds the report rate, THEN THE SYSTEM SHALL reply `ok` and discard the report. In neither case SHALL it close the connection or affect other sources.
- **REQ-AUT-006** · MUST · event — WHEN a client invokes `wait.list`, THE SYSTEM SHALL return every active wait with its identifier, owning connection, target thread or block, target states, age and whether it is marked stalled.
- **REQ-AUT-007** · MUST · event — WHEN a client invokes `wait.cancel`, THE SYSTEM SHALL end that wait with `CANCELLED` for whoever is waiting, without touching the turn, the process or the pane it was observing.
- **REQ-AUT-008** · MUST · unwanted — IF a running turn produces no model event, tool call or output for the stall window (120 s for local models, 30 s for remote ones, configurable), THEN THE SYSTEM SHALL mark it `stalled`, notify it and expose it in `wait.list` and `thread.list`, without cancelling it: the user decides between resuming and cancelling.

### 6.12 Integrations and external state (INT)

- **REQ-INT-001** · MUST · ubiquitous — THE SYSTEM SHALL inject `UMBRAL_ENV=1`, `UMBRAL_SOCKET_PATH`, `UMBRAL_BIN_PATH`, `UMBRAL_WORKSPACE_ID`, `UMBRAL_TAB_ID`, `UMBRAL_PANE_ID` and `UMBRAL_SESSION_ID` into every process it launches in a pane, and its own variables SHALL take precedence over caller-supplied values.
- **REQ-INT-002** · MUST · event — WHEN an external process invokes `pane.report_state` with a stable `source`, THE SYSTEM SHALL record that semantic state for the pane and use it for rollups, waits and notifications.
- **REQ-INT-003** · MUST · unwanted — IF a report from the same `source` arrives with a sequence number lower than or equal to the last accepted one, THEN THE SYSTEM SHALL accept the request and discard the update.
- **REQ-INT-004** · MUST · ubiquitous — THE SYSTEM SHALL keep display metadata (title, displayed name, state labels and tokens with TTL) separate from semantic state, and metadata SHALL never alter waits, rollups or notifications.
- **REQ-INT-005** · MUST · event — WHEN a `source` invokes `pane.release_state`, THE SYSTEM SHALL drop that source's authority over the pane and fall back to its own detection.

- **REQ-INT-006** · MUST · unwanted — IF an external `source` tries to report state for a pane that hosts a thread of Umbral's own agent, THEN THE SYSTEM SHALL reply `PERMISSION_DENIED` and keep its own authority; that pane accepts display metadata only.

### 6.13 Notifications (NTF)

- **REQ-NTF-001** · MUST · event — WHEN a component requests a notification, THE SYSTEM SHALL normalize the text (strip control characters, collapse whitespace, cap the title at 80 and the body at 240 characters), deliver it to the foreground client and return a typed reason among `shown`, `disabled`, `rate_limited` and `no_client`.
- **REQ-NTF-002** · MUST · unwanted — IF one `source` requests more than 5 notifications within 60 s, THEN THE SYSTEM SHALL discard the excess and return `rate_limited` without blocking other sources.

### 6.14 Packaging and identity (PKG)

Folded from `changes/_archive/2026-09-visual-identity/`. REQ-PKG-004, REQ-PKG-005, REQ-PKG-007
and REQ-PKG-008 depend on the desktop client or on a release channel this MVP does not have, and
live in `specs/prd/umbral-f2-desktop.md` (finding A-12); their IDs stay reserved here.

- **REQ-PKG-001** · MUST · ubiquitous — THE SYSTEM SHALL install its icon in the `hicolor` theme with the name `io.github.ecrespo.Umbral`, in `scalable/apps` (SVG) and as PNG at 16, 22, 24, 32, 48, 64, 128, 256 and 512 px, plus `symbolic/apps/io.github.ecrespo.Umbral-symbolic.svg`.
- **REQ-PKG-002** · MUST · ubiquitous — THE SYSTEM SHALL install `io.github.ecrespo.Umbral.desktop` with `Icon=io.github.ecrespo.Umbral` and `StartupWMClass=io.github.ecrespo.Umbral`, and that file SHALL pass `desktop-file-validate` without errors.
- **REQ-PKG-003** · MUST · optional — WHERE only the TUI is distributed (release 0.1), the `.desktop` file SHALL use `Exec=umbral-tui` with `Terminal=true`.
- **REQ-PKG-006** · MUST · unwanted — IF a build pipeline tries to regenerate `icon.ico` or `icons.icns` from a single PNG, THEN the build SHALL use the kit's files and fail if their SHA-256 differ from `assets/branding/umbral-icons/CHECKSUMS.sha256`.

### 6.15 Observability (OBS)

- **REQ-OBS-001** · MUST · event — WHEN a turn runs, THE SYSTEM SHALL emit an OTel trace with one span per model call and one per tool, with the GenAI attributes for model, provider and tokens.
- **REQ-OBS-002** · MUST · ubiquitous — THE SYSTEM SHALL expose the metric `umbral_tool_calls_invalid_total` labeled by model.
- **REQ-OBS-004** · MUST · ubiquitous — THE SYSTEM SHALL expose the metrics `umbral_waits_active`, `umbral_waits_stalled_total`, `umbral_reports_rate_limited_total` and `umbral_rule_updates_rejected_total`, labelled by reason where applicable.
- **REQ-OBS-003** · SHOULD · optional — WHERE `otel.endpoint` is configured, THE SYSTEM SHOULD export traces through OTLP.

## 7. Non-Functional Requirements

### Performance

Reference hardware: the automated gate runs on the CI runner (GitHub `ubuntu-latest`, 4 vCPU) and
manual validation runs on a ThinkPad P14s Gen 6 AMD. If the runner cannot reach a number, the gate
threshold is adjusted and the laptop figure stays as the product target, with both written down
(finding A-08).

- `session.create` < 300 ms p95 (REQ-TERM-001).
- Latency added by the daemon < 5 ms p95 (REQ-TERM-006).
- `block.search` < 200 ms p95 with 100,000 blocks (REQ-BLK-006).
- Idle daemon memory < 80 MiB with 5 sessions and no embedded model loaded.

### Security
- Local `0600` socket with a per-installation token (REQ-SEC-003, REQ-SEC-007).
- Secrets only in the keyring (REQ-SEC-004).
- Explicit consent for side effects (Art. 5).

### Availability
- A client crash does not affect sessions or threads.
- A daemon crash keeps closed blocks and thread history (SQLite WAL); live PTYs are lost in the MVP (durability across daemon restarts: F2).

### Portability
- Linux x86_64/arm64 (Wayland vs. X11 is irrelevant for the TUI) and macOS arm64.

### Observability
- Traces per turn, JSON logs with `trace_id`, usage metrics per provider (REQ-OBS-*).

## 8. Constraints and Dependencies

### Technical Constraints
- Stack fixed by the constitution (Go, libghostty, SQLite, JSON-RPC).
- libghostty-vt does not promise API stability: it is isolated behind the `Emulator` port and pinned.
- Building libghostty with cgo requires native builds per platform.

### Business Constraints
- Licenses: no AGPL code (Warp) or FSL code (Crush) is copied. Apache-2.0/MIT code is reused respecting its notices.

### External Dependencies

| Dependency | Type | Owner | Status | Risk |
|---|---|---|---|---|
| go-libghostty | library (cgo) | Mitchell Hashimoto / Ghostty | active, unstable API | High |
| charm.land/fantasy | library | Charm | active | Medium |
| modelcontextprotocol/go-sdk | library | MCP + Google | stable v1.x | Low |
| Ollama / llama.cpp / LM Studio | local service | third parties | stable | Medium (variable tool calling) |
| OpenRouter, HF router, OmniRoute | remote/self-hosted service | third parties | stable | Low |

## 9. User Stories

### Epic: Fix things without leaving the terminal

**US-001:** As a Tech Lead, I want every command to become a block with its exit code, so I can find and reuse results.
- Criteria: REQ-BLK-001, REQ-BLK-002, REQ-BLK-006, REQ-BLK-007.

**US-002:** As a Tech Lead, I want to attach a failed block to the agent with one action, so I don't have to copy and paste.
- Criteria: REQ-TUI-003, REQ-CTX-002.

**US-003 (reference scenario):** As a Tech Lead, in a Go repo with a broken test, I want to ask "fix it" and approve the diff, so I end with `go test ./...` green **offline**.
- Criteria: REQ-AGT-001…011, REQ-LLM-001, REQ-LLM-004, REQ-SEC-001.

**US-004:** As an SRE, I want to close the window and reopen it without losing my shells, so long-running processes are not interrupted.
- Criteria: REQ-TERM-003, REQ-TERM-004.

**US-005:** As a user, I want to pipe output to the agent from any terminal, so I can use it without the TUI.
- Criteria: REQ-CLI-001.

**US-006:** As a privacy-conscious user, I want to see what left my machine, so I can audit it.
- Criteria: REQ-SEC-001, REQ-SEC-002.

**US-007:** As a Tech Lead running several projects, I want one glance to tell me which workspace needs a decision, so I don't poll every terminal.
- Criteria: REQ-WS-006, REQ-AGT-016, REQ-NTF-001.

**US-008:** As a Tech Lead, I want a script (or another agent) to open a workspace, launch a thread and wait until it finishes or asks something, so I can automate routine work.
- Criteria: REQ-WS-001, REQ-AUT-001, REQ-AUT-002, REQ-API-001.

**US-009:** As an SRE, I want to reopen Umbral after a restart and find my project structure, so I don't rebuild it by hand.
- Criteria: REQ-TERM-009, REQ-AGT-017.

**US-010:** As a security-minded user, I want to ask why a command needed approval, so I can tune my rules with evidence.
- Criteria: REQ-SEC-009, REQ-SEC-010.

## 10. Wireframes / Mockups

TUI as text:

```
┌ tabs: [1 api] [2 infra] ──────────────────────────────────── ollama/gpt-oss:20b ┐
│ ▸ go test ./...                        ✗ exit 1 · 4.2s            [@ attach]      │
│   --- FAIL: TestParse (0.00s)                                                     │
│ ▸ git status                           ✓ exit 0 · 0.1s                            │
├───────────────────────────────── agent · mode auto-edit ──────────────────────────┤
│ Edited parser.go:42 (diff below). Now I want to run the tests…                    │
│ ⚠ Approval: run_command `go test ./...`   [a]pprove  [d]eny  a[l]ways             │
│ > fix it @block:blk_01J9…                                        ctrl+space ⇄ shell│
└───────────────────────────────────────────────────────────────────────────────────┘
```

## 10.1 Glossary

| Term | Definition |
|---|---|
| Workspace | Top-level container, usually one repository or investigation. Owns tabs and panes and exposes a rollup state. |
| Tab | A layout inside a workspace: a binary tree of panes. |
| Pane | A terminal inside a tab. It hosts at most one session at a time. |
| Session | A PTY with its VT emulator state. |
| Write root | The thread's write boundary: the git root of its cwd, or the cwd itself when there is no repository (REQ-AGT-013). Named this way, rather than "workspace", so it is not confused with the structural workspace of the WS area (finding B-09). |
| Attention state | `blocked`, `working`, `done`, `idle`: what the sidebar rolls up and what waits observe. |
| Semantic state vs. metadata | Semantic state drives waits, rollups and notifications; metadata only changes what is displayed. |

## 11. Risks and Mitigations

| Risk | Probability | Impact | Mitigation |
|---|---|---|---|
| Weak tool calling in local models | High | High | Repair (REQ-AGT-006), class fallback (REQ-LLM-003), metric REQ-OBS-002 |
| go-libghostty API changes | Medium | High | `Emulator` port, pinned version, conformance suite |
| Prompt injection from output or web content | Medium | High | Taint (REQ-SEC-006), destructive patterns (REQ-SEC-005) |
| Excessive scope | High | Medium | MVP = F0 + F1; everything else "Out of Scope" |

## 12. Estimated Timeline

| Phase | Estimated duration | Deliverable |
|---|---|---|
| Spec & Design | 1 week | Approved specs + Analyze without CRITICAL findings |
| F0 Core | 4-5 weeks | Daemon + TUI with blocks and scriptable structure |
| F1 Agentic | 6-7 weeks | US-003 scenario offline, thread drivable from scripts |
| Hardening | 1-2 weeks | Release 0.1 (Linux + macOS) |

---

## Constitution check

- **Art. 2:** every MUST REQ has a task and a test in `specs/tasks/`.
- **Art. 4:** REQ-LLM-004, REQ-SEC-001 and REQ-SEC-002 make offline mode and egress auditing verifiable.
- **Art. 5:** REQ-AGT-004/005/009 and REQ-SEC-005/006 cover permissions and sandboxing.
- **Art. 6:** cost in micro-USD (REQ-LLM-005). REQ-WS-002's structural identifiers are the exception the Art. 6 amendment of 2026-09-20 authorises; every other public identifier is a prefixed ULID.
- **Art. 8:** REQ-API-002/003/004 make the contract explicit: sequenced events, a missing method degrades one action instead of the connection, and the schema is published from the binary itself.
- **Art. 4 check on REQ-SEC-011:** remote rule updates are disabled by default, are configured explicitly by the user, and every fetch is recorded in `egress_log`, so offline operation and egress auditing are preserved. No amendment is required.
- **Art. 5 check on REQ-INT-002:** an external report changes the *displayed and awaited* state of a pane; it never grants tool permissions, which keep flowing through the policy engine.
- **Art. 5 (amended 2026-09-20):** REQ-SEC-012 allows `env:<VAR>` only when the keyring is unavailable, the fallback is enabled explicitly and the state is reported as degraded. REQ-SEC-011 and REQ-SEC-013 require a valid signature for any rule material arriving over the network.
- **Requested exception:** Windows is out of the MVP. The constitution sets it from F2, so this is not a violation.

## Change History

| Version | Date | Author | Changes |
|---|---|---|---|
| 1.0 | 2026-09-11 | E. Crespo (assisted draft) | Initial version from ARCHITECTURE v0.1 |
| 1.1 | 2026-09-11 | E. Crespo (assisted draft) | delta `2026-09-analyze-fixes`: REQ-SEC-008 (degraded start without a keyring, A-03) and REQ-AGT-015 (`thread.send` idempotency, A-04) |
| 1.2 | 2026-09-11 | E. Crespo (assisted draft) | delta `2026-09-visual-identity`: §6.10 with REQ-PKG-001, 002, 003 and 006; REQ-PKG-004, 005, 007 and 008 moved to the F2 PRD per finding A-12 |
| 1.3 | 2026-09-11 | E. Crespo (assisted draft) | delta `2026-09-block-lifecycle-decisions`: REQ-BLK-003 gains the late-marker promotion and the one-way rule |
| 1.4 | 2026-09-20 | E. Crespo (assisted draft) | Adds the WS, API, AUT, INT and NTF areas, REQ-TERM-009/010, REQ-AGT-016/017 and REQ-SEC-009/010/011, plus the glossary. Rationale in `docs/adr/ADR-0002-orchestration-surface.md` |
| 1.8 | 2026-09-20 | E. Crespo (assisted draft) | delta `2026-09-notification-sequencing`: REQ-API-002 counts per daemon run, shared by every connection, instead of "per session" — a word that in this system already names a PTY |
| 1.7 | 2026-09-20 | E. Crespo (assisted draft) | delta `2026-09-cli-surface`: REQ-CLI-002 says what the current session is and what happens without one; REQ-CLI-004 fixes the `umb` exit codes |
| 1.6 | 2026-09-20 | E. Crespo (assisted draft) | delta `2026-09-art6-structural-ids`: the Constitution check names the Art. 6 exception for structural identifiers (C-05) |
| 1.5 | 2026-09-20 | E. Crespo (assisted draft) | Closes the Analyze findings: folds the two pending deltas (REQ-SEC-008, REQ-AGT-015, PKG area), adds signing and key management (SEC-011/013/014/015/016), the environment fallback (SEC-012), wait monitoring (AUT-005…008), `fetch_url` limits (AGT-018), authority for its own agent (INT-006), metrics (OBS-004), reference hardware and redaction thresholds |

## Approvals

| Role | Name | Date | Status |
|---|---|---|---|
| Product Owner | Ernesto Crespo | | ☐ Pending |
| Tech Lead | Ernesto Crespo | | ☐ Pending |
