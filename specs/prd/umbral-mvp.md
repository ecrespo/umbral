# Umbral MVP — local-first agentic terminal

## Product Requirements Document (PRD)

| Field | Value |
|---|---|
| **Author** | Ernesto Crespo (Tech Lead) · assisted draft |
| **Status** | `DRAFT` |
| **Version** | 1.2 |
| **Date** | 2026-09-11 |
| **Reviewers** | pending |
| **Last updated** | 2026-09-11 |
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
- [ ] Blocks through shell integration (bash, zsh, fish) and FTS search.
- [ ] TUI client (Bubble Tea v2) with tabs, splits, block navigation and an agent panel.
- [ ] Agent runtime with built-in tools, modes, permissions and limits.
- [ ] Context: rules files, `@` attachments, git and compaction.
- [ ] Model gateway: Ollama, llama.cpp, LM Studio, OpenRouter and generic OpenAI-compatible; presets for the Hugging Face router and OmniRoute.
- [ ] MCP client (stdio and streamable HTTP).
- [ ] `umb` CLI (`ai`, `block`, `status`).
- [ ] Secret redaction, `egress_log`, keyring and an authenticated socket.
- [ ] OpenTelemetry traces per turn.

### 5.2 Out of Scope
- Wails v3 desktop GUI: phase F2. The TUI validates the core first.
- Full Terminal Use (the agent driving interactive programs): F2.
- Active AI and the embedded yzma model: F2.
- ACP client and server, MCP server: F2/F3.
- Parallel threads with worktrees and background agents: F2/F3.
- SSH with a remote agent and remote durability: F2.
- Windows: F2. It requires ConPTY and its own tests.
- Real-time collaboration and any cloud backend: outside the product.

### 5.3 Future Considerations
- Full policy router (cost/quality, per-provider circuit breakers): F2.
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

### 6.2 Blocks (BLK)

- **REQ-BLK-001** · MUST · event — WHEN the shell emits `OSC 133;C`, THE SYSTEM SHALL create a block in state `running` with the command (`OSC 633;E`), the cwd (`OSC 7`) and the start time.
- **REQ-BLK-002** · MUST · event — WHEN the shell emits `OSC 133;D;<exit>`, THE SYSTEM SHALL close the block with its exit code and duration, and publish `block.closed`.
- **REQ-BLK-003** · MUST · unwanted — IF a session emits no shell-integration sequences within the first 5 s, THEN THE SYSTEM SHALL mark it `integration: none` and keep delivering output without creating blocks.
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
- **REQ-AGT-013** · MUST · state — WHILE a thread is in `auto-edit` mode, THE SYSTEM SHALL allow without approval the `WriteFS` tools whose path is inside the thread's workspace and SHALL request approval (`ask`) for those pointing outside it.
- **REQ-AGT-014** · MUST · state — WHILE a thread is in `normal` mode (the default), THE SYSTEM SHALL apply `ask` to every tool that is not `ReadOnly`, unless the user has persisted `allow` rules.
- **REQ-AGT-015** · MUST · unwanted — IF `thread.send` arrives with a `client_msg_id` already processed in the same thread, THEN THE SYSTEM SHALL reply with the original `turn_id` and `message_id` without creating a new turn or running any tool again.
- **REQ-AGT-012** · SHOULD · event — WHEN `edit_file` or `write_file` requires approval, THE SYSTEM SHOULD include the unified diff of the proposed change in `approval.requested`.

### 6.4 Context (CTX)

- **REQ-CTX-001** · MUST · event — WHEN a turn's prompt is assembled, THE SYSTEM SHALL include the rules files `AGENTS.md`, `CLAUDE.md`, `WARP.md` and `CRUSH.md` (and their `.local.md` variants), searched from the repo root down to the thread's cwd, in that order of precedence.
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

