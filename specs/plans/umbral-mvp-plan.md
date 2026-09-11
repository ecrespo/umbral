# Umbral MVP — Implementation Plan

## Metadata

| Field | Value |
|---|---|
| **Author** | Ernesto Crespo · assisted draft |
| **Status** | `DRAFT` |
| **Version** | 1.1 |
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

**Total estimated duration:** 10-12 weeks for one person part-time, plus supervised coding agents.
**Team:** 1 Tech Lead/backend (Go), supported by coding agents using the tasks in `specs/tasks/`.
**Release 0.1 target:** end of F1 + hardening.

## 2. Prerequisites

| Prerequisite | Owner | Status | Deadline |
|---|---|---|---|
| Specs approved (this package) + Analyze without CRITICAL findings | Tech Lead | ☑ Met 2026-09-11 — `analyze-2026-09-11b.md`, 0 CRITICAL / 0 HIGH | before T-F0-01 |
| Toolchain: Go ≥ 1.25, Zig (libghostty-vt build), golangci-lint, go-arch-lint, gitleaks | Tech Lead | ◐ Partial — Go 1.27.1, gofumpt, golangci-lint, go-arch-lint, govulncheck, gitleaks and Task installed; **Zig still missing** | T-F0-01 |
| Ollama with `gpt-oss:20b` and `num_ctx` ≥ 32k on the development machine | Tech Lead | ☐ Pending | start of F1 |
| CI runner with bash, zsh and fish installed | Tech Lead | ☐ Pending | T-F0-08 |
| Q-01 decision (snapshot format) | Tech Lead | ☐ Pending | spike T-F0-04 |

## 3. Implementation Phases

---

### Phase 0: Fold the pending deltas

**Duration:** < 1 day. **Goal:** `specs/` free of CRITICAL findings before the first line of Go.
Spec-only work: no product code.

| ID | Task | Dependency | Status |
|---|---|---|---|
| T-FIX-01 | Fold A-01 and A-07 into the Data Model (`threads` in migration 0001, FTS triggers) | — | ☑ 2026-09-11 |
| T-FIX-02 | Fold A-02: Tech Design §8.1 with the VT conformance cases | — | ☑ 2026-09-11 |
| T-FIX-03 | Fold A-03 and A-04: REQ-SEC-008, REQ-AGT-015, `client_msg_id` | T-FIX-01 | ☑ 2026-09-11 |
| T-FIX-04 | Fold A-05 and A-06: `owner_thread_id`, `env_refs` | — | ☑ 2026-09-11 |
| T-FIX-05 | Re-run the Analyze and archive the delta | T-FIX-01…04 | ☑ 2026-09-11 |
| T-PKG-01 | Visual identity: icon kit and pinned checksums | — | ☑ 2026-09-11 |
| T-PKG-02 | Visual identity: `.desktop` file for release 0.1 | T-PKG-01 | ☑ 2026-09-11 |

**Phase 0 "Done" criteria — all met on 2026-09-11:**
- `python3 tools/sdd_check.py` exits 0: 69 REQs, 64/64 MUST with a task and a matrix row.
- `node tools/mermaid_check.mjs` green.
- The visual-identity delta is folded honouring A-12 (REQ-PKG-004, 005, 007 and 008 moved to
  `specs/prd/umbral-f2-desktop.md` instead of becoming MVP MUSTs nothing could close),
  A-15 (checksums regenerated after the `.desktop` change) and A-16 (the superseded Spanish
  copy of the delta deleted).
- `changes/` holds no pending delta; all three are in `changes/_archive/`.

**Phase 0 is closed.** The remaining `changes/_archive/2026-09-visual-identity` tasks T-PKG-03,
04 and 05 are marked blocked, not pending: T-PKG-03 needs a package to build (hardening phase)
and the other two need the F2 desktop client.

---

### Phase F0: Terminal core and blocks

**Duration:** 3-4 weeks.
**Goal:** daemon with locally durable sessions, blocks, search, basic CLI and a usable TUI.

| ID | Task | Estimate | Dependency | Status |
|---|---|---|---|---|
| T-F0-01 | Scaffolding, CI gate (Art. 1) and `go-arch-lint` rules (Art. 3) | 1d | — | ☑ 2026-09-11 |
| T-F0-02 | Store: migration 0001 (terminal) and restart recovery | 1.5d | T-F0-01 | ☑ 2026-09-11 |
| T-F0-03 | JSON-RPC API: 0600 socket, token, `system.hello`/`status`, bus | 2d | T-F0-01 | ☑ 2026-09-11 |
| T-F0-04 | Spike Q-01: VT snapshot with libghostty | 1d | T-F0-01 | ☐ |
| T-F0-05 | Sessions: PTY, `Emulator` port, create/list/input/resize/close, lock | 3d | T-F0-02, T-F0-03 | ☐ |
| T-F0-06 | Subscription, snapshot, fan-out with batching and per-client queue | 2d | T-F0-04, T-F0-05 | ☐ |
| T-F0-07 | VT conformance suite | 2d | T-F0-05 | ☐ |
| T-F0-08 | Shell-integration bootstrap for bash/zsh/fish | 1.5d | T-F0-01 | ☐ |
| T-F0-09 | Block lifecycle from OSC; plain text; chunks | 3d | T-F0-05, T-F0-08 | ☐ |
| T-F0-10 | `block.list`/`get`/`search` + 100k benchmark | 1.5d | T-F0-09 | ☐ |
| T-F0-11 | `umb` CLI: autostart, `status`, `block last --json` | 1d | T-F0-10 | ☐ |
| T-F0-12 | Base TUI: tabs, splits, rendering, block navigation | 4d | T-F0-06, T-F0-09 | ☐ |
| T-F0-13 | TERM-001 and TERM-006 performance gates in CI | 1d | T-F0-06 | ☐ |

**F0 "Done" criteria:**
- VT conformance suite 100 % MUST green (REQ-TERM-002).
- Correct blocks in bash, zsh and fish in CI.
- The TUI is used for one week as the main terminal without blocking regressions.
- Benchmarks within the NFRs.

---

### Phase F1: Agent, models, MCP and security

**Duration:** 5-6 weeks.
**Goal:** US-003 scenario offline with approval; `umb ai` CLI; MCP; egress audit.

| ID | Task | Estimate | Dependency | Status |
|---|---|---|---|---|
| T-F1-01 | Migration 0002 (agent, models, audit, MCP) | 1d | F0 | ☐ |
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
| T-F1-22 | Hardening: retention job, recovery verification, user docs | 2d | T-F1-21 | ☐ |

**F1 "Done" criteria:**
- Every PRD MUST with a green test (matrices in the tasks files).
- US-003 ≥ 70 % success over 20 offline runs with `gpt-oss:20b`.
- 0 remote requests without an `egress_log` entry in the E2E suite.

---

### Hardening phase and release 0.1 (1-2 weeks)

- `.deb` and tar.gz for Linux, tar.gz for macOS.
- `systemd --user` unit and `launchd` plist.
- Installation guide and provider configuration guide (includes HF and OmniRoute presets).
- `code-audit` on the repo (SAST, SCA and secrets) as a release gate.

## 4. Horizon (outside this plan)

| Phase | Content | Artifacts it will require |
|---|---|---|
| F2 ADE | Wails v3 client, Full Terminal Use, Active AI + yzma, policy router, multi-thread worktrees, ACP client, durable SSH, Windows | F2 PRD + deltas on the API and Tech Design; **visual identity delta already proposed** (`changes/2026-09-visual-identity/`) |
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
