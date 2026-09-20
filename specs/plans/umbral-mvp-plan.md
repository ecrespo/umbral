# Umbral MVP — Implementation Plan

## Metadata

| Field | Value |
|---|---|
| **Author** | Ernesto Crespo · assisted draft |
| **Status** | `DRAFT` |
| **Version** | 1.4 |
| **Date** | 2026-09-11 |
| **PRD** | `specs/prd/umbral-mvp.md` |
| **Tech Design** | `specs/technical/umbral-architecture.md` |
| **Data Model** | `specs/data-model/umbral-schema.md` |
| **API Spec** | `specs/api/umbral-daemon-api-v1.md` |
| **Tasks** | `specs/tasks/umbral-f0-tasks.md`, `specs/tasks/umbral-f1-tasks.md` |

---

## 1. Implementation Summary

There are two MVP phases in sequence (F0 Core → F1 Agentic) plus a hardening phase.

- Each phase ends in something **usable daily**:
  - F0: the TUI replaces the usual terminal for work without the agent;
  - F1: the US-003 scenario works offline.
- F2 and F3 are described at the end only as a horizon; they will get their own PRDs/deltas.

**Total estimated duration:** 13-16 weeks for one person part-time, plus supervised coding agents.
**Team:** 1 Tech Lead/backend (Go), supported by coding agents using the tasks in `specs/tasks/`.
**Release 0.1 target:** end of F1 + hardening.

## 2. Prerequisites

| Prerequisite | Owner | Status | Deadline |
|---|---|---|---|
| Specs approved (this package) + Analyze without CRITICAL findings | Tech Lead | ✅ 2026-09-20 | closed: the gate passes and every delta raised so far is ratified and archived |
| Toolchain: Go ≥ 1.25, Zig (libghostty-vt build), golangci-lint, go-arch-lint, gitleaks | Tech Lead | ✅ 2026-09-11 | closed in T-F0-01; Go 1.27.1 and Zig on the development machine |
| Ollama with `gpt-oss:20b` and `num_ctx` ≥ 32k on the development machine | Tech Lead | ☐ Pending | start of F1 |
| CI runner with bash, zsh and fish installed | Tech Lead | ✅ 2026-09-11 | closed in T-F0-08: the Linux test job installs zsh and fish, and the tests skip a shell that is absent rather than failing, so macOS runs bash only |
| Q-01 decision (snapshot format) | Tech Lead | ✅ 2026-09-11 | closed in T-F0-04: `FormatterFormatVT` is replayable, no fallback needed (`docs/spikes/q01-snapshot.md`) |

## 3. Implementation Phases

---

### Phase 0: Fold the pending deltas — **closed 2026-09-11**

Spec-only work, no product code. T-FIX-01…05 folded Analyze findings A-01…A-07 and A-13;
T-PKG-01 and T-PKG-02 folded the visual identity, honouring A-12 (REQ-PKG-004, 005, 007 and 008
moved to `specs/prd/umbral-f2-desktop.md`), A-15 (checksums regenerated after the `.desktop`
change) and A-16. All seven are `☑ 2026-09-11`; the deltas are in `changes/_archive/`. T-PKG-01
and T-PKG-02 now live in `specs/tasks/umbral-hardening-tasks.md` with the rest of the packaging
work, and T-PKG-03 is blocked on a package to build rather than pending.

---

### Phase F0: Terminal core and blocks

**Duration:** 4-5 weeks.
**Goal:** daemon with locally durable sessions, workspace structure, blocks, search, basic CLI and a usable TUI, plus a protocol usable from scripts.