- **REQ-SEC-001** · MUST · ubiquitous — THE SYSTEM SHALL apply the redaction rules (gitleaks-style patterns plus entropy) to all content before sending it to a provider, replacing every match with `[REDACTED:<rule>]`.
- **REQ-SEC-002** · MUST · event — WHEN a request is sent to a remote provider, THE SYSTEM SHALL record in `egress_log` the destination host, bytes sent, SHA-256 of the payload, provider and thread.
- **REQ-SEC-003** · MUST · unwanted — IF a socket connection does not present the valid local token in `system.hello`, THEN THE SYSTEM SHALL reply `UNAUTHORIZED` and close the connection.
- **REQ-SEC-004** · MUST · unwanted — IF the configuration contains a plaintext API key, THEN THE SYSTEM SHALL reject that provider entry and indicate that `keyring:<path>` must be used.
- **REQ-SEC-005** · MUST · ubiquitous — THE SYSTEM SHALL require approval for commands matching the destructive-pattern list (`rm -rf`, `git push --force`, `mkfs`, `dd of=`, `kubectl delete`, …), regardless of `allow` rules and the thread mode.
- **REQ-SEC-006** · MUST · state — WHILE a turn's context contains content marked as untrusted (results from `fetch_url` or from MCP servers with `trust = untrusted`), THE SYSTEM SHALL require approval for tools with risk `Exec` and `Network`.
- **REQ-SEC-007** · MUST · ubiquitous — THE SYSTEM SHALL create the socket with permissions `0600` in `$XDG_RUNTIME_DIR/umbral/`.
- **REQ-SEC-008** · MUST · unwanted — IF the operating system keyring is unavailable when the daemon starts (headless Linux without Secret Service, locked keychain), THEN THE SYSTEM SHALL start without aborting, disable every provider whose credential is `keyring:<path>`, mark its models `health = down` with reason `keyring_unavailable`, and show that reason in `umb status`.

### 6.7 MCP

- **REQ-MCP-001** · MUST · event — WHEN an MCP server is configured (stdio or streamable HTTP), THE SYSTEM SHALL connect it and expose its tools to the model with the prefix `mcp_<server>_<tool>`.
- **REQ-MCP-002** · MUST · event — WHEN a server is added with `mcp.server.add` during a thread, THE SYSTEM SHALL offer its tools from the next turn on without restarting the thread.
- **REQ-MCP-003** · MUST · unwanted — IF an MCP server crashes or does not respond within 10 s, THEN THE SYSTEM SHALL mark it `unavailable`, return an error to pending tool calls and retry the connection with exponential backoff (at most 5 attempts).
- **REQ-MCP-004** · MUST · ubiquitous — THE SYSTEM SHALL apply the `ask` policy to MCP tools by default, unless there are explicit per-server or per-tool rules.

### 6.8 CLI and TUI

- **REQ-CLI-001** · MUST · event — WHEN `umb ai "<prompt>"` runs with data on stdin, THE SYSTEM SHALL create an ephemeral thread in `ask` mode with stdin as an attachment (at most 1 MiB) and stream the response to stdout.
- **REQ-CLI-002** · MUST · event — WHEN `umb block last --json` runs, THE SYSTEM SHALL print the last closed block of the current session as JSON, following the API `Block` schema.
- **REQ-CLI-003** · MUST · unwanted — IF the daemon is not running, THEN `umb` SHALL try to start it and, if that fails within 3 s, exit with code 69 and an actionable message.
- **REQ-TUI-001** · MUST · ubiquitous — THE SYSTEM SHALL provide in the TUI tabs, splits, jumping between blocks and an agent panel with pending approvals.
- **REQ-TUI-002** · MUST · event — WHEN the user presses the mode shortcut (`ctrl+space` by default), the TUI SHALL toggle input between shell and agent.
- **REQ-TUI-003** · MUST · event — WHEN the user picks "attach to agent" on a block, the TUI SHALL open the agent panel with `@block:<id>` preloaded in the input.

### 6.9 Observability (OBS)

