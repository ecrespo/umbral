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
| The protocol in the binary against the API Spec | `task schema` | now |
| Lint + format + gosec | `task lint` | now |
| Architecture boundaries | `task arch` | now |
| Prove the boundaries reject a violation | `task arch:selftest` | now |
| NFR budget gates, and the proof they still bite | `task perf` | now |
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

- **Implementation: T-F0-01 … T-F0-17 and T-PKG-01/02 are done.** `docs/qa/f0-tui.md` was walked on 2026-09-20 with no step failing, which closed T-F0-12; the `Performance` job ran green on the CI runner the same day, which closed T-F0-13. `umbrald` owns real PTY sessions, streams them, records blocks and serves `block.list`/`get`/`search`; `umb` talks to it and autostarts it; `umbral-tui` renders it with tabs, a split and a block list. Three of the four NFR budgets are gates: `task perf` and `.github/workflows/perf.yml` run them, fail on a regression and prove on the same run that they still would. Idle daemon memory is the ungated one. `umbrald` now owns the workspace tree too: `workspace.*`, `tab.*` and `pane.*` over migration 0003, with `w<n>`/`w<n>:t<m>`/`w<n>:p<m>` identifiers and REQ-WS-007's aliases. A tab's layout exports and applies as a portable tree, and a pane can be launched with a command instead of a shell. Every notification carries a `seq` in its envelope and `session.snapshot` bootstraps a client's cache. The protocol is published: `umb api schema --json` prints the schema generated from the Go types, `task schema` compares it with the API Specification on every CI run, and a method whose module is absent answers `NOT_IMPLEMENTED` rather than pretending not to exist. Not yet written: structure restore (T-F0-18).
- Specs at PRD 1.8 / API 1.10 / Tech 1.8 / Data Model 1.6 / Plan 1.4, constitution 1.2. Fourteen deltas are archived under `changes/_archive/`. **One is pending ratification:** `changes/2026-09-capability-degradation/`, which moves the `capabilities` derivation onto what a build serves and says that an unserved method keeps its name so `NOT_IMPLEMENTED` is reachable; it is already applied to `specs/`.
- Migrations are `0001_terminal`, `0002_block_index`, `0003_structure` and `0004_agent` (T-F1-01, not written yet). 0001, 0002 and 0003 are applied; nothing already applied is ever edited (Art. 6).
- `python3 tools/sdd_check.py` passes with no CRITICAL findings: 107/107 MUST with a task, and migration 0001 works in isolation.
- Building the tests needs libghostty-vt: run `task deps:ghostty` once, then use `task test` (it injects `PKG_CONFIG_PATH`). A bare `go test ./...` fails to build the cgo packages. `umbral-tui` links it too, from T-F0-12.
- Current Analyze: `specs/analyze/analyze-2026-09-20b.md`. Open: C-01 (SQLite write failure under persist-first, blocks T-F1-13); C-02 (custody of the rule-signing key, blocks T-F1-30); and two LOW items. C-05 is closed.
- The constitution carries three amendments dated 2026-09-20: two to Art. 5 (environment fallback for secrets, mandatory signatures for rule material) and one to Art. 6 (the workspace tree uses `w<n>`, `w<n>:t<m>` and `w<n>:p<m>` instead of prefixed ULIDs).
