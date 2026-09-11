# AGENTS.md — Umbral

Guide for any coding agent (Claude Code, Codex, Gemini CLI, OpenCode…) and for people new to the
repo. `CLAUDE.md` imports this file.

## What this repo is
Umbral is a local-first agentic terminal in Go. It consists of:
- `umbrald`: a daemon with PTY, VT (libghostty), blocks, agent, model gateway and MCP;
- thin clients: Bubble Tea v2 TUI, `umb` CLI and, from F2, a Wails v3 desktop app.

The second-pass Analyze cleared F0 to start, and implementation has begun: T-F0-01 through
T-F0-09 are done, so the daemon owns sessions, streams their output and records blocks.
T-F0-10 (block query and search), T-F0-11 (`umb`), T-F0-12 (TUI) and T-F0-13 (performance
gates) remain. The specs themselves are still `DRAFT`: the Analyze presents findings, it approves nothing. The execution
log at the bottom of `specs/tasks/umbral-f0-tasks.md` is the only trustworthy record of what is
complete; the task file's `[x]` markers are the source of truth, not this paragraph.

## Mandatory reading order
1. `specs/constitution.md`: 9 non-negotiable articles. A violation blocks the merge.
2. The task in `specs/tasks/umbral-f0-tasks.md` / `umbral-f1-tasks.md` (or `changes/*/tasks.md`).
3. Only the spec sections the task touches:
   - `specs/prd/umbral-mvp.md` (EARS criteria per REQ);
   - `specs/api/umbral-daemon-api-v1.md` (JSON-RPC contract);
   - `specs/technical/umbral-architecture.md` (DD-001…008, boundary rules §5.2, policies §5.3);
   - `specs/data-model/umbral-schema.md` (SQLite DDL).

## Commands

| Purpose | Command | Available |
|---|---|---|
| REQ → task coverage, ghost REQs, per-migration DDL | `python3 tools/sdd_check.py` | now |
| Validate Mermaid diagrams | `npm install --prefix tools && node tools/mermaid_check.mjs` | now |
| Both spec checkers at once | `task specs` | now |
| Lint + format + gosec | `task lint` | now |
| Architecture boundaries | `task arch` | now |
| Prove the boundaries reject a violation | `task arch:selftest` | now |
| Tests with the race detector | `task test` (≈ `go test -race ./...`) | now |
| Everything the pipeline runs | `task ci` | now |
| Install the Go tools the gate needs | `task tools:install` | now |
| Build libghostty-vt, the cgo dependency (needs Zig ≥ 0.16) | `task deps:ghostty` | now |
| Tests against real models | `go test -tags live ./...` | F1 |

## Working rules
- **One task per branch and per PR.** Branch `feat/T-F0-03-<slug>`.
- **Tests first.** Every test cites its REQ in the name (`Test…_REQ_SEC_003`); use the exact name from the traceability matrix when it exists.
- **First supervised batch:** at most 3-5 tasks before a human review.
- **If the spec is wrong: stop.** Open a Delta in `changes/<YYYY-MM>-<slug>/` and do not continue until it is approved. Never let code and `specs/` diverge silently.
- **Mark progress** in the tasks file (`### [x] YYYY-MM-DD T-…`) and in its execution log.
- **Language:** everything in English: specs, docs, code, comments, commits and logs.
- **Commits:** Conventional Commits with the footers `Refs: T-…` and `REQ: …`.
- **Security:**
  - no secrets in code, config or logs (only `keyring:<path>`);
  - never run WriteFS/Exec/Network tools outside `security.Decide()`.
- **Mermaid:** every new or modified diagram is validated before it is accepted.
- **Checkpoint:** after multi-step work, verify against the filesystem what is actually complete (`docs/checkpoints/`).

## Known status (update it when findings are closed)
- The second-pass Analyze (`specs/analyze/analyze-2026-09-11b.md`, 2026-09-11) has **0 CRITICAL and 0 HIGH**. Verdict: **READY TO IMPLEMENT (F0)**.
- A-01 … A-07 and A-13 are folded into `specs/`; the delta lives in `changes/_archive/2026-09-analyze-fixes/`.
- Open findings, each attached to the task that first needs it: A-08 (reference machine, T-F0-13), A-09 (entropy and rules precedence, T-F1-04/T-F1-11), A-10 (SQLite write failure, T-F1-13), A-11 (`fetch_url` limits, T-F1-09), A-14 (glossary).
- Closed while folding the visual identity: A-12 (the four desktop PKG requirements moved to `specs/prd/umbral-f2-desktop.md`), A-15 (checksums regenerated) and A-16 (Spanish duplicate deleted).
- **Phase 0 is closed** and `changes/` holds no pending delta: all five are archived. The two
  raised during F0, `2026-09-slow-client-notification` (T-F0-06) and
  `2026-09-block-lifecycle-decisions` (T-F0-09), were approved and folded on 2026-09-11 into
  API Spec v1.3, Data Model v1.2, PRD v1.3 and Tech Design v1.3.
- **Q-01 is resolved** (`docs/spikes/q01-snapshot.md`): the libghostty VT formatter produces replayable snapshots, so the bounded-replay fallback is not needed and DD-001 stands unchanged.
- **Building needs libghostty-vt.** Run `task deps:ghostty` once; the Taskfile then points `PKG_CONFIG_PATH` at it, so no Go target needs you to export anything.
- `python3 tools/sdd_check.py` exits 0. Keep it that way: it is the *Specs* gate in CI.