- **REQ-OBS-001** · MUST · event — WHEN a turn runs, THE SYSTEM SHALL emit an OTel trace with one span per model call and one per tool, with the GenAI attributes for model, provider and tokens.
- **REQ-OBS-002** · MUST · ubiquitous — THE SYSTEM SHALL expose the metric `umbral_tool_calls_invalid_total` labeled by model.
- **REQ-OBS-003** · SHOULD · optional — WHERE `otel.endpoint` is configured, THE SYSTEM SHOULD export traces through OTLP.

### 6.10 Packaging and identity (PKG)

Folded from `changes/_archive/2026-09-visual-identity/`. Finding A-12 of the 2026-09-11 Analyze
applies: REQ-PKG-004, 005 and 007 describe the desktop client and belong to F2, so they live in
`specs/prd/umbral-f2-desktop.md` and are **not** MVP MUSTs. Keeping them here would have put
requirements in the MVP that no MVP task can close.

- **REQ-PKG-001** · MUST · ubiquitous — THE SYSTEM SHALL install its icon in the `hicolor` theme with the name `io.github.ecrespo.Umbral`, in `scalable/apps` (SVG) and as PNG at 16, 22, 24, 32, 48, 64, 128, 256 and 512 px, plus `symbolic/apps/io.github.ecrespo.Umbral-symbolic.svg`.
- **REQ-PKG-002** · MUST · ubiquitous — THE SYSTEM SHALL install `io.github.ecrespo.Umbral.desktop` with `Icon=io.github.ecrespo.Umbral` and `StartupWMClass=io.github.ecrespo.Umbral`, and that file SHALL pass `desktop-file-validate` without errors.
- **REQ-PKG-003** · MUST · optional — WHERE only the TUI is distributed (release 0.1), the `.desktop` file SHALL use `Exec=umbral-tui` with `Terminal=true`.
- **REQ-PKG-006** · MUST · unwanted — IF a build pipeline tries to regenerate an icon artifact from `appicon.png`, THEN the build SHALL use the kit's files and fail if their SHA-256 differ from `assets/branding/umbral-icons/CHECKSUMS.sha256`.

## 7. Non-Functional Requirements

### Performance
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
| F0 Core | 3-4 weeks | Daemon + TUI with blocks |
| F1 Agentic | 5-6 weeks | US-003 scenario offline |
| Hardening | 1-2 weeks | Release 0.1 (Linux + macOS) |

---

## Constitution check

- **Art. 2:** every MUST REQ has a task and a test in `specs/tasks/`.
- **Art. 4:** REQ-LLM-004, REQ-SEC-001 and REQ-SEC-002 make offline mode and egress auditing verifiable.
- **Art. 5:** REQ-AGT-004/005/009 and REQ-SEC-005/006 cover permissions and sandboxing.
- **Art. 6:** cost in micro-USD (REQ-LLM-005).
- **Requested exception:** Windows is out of the MVP. The constitution sets it from F2, so this is not a violation.

## Change History

| Version | Date | Author | Changes |
|---|---|---|---|
| 1.0 | 2026-09-11 | E. Crespo (assisted draft) | Initial version from ARCHITECTURE v0.1 |

## Change History

| Version | Date | Changes |
|---|---|---|
| 1.0 | 2026-09-11 | Initial version |
| 1.2 | 2026-09-11 | delta `2026-09-visual-identity`: §6.10 with REQ-PKG-001, 002, 003 and 006; REQ-PKG-004, 005, 007 and 008 moved to the F2 PRD per finding A-12 |
| 1.1 | 2026-09-11 | delta `2026-09-analyze-fixes`: REQ-SEC-008 (degraded start without a keyring, A-03) and REQ-AGT-015 (`thread.send` idempotency, A-04) |

## Approvals

| Role | Name | Date | Status |
|---|---|---|---|
| Product Owner | Ernesto Crespo | | ☐ Pending |
| Tech Lead | Ernesto Crespo | | ☐ Pending |
