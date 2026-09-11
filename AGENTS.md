# AGENTS.md — Umbral

Guide for any coding agent (Claude Code, Codex, Gemini CLI, OpenCode…) and for people new to the
repo. `CLAUDE.md` imports this file.

## What this repo is
Umbral is a local-first agentic terminal in Go. It consists of:
- `umbrald`: a daemon with PTY, VT (libghostty), blocks, agent, model gateway and MCP;
- thin clients: Bubble Tea v2 TUI, `umb` CLI and, from F2, a Wails v3 desktop app.

Today the repo is in the **specification phase**; code starts with `T-F0-01`.

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
| Lint + format + gosec | `task lint` | after T-F0-01 |
| Architecture boundaries | `task arch` | after T-F0-01 |
| Tests with the race detector | `task test` (≈ `go test -race ./...`) | after T-F0-01 |
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
- The 2026-09-11 Analyze has **1 CRITICAL** (A-01: FK to `threads` before its migration) and 3 HIGH (A-02 VT suite, A-03 missing keyring, A-04 `thread.send` idempotency).
- A draft Delta fixes them: `changes/2026-09-analyze-fixes/`. **It is approved and folded before T-F0-02.**
- Visual identity Delta (`changes/2026-09-visual-identity/`): in review; it is folded respecting finding A-12.
