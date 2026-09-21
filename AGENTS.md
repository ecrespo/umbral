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
- **Tests first, always.** Write the failing test from the EARS criterion before the implementation. Every test cites its REQ in the name (`Test…_REQ_SEC_003`); use the exact name from the traceability matrix when it exists. Then **check the teeth**: break the property deliberately and confirm the test reddens. A test written after the code tends to be written to fit it — the T-F0-18 review found every gate green while a one-token mutation that made a restart execute every stored command passed the whole suite.
- **Start every test run clean, and leave nothing behind.** No orphan `umbrald` processes, no stray temp directories, and never a write to the real `$XDG_DATA_HOME` database. A test that starts a daemon redirects **every** `XDG_*` directory it writes to — not only `XDG_RUNTIME_DIR`, which moves the socket and leaves the database where the developer's own is — and owns and stops the process it starts, because `umbrald` outlives its clients by design (REQ-TERM-003) and there is no `system.shutdown`. Dirty state hides defects and invents others; the suite was not hermetic until 2026-09-20 (`docs/checkpoints/2026-09-20-f0-closure.md`).
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

- **Implementation: T-F0-01 … T-F0-18 and T-PKG-01/02 are done. That completes F0's task list; it does not close the phase** — two of the plan's six F0 exit criteria are still open, verified on 2026-09-20 (`docs/checkpoints/2026-09-20-f0-closure.md`): the TUI has not been used for a week as the main terminal, and **there is no CLI for the workspace tree at all**, so "a script creates a workspace, splits, exports and reapplies a layout using only the CLI" cannot be performed. `umb` serves `status`, `block last`, `api schema`, `version` and `help`; no task builds `umb workspace`/`tab`/`pane`/`layout` and no REQ requires it, while the Art. 6 amendment justifies the `w<n>` identifiers on the strength of `umb pane split w1:t1` being "the feature". That gap wants a Delta. A third item was added by the same verification: **`T-F0-19`**, the shell-bootstrap sweeper (delta `2026-09-bootstrap-sweeper`, REQ-TERM-012), specified and not implemented — so F0 has one open task as well as two open criteria. `docs/qa/f0-tui.md` was walked on 2026-09-20 with no step failing, which closed T-F0-12; the `Performance` job ran green on the CI runner the same day, which closed T-F0-13. `umbrald` owns real PTY sessions, streams them, records blocks and serves `block.list`/`get`/`search`; `umb` talks to it and autostarts it; `umbral-tui` renders it with tabs, a split and a block list. Three of the four NFR budgets are gates: `task perf` and `.github/workflows/perf.yml` run them, fail on a regression and prove on the same run that they still would. Idle daemon memory is the ungated one. `umbrald` now owns the workspace tree too: `workspace.*`, `tab.*` and `pane.*` over migration 0003, with `w<n>`/`w<n>:t<m>`/`w<n>:p<m>` identifiers and REQ-WS-007's aliases. A tab's layout exports and applies as a portable tree, and a pane can be launched with a command instead of a shell. Every notification carries a `seq` in its envelope and `session.snapshot` bootstraps a client's cache. The protocol is published: `umb api schema --json` prints JSON Schema (draft 2020-12) generated from the Go types, `task schema` compares all 33 served methods with the API Specification on every CI run and validates every embedded schema against the metaschema, and a method whose module is absent answers `NOT_IMPLEMENTED` rather than pretending not to exist. A restart rebuilds the workspace tree, gives every pane a fresh shell and keeps focus; a pane's stored command comes back **pending**, typed at the prompt and never run, which is REQ-TERM-011. `$XDG_CONFIG_HOME/umbral/config.toml` carries `[experimental] pane_history`, off by default; a malformed file stops the daemon rather than being replaced by defaults.
- Specs at PRD 1.10 / API 1.12 / Tech 1.10 / Data Model 1.7 / Plan 1.4, constitution 1.2. Seventeen deltas are archived under `changes/_archive/`, and **`2026-09-bootstrap-sweeper` is pending ratification** — it is applied to `specs/` and implemented by T-F0-19, which is not written. The last of them, `2026-09-restore-semantics`, stops a restart and `layout.apply` running a stored command, gives focus and `command_pending` their columns, moves `pane_history` to migration `0004_restore`, and specifies `$XDG_CONFIG_HOME/umbral/config.toml`.
- Migrations are `0001_terminal`, `0002_block_index`, `0003_structure`, `0004_restore` and `0005_agent` (T-F1-01, not written yet). 0001 to 0004 are applied; nothing already applied is ever edited (Art. 6). The agent subdomain moved from `0004` to `0005` so `pane_history` could ship with F0 (delta `2026-09-restore-semantics`).
- `python3 tools/sdd_check.py` passes with no CRITICAL findings: 107/107 MUST with a task, and migration 0001 works in isolation.
- Building the tests needs libghostty-vt: run `task deps:ghostty` once, then use `task test` (it injects `PKG_CONFIG_PATH`). A bare `go test ./...` fails to build the cgo packages. `umbral-tui` links it too, from T-F0-12.
- Current Analyze: `specs/analyze/analyze-2026-09-20b.md`. Open: C-01 (SQLite write failure under persist-first, blocks T-F1-13); C-02 (custody of the rule-signing key, blocks T-F1-30); and two LOW items. C-05 is closed.
- A restart was verified end to end on 2026-09-20 against a real daemon: `kill -9`, restart, five panes back with their labels, cwds and fresh sessions, focus preserved, and the stored commands typed at their prompts without running (REQ-TERM-011). The same run found that `cmd/umbrald/bootstrap_test.go` overrode only `XDG_RUNTIME_DIR`, so its autostarted daemon wrote to the **developer's real** `~/.local/share/umbral/umbral.db` — which now holds 2484 tabs and 2490 sessions created by tests — and leaked one `umbrald` per run, four of them found alive. Both are fixed, and the leftovers were cleaned on 2026-09-20: 2527 stray `/tmp` directories (168 MB) removed, and the test rows deleted from the real database (28 workspaces cascading to 2484 tabs and 2491 panes, plus 2486 sessions; 1.9 MB + 4.2 MB WAL down to 225 KB), **keeping** the 4 real sessions and 10 blocks of the `docs/qa/f0-tui.md` walkthrough. A full `task ci` now leaves no daemon, no temp directory and the real database untouched.
- The constitution carries three amendments dated 2026-09-20: two to Art. 5 (environment fallback for secrets, mandatory signatures for rule material) and one to Art. 6 (the workspace tree uses `w<n>`, `w<n>:t<m>` and `w<n>:p<m>` instead of prefixed ULIDs).