| ID | Task | Estimate | Dependency | Status |
|---|---|---|---|---|
| T-F0-01 | Scaffolding, CI gate (Art. 1) and `go-arch-lint` rules (Art. 3) | 1d | — | ✅ 2026-09-11 |
| T-F0-02 | Store: migration 0001 (terminal) and restart recovery | 1.5d | T-F0-01 | ✅ 2026-09-11 |
| T-F0-03 | JSON-RPC API: 0600 socket, token, `system.hello`/`status`, bus | 2d | T-F0-01 | ✅ 2026-09-11 |
| T-F0-04 | Spike Q-01: VT snapshot with libghostty | 1d | T-F0-01 | ✅ 2026-09-11 |
| T-F0-05 | Sessions: PTY, `Emulator` port, create/list/input/resize/close, lock | 3d | T-F0-02, T-F0-03 | ✅ 2026-09-11 |
| T-F0-06 | Subscription, snapshot, fan-out with batching and per-client queue | 2d | T-F0-04, T-F0-05 | ✅ 2026-09-11 |
| T-F0-07 | VT conformance suite | 2d | T-F0-05 | ✅ 2026-09-11 |
| T-F0-08 | Shell-integration bootstrap for bash/zsh/fish | 1.5d | T-F0-01 | ✅ 2026-09-11 |
| T-F0-09 | Block lifecycle from OSC; plain text; chunks | 3d | T-F0-05, T-F0-08 | ✅ 2026-09-11 |
| T-F0-10 | `block.list`/`get`/`search` + 100k benchmark | 1.5d | T-F0-09 | ✅ 2026-09-11 |
| T-F0-11 | `umb` CLI: autostart, `status`, `block last --json` | 1d | T-F0-10 | ✅ 2026-09-20 |
| T-F0-12 | Base TUI: tabs, splits, rendering, block navigation | 4d | T-F0-06, T-F0-09 | ✅ 2026-09-20 |
| T-F0-13 | TERM-001 and TERM-006 performance gates in CI | 1d | T-F0-06 | 🔄 gates written and self-tested locally; the CI job has not run yet |
| T-F0-14 | Workspace, tab and pane model with identifiers and rollup | 3d | T-F0-03, T-F0-05 | ☐ |
| T-F0-15 | Portable layouts (`layout.export` / `apply`) | 1.5d | T-F0-14 | ☐ |
| T-F0-16 | `session.snapshot` and event sequencing | 1.5d | T-F0-14 | ☐ |
| T-F0-17 | Protocol schema and capability degradation | 1.5d | T-F0-03 | ☐ |
| T-F0-18 | Structure restore after restart (+ optional pane history) | 2d | T-F0-14 | ☐ |

**F0 "Done" criteria:**
- VT conformance suite 100 % MUST green (REQ-TERM-002) — ☑ met by T-F0-07, 20 MUST cases.
- Correct blocks in bash, zsh and fish in CI — ☑ met by T-F0-09,
  `TestBlocksInEveryShell_REQ_BLK_005`; the Linux test job installs all three shells.
- The TUI is used for one week as the main terminal without blocking regressions.
- A script creates a workspace, splits, exports and reapplies a layout using only the CLI.
- After `kill` on the daemon, the structure comes back with its cwd and labels.
- Benchmarks within the NFRs.

---

### Phase F1: Agent, models, MCP and security

**Duration:** 7-8 weeks.
**Goal:** US-003 scenario offline with approval; `umb ai` CLI; MCP; egress audit; a thread that another agent or a script can drive.

| ID | Task | Estimate | Dependency | Status |
|---|---|---|---|---|
| T-F1-01 | Migration 0004 (agent, models, audit, MCP) | 1d | F0 | ☐ |
| T-F1-02 | Keyring and config loader that rejects plaintext secrets | 1d | T-F1-01 | ☐ |
| T-F1-03 | Policy engine (pure function) | 2d | T-F1-01 | ☐ |
| T-F1-04 | Secret redaction | 1.5d | T-F1-01 | ☐ |
| T-F1-05 | `llmgw`: ports, catalog, openai-compat adapters through Fantasy | 3d | T-F1-02 | ☐ |
| T-F1-06 | Native Ollama adapter | 1.5d | T-F1-05 | ☐ |
| T-F1-07 | Router, fallback, `usage`, redaction and egress hooks | 2d | T-F1-04, T-F1-06 | ☐ |
| T-F1-08 | HF router and OmniRoute presets | 0.5d | T-F1-05 | ☐ |
| T-F1-09 | Tool registry + built-in file/search/network tools | 3d | T-F1-03 | ☐ |
| T-F1-10 | `run_command` in the thread PTY as an agent block | 1.5d | T-F1-09 | ☐ |
| T-F1-11 | Context: rules, attachments, git | 2d | T-F1-01 | ☐ |
| T-F1-12 | Token budget and compaction | 2d | T-F1-11 | ☐ |
| T-F1-13 | Agent runtime and `thread.*` methods | 4d | T-F1-07, T-F1-10, T-F1-12 | ☐ |
| T-F1-14 | Approval flow | 2d | T-F1-13 | ☐ |
| T-F1-15 | Repair of invalid arguments + metric | 1d | T-F1-13 | ☐ |
| T-F1-16 | Cancellation < 500 ms | 1d | T-F1-13 | ☐ |
| T-F1-17 | MCP client | 3d | T-F1-09 | ☐ |
| T-F1-18 | OTel observability | 1.5d | T-F1-13 | ☐ |
| T-F1-19 | `umb ai` with stdin | 1d | T-F1-13 | ☐ |
| T-F1-20 | TUI: agent panel, mode toggle, approvals, attach block | 4d | T-F1-14 | ☐ |
| T-F1-21 | Live US-003 E2E (20 runs) and report | 1.5d | T-F1-20 | ☐ |
| T-F1-23 | Wait engine (`thread.wait`, `send --wait`, `block.wait_output`) | 2.5d | T-F1-13 | ☐ |
| T-F1-24 | Attention state (`done`/seen) and thread resume after restart | 1.5d | T-F1-13 | ☐ |
| T-F1-25 | Integration surface: env, `report_state`, authority | 2d | T-F1-13 | ☐ |
| T-F1-26 | Display metadata and tokens | 1d | T-F1-25 | ☐ |
| T-F1-27 | Notifications with normalization and rate limit | 1d | T-F1-24 | ☐ |
| T-F1-28 | `policy.explain` | 1d | T-F1-03 | ☐ |
| T-F1-29 | Rule overrides and optional updates | 1.5d | T-F1-04 | ☐ |
| T-F1-30 | Signed rule bundles, trust store and offline recovery | 2.5d | T-F1-29 | ☐ |
| T-F1-31 | Wait monitoring: inventory, cancellation and stall detection | 2d | T-F1-23 | ☐ |
| T-F1-22 | Hardening: retention job, recovery verification, user docs | 2d | T-F1-21 | ☐ |

