# AGENTS.md — Umbral

Guide for any coding agent (Claude Code, Codex, Gemini CLI, OpenCode…) and for people new to the
repo. `CLAUDE.md` imports this file.

## What this repo is
Umbral is a local-first agentic terminal in Go. It consists of:
- `umbrald`: a daemon with PTY, VT (libghostty), blocks, agent, model gateway and MCP;
- thin clients: Bubble Tea v2 TUI, `umb` CLI and, from F2, a Wails v3 desktop app.

The specification package is complete and **phase F0 is under way**: `T-F0-01` … `T-F0-10` are
implemented (daemon, store, JSON-RPC, PTY sessions, streaming, blocks and search). See "Known
status" at the end of this file for what is and is not written.

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

- **Implementation: T-F0-01 … T-F0-10 and T-PKG-01/02 are done** (see the execution log in `specs/tasks/umbral-f0-tasks.md`). `umbrald` owns real PTY sessions, streams them, records blocks and serves `block.list`/`get`/`search`. Not yet written: the `umb` CLI (T-F0-11), the TUI (T-F0-12), the performance gates (T-F0-13) and the whole orchestration surface (T-F0-14…18).
- Specs at PRD 1.5 / API 1.6 / Tech 1.5 / Data Model 1.5 / Plan 1.4, constitution 1.1. The seven deltas raised so far are folded and archived under `changes/_archive/`.
- **One delta is pending ratification:** `changes/2026-09-structure-migration/`, which renumbers the structure and agent migrations. It blocks T-F0-14 and nothing else.
- Migrations are `0001_terminal`, `0002_block_index`, `0003_structure` (T-F0-14, not written yet) and `0004_agent` (T-F1-01). 0001 and 0002 are applied; nothing already applied is ever edited (Art. 6).
- `python3 tools/sdd_check.py` passes with no CRITICAL findings: 106/106 MUST with a task, and migration 0001 works in isolation.
- Building the tests needs libghostty-vt: run `task deps:ghostty` once, then use `task test` (it injects `PKG_CONFIG_PATH`). A bare `go test ./...` fails to build the cgo packages.
- Current Analyze: `specs/analyze/analyze-2026-09-20b.md`. Open: **C-05 (HIGH)**, the `w1:t1:p2` identifiers of REQ-WS-002 against Art. 6's type-prefixed ULIDs — a decision needed before T-F0-14; C-01 (SQLite write failure under persist-first, blocks T-F1-13); C-02 (custody of the rule-signing key, blocks T-F1-30); and two LOW items.
- The constitution carries two amendments to Art. 5 dated 2026-09-20: environment fallback for secrets and mandatory signatures for rule material.