**F1 "Done" criteria:**
- Every PRD MUST with a green test (matrices in the tasks files).
- US-003 ≥ 70 % success over 20 offline runs with `gpt-oss:20b`.
- 0 remote requests without an `egress_log` entry in the E2E suite.
- A shell script drives a full thread with `thread.send --wait` and `thread.wait` without polling.
- An external process reports state, appears in the rollup and releases authority.

---

### Hardening phase and release 0.1 (1-2 weeks)

Tasks in `specs/tasks/umbral-hardening-tasks.md`: T-PKG-01 (kit and checksums), T-PKG-02
(`.desktop` for 0.1, regenerating checksums) and T-PKG-03 (installation in the `.deb`).

- `.deb` and tar.gz for Linux, tar.gz for macOS.
- `systemd --user` unit and `launchd` plist.
- Installation guide and provider configuration guide (includes HF and OmniRoute presets).
- `code-audit` on the repo (SAST, SCA and secrets) as a release gate.

## 4. Horizon (outside this plan)

| Phase | Content | Artifacts it will require |
|---|---|---|
| F2 ADE | Wails v3 client, Full Terminal Use, Active AI + yzma, policy router, multi-thread worktrees, ACP client, durable SSH, Windows | F2 PRD + deltas on the API and Tech Design; REQ-PKG-004/005/007 and tasks T-PKG-04/05 are reserved for that PRD (finding A-12) |
| F2 orchestration | Third-party agent detection (process + declarative manifests with local override and `umb agent explain`), worktrees as a daemon primitive with provenance and group close, plugins with a declarative manifest before WASM, pane graphics capability, live PTY handoff across daemon versions, multi-machine federation over SSH with independent reconnects | F2 PRD; the capability names are already reserved in API Spec §9 |
| F3 | background agents, MCP/ACP server, WASM plugins, workflows/notebooks | F3 PRD |

## 5. Execution strategy with agents

- **First supervised batch:** T-F0-01, T-F0-02, T-F0-03 and T-F0-04. Review, adjust the specs if needed, then scale.
- **Parallelizable** (no shared files): T-F0-04 ∥ T-F0-08; T-F0-07 ∥ T-F0-09; T-F1-03 ∥ T-F1-04 ∥ T-F1-11; T-F1-08 ∥ T-F1-09.
- **Resuming a session:** each task is marked in its tasks file. If a task reveals a spec error, a Delta is opened before continuing.

## 6. Plan risks

| Risk | Mitigation | Trigger |
|---|---|---|
| Q-01 negative (no VT snapshot) | Fallback: replay the last N lines of the raw buffer | result of spike T-F0-04 |
| Building libghostty with Zig in CI | CI image with pinned Zig; artifact cache | T-F0-01 failure |
| Local models below 70 % on US-003 | Try Qwen3-Coder; tune system prompts; revisit the threshold through a Delta | T-F1-21 report |

## Constitution check

- **Art. 1 and 3:** T-F0-01 installs the gate and the architecture rules before any domain code.
- **Art. 2:** traceability matrices in the tasks files.
- **Art. 9:** the first batch is bounded; spec changes during implementation go through a Delta.

## Change History

| Version | Date | Author | Changes |
|---|---|---|---|
| 1.0 | 2026-09-11 | E. Crespo (assisted draft) | Initial version |
| 1.1 | 2026-09-11 | E. Crespo (assisted draft) | Phase 0 added (delta folding); the Analyze prerequisite is met |
| 1.2 | 2026-09-11 | E. Crespo (assisted draft) | delta `2026-09-block-query-performance`: the agent migration is renumbered because `idx_blocks_started` takes one of its own |
| 1.3 | 2026-09-20 | E. Crespo (assisted draft) | Adds T-F0-14…18 and T-F1-23…29, extends the phase durations and the F2 orchestration horizon |
| 1.4 | 2026-09-20 | E. Crespo (assisted draft) | Adds T-F1-30/31 and the hardening tasks file; F1 grows to 7-8 weeks; the deltas are folded and archived; the structure tables take migration `0003` and the agent subdomain `0004` |
