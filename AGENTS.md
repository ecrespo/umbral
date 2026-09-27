# AGENTS.md — Umbral

Guide for any coding agent (Claude Code, Codex, Gemini CLI, OpenCode…) and for people new to the
repo. `CLAUDE.md` imports this file.

## What this repo is
Umbral is a local-first agentic terminal in Go. It consists of:
- `umbrald`: a daemon with PTY, VT (libghostty), blocks, agent, model gateway and MCP;
- thin clients: Bubble Tea v2 TUI, `umb` CLI and, from F2, a Wails v3 desktop app.

The specification package is complete and **phase F0 is closed (2026-09-26)**. `T-F0-01` …
`T-F0-27` are implemented: the daemon, the store, JSON-RPC, PTY sessions, streaming, blocks,
search, the workspace tree, portable layouts, restore, the `umb` CLI and the base TUI. **F1,
the agent, is next.** See "Known status" at the end of this file for what is and is not
written.

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
| The same, failing if the run leaked a daemon, a temp dir or a write to the real DB | `task test:hygiene` | now |
| Prove that hygiene gate rejects a leak | `task test:hygiene:selftest` | now |
| F0 exit criterion 4 against a real daemon, using only the CLI | `task roundtrip` | now |
| The socket-using packages under a macOS-length `TMPDIR` | `task test:portability` | now |
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

- **Phase F0 is closed (2026-09-26); F1 is next.** `docs/checkpoints/2026-09-26-f0-closed.md`
  is the closing checkpoint, verified by running rather than from notes.
  - **Tasks:** T-F0-01 … T-F0-27 and T-PKG-01/02 are done.
  - **Exit criteria:** five are met, together with REQ-BLK-003. The third, the TUI's week as
    the main terminal, was moved by the Tech Lead to the release 0.1 gate as **`T-REL-01`**
    (delta `2026-09-defer-tui-week`). It is the one piece of F0's original list still open.
  - **CI:** the GitHub `CI` workflow is green on `ubuntu-latest` and `macos-latest` in one run
    on the closing tree (run 36287708369, `4d77c19`).
    Before T-F0-26 it had been red on both OSes since `eeab3fb`.
  - **What the runners exposed:** three defects no local run could see.
    - Output published before `Server.Notify`'s goroutine was scheduled was lost (`Listen`
      now takes the bus subscription).
    - A `session.output` could be written before the `session.subscribe` reply (the
      subscription is now held until the reply is on the wire).
    - A test treated the daemon's hang-up after an oversized message as a failure.

  The history of how F0 got here follows. Criterion 4 was met on 2026-09-26 by **`T-F0-20`**: `umb workspace`/`tab`/`pane`/`layout` (REQ-CLI-005, REQ-CLI-006, delta `2026-09-cli-workspace-surface`), after delta `2026-09-cli-allowlist` widened API §2 so the `cli` client kind may call the tree — every one of those methods answered `METHOD_NOT_FOUND` to `umb` before, a gap every fake-daemon test passed through. `scripts/cli_roundtrip.sh` performs the criterion against a real daemon (create, split, export, apply through a pipe, compare the trees) and runs as `task roundtrip`, last in `task ci`. **`T-F0-19`**, the shell-bootstrap sweeper (REQ-TERM-012), was implemented on 2026-09-21. Verified on 2026-09-20 (`docs/checkpoints/2026-09-20-f0-closure.md`) and re-validated on 2026-09-21 (`docs/checkpoints/2026-09-21-f0-validation.md`). `docs/qa/f0-tui.md` was walked on 2026-09-20 with no step failing, which closed T-F0-12; the `Performance` job ran green on the CI runner the same day, which closed T-F0-13. `umbrald` owns real PTY sessions, streams them, records blocks and serves `block.list`/`get`/`search`; `umb` talks to it and autostarts it; `umbral-tui` renders it with tabs, a split and a block list. Three of the four NFR budgets are gates: `task perf` and `.github/workflows/perf.yml` run them, fail on a regression and prove on the same run that they still would. Idle daemon memory is the ungated one, and on 2026-09-21 it was **measured** for the first time: an isolated daemon with five live panes held VmRSS 37.6 MiB / PSS 36.0 MiB, flat over 90 s, against the 80 MiB of PRD §7 — criterion 6 is met, still without a gate. `umbrald` now owns the workspace tree too: `workspace.*`, `tab.*` and `pane.*` over migration 0003, with `w<n>`/`w<n>:t<m>`/`w<n>:p<m>` identifiers and REQ-WS-007's aliases. A tab's layout exports and applies as a portable tree, and a pane can be launched with a command instead of a shell. Every notification carries a `seq` in its envelope and `session.snapshot` bootstraps a client's cache. The protocol is published: `umb api schema --json` prints JSON Schema (draft 2020-12) generated from the Go types, `task schema` compares all 33 served methods with the API Specification on every CI run and validates every embedded schema against the metaschema, and a method whose module is absent answers `NOT_IMPLEMENTED` rather than pretending not to exist. A restart rebuilds the workspace tree, gives every pane a fresh shell and keeps focus; a pane's stored command comes back **pending**, typed at the prompt and never run, which is REQ-TERM-011. `$XDG_CONFIG_HOME/umbral/config.toml` carries `[experimental] pane_history`, off by default; a malformed file stops the daemon rather than being replaced by defaults. **`T-F0-21` closed on 2026-09-26**: `Service.finish` used to stop the integration timer, the only thing that ever wrote `integration: none`, so a session whose process exited inside the five-second window stayed `pending` for the life of the daemon (REQ-BLK-003). The exit now settles the verdict; measured on a real daemon, two `sh -c "sleep 1"` panes read `exited|none` three seconds in. That was also the race behind `TestACommandPaneGetsNoShellIntegration_REQ_BLK_003`, so `task ci` is no longer a coin flip.
- **F1 progress:** T-F1-32 (handshake deadline), T-F1-33 (frame limit), T-F1-01 (migration 0005 and recovery §6 steps 3, 4 and 8), T-F1-02 (keyring, `models.toml`, `config.get`/`reload`), T-F1-03 (the pure policy engine), T-F1-04 (secret redaction), T-F1-05 (the model catalog and the openai-compat and OpenRouter adapters on Fantasy), T-F1-06 (the native Ollama adapter), T-F1-07 (the router: fallback, `usage`, redaction and the thread on every egress row) T-F1-08 (`examples/models.toml` with the Hugging Face router and OmniRoute presets) T-F1-09 (the tool registry and seven built-ins; `fetch_url` as Art. 4 egress) T-F1-10 (`run_command` in the thread's own PTY, with a cancel that keeps the shell) and T-F1-11 (context: rules files, attachments, git) were merged on 2026-09-27; T-F1-12 onwards are open.
- **Pending deltas:** `changes/2026-09-context-assembly/` (PROPOSED 2026-09-27, T-F1-11): git context runs no command a repository's config names (fsmonitor, filter drivers, diff drivers), rules files are never followed out of the repository, the bounds, and a failing git stated. The five F1 deltas raised by T-F1-02…T-F1-10 — `2026-09-provider-config` (the `models.toml` shape, provider health reasons, `config.get`, locality by `base_url`), `2026-09-policy-precedence` (an exposure step before DD-006, the destructive floor, `auto-edit` outside the write root, rules per command of a line), `2026-09-redaction-thresholds` (REQ-SEC-001's thresholds as necessary conditions and the recall they allow), `2026-09-router-fallback` (no fallback after the first event, a failed `usage` write is logged) and `2026-09-builtin-tools` (`fetch_url` as Art. 4 egress, grants bound to their call, the thread's PTY and its cancel) — were ratified by the Tech Lead on 2026-09-27 and folded into PRD 1.14, Tech 1.24 and Data Model 1.11. API §5.36's example trace (policy-precedence decision 5) is written by T-F1-28.
- Specs at PRD 1.14 / API 1.18 / Tech 1.25 / Data Model 1.11 / Plan 1.10, constitution 1.2. Thirty-four deltas are archived under `changes/_archive/`; one, `2026-09-context-assembly`, is pending. The four newest were ratified together on 2026-09-26 for F1 and are folded into PRD 1.13, Data Model 1.9, Plan 1.10 and `specs/tasks/umbral-f1-tasks.md`: `2026-09-handshake-hardening` (**T-F1-32**) and `2026-09-frame-limit-monitoring` (**T-F1-33**) run **before T-F1-01**, which depends on them; `2026-09-skills-cli` (T-F1-34..36, `umb skill` for Umbral's own agent, `skill_load` output tainting the turn) and `2026-09-cli-mcp` (T-F1-37, `umb mcp` and the agent panel's extensions view). Their API and Tech Design text is written by those tasks. F1 is 37 tasks and about 68 days of effort. The newest, `2026-09-oversized-message` (ratified 2026-09-26, **T-F0-27**), writes down what the daemon does past the 4 MiB frame limit — `VALIDATION_ERROR` with a null id and a close — and makes every unparseable, non-JSON-RPC or oversized line before `system.hello` answer `UNAUTHORIZED` and close, which REQ-SEC-003 already required and the code did not do. The one before it, `2026-09-pane-term` (**T-F0-25**, REQ-TERM-013), gives every pane `TERM=xterm-256color` and `COLORTERM=truecolor`: a daemon started with no terminal used to hand its shells a dumb one. The one before it, `2026-09-database-lock` (ratified 2026-09-26, **T-F0-24**), adds a lock beside the database, so a daemon of another runtime directory pointed at it with `--db` exits 75 instead of recovering over live sessions. The one before it, `2026-09-recovery-integration` (ratified 2026-09-26, **T-F0-22**), makes Data Model §6 step 1 settle every `pending` integration verdict on restart — `osc133` for a session with blocks, `none` otherwise — so a `kill -9` inside a session's window — or a row an older daemon wrote — no longer stays `exited|pending` (REQ-BLK-003). The one before it, `2026-09-cli-allowlist` (ratified 2026-09-26), gives API §2's `cli` row `workspace.*`, `tab.*`, `pane.*` except `pane.move`, and `layout.*` — exactly the surface REQ-CLI-005/006 give `umb`; `session.*` stays interactive-only. The one before it, `2026-09-cli-workspace-surface` (ratified 2026-09-26), adds REQ-CLI-005 and REQ-CLI-006 — `umb workspace`/`tab`/`pane`/`layout` and a layout round trip through a pipe — and `T-F0-20` to build them. The one before it, `2026-09-bootstrap-sweeper` (ratified 2026-09-20), puts a shell's bootstrap files in the daemon's runtime directory and has it sweep the previous run's at start behind the instance lock; **T-F0-19 implemented it on 2026-09-21** — `shellinteg.Prepare` takes its parent directory and refuses an empty one, `cmd/umbrald` sweeps `shellinteg-*` under the lock and before `Restore`, and a `kill -9` followed by a restart was measured to remove the orphan while the restored pane keeps its own. The one before it, `2026-09-restore-semantics`, stops a restart and `layout.apply` running a stored command, gives focus and `command_pending` their columns, moves `pane_history` to migration `0004_restore`, and specifies `$XDG_CONFIG_HOME/umbral/config.toml`.
- Migrations are `0001_terminal`, `0002_block_index`, `0003_structure`, `0004_restore` and `0005_agent` (T-F1-01, written 2026-09-27: the agent, model, audit, MCP, rule and skill tables, plus `threads.attention_state`/`seen_at`). 0001 to 0005 are applied; nothing already applied is ever edited (Art. 6). The agent subdomain moved from `0004` to `0005` so `pane_history` could ship with F0 (delta `2026-09-restore-semantics`).
- `python3 tools/sdd_check.py` passes with no CRITICAL and no HIGH findings: 125/125 MUST with a task, and migration 0001 works in isolation.
- Building the tests needs libghostty-vt: run `task deps:ghostty` once, then use `task test` (it injects `PKG_CONFIG_PATH`). A bare `go test ./...` fails to build the cgo packages. `umbral-tui` links it too, from T-F0-12.
- Current Analyze: `specs/analyze/analyze-2026-09-20b.md`. Open: C-01 (SQLite write failure under persist-first, blocks T-F1-13); C-02 (custody of the rule-signing key, blocks T-F1-30); and two LOW items. C-05 is closed.
- A restart was verified end to end on 2026-09-20 against a real daemon: `kill -9`, restart, five panes back with their labels, cwds and fresh sessions, focus preserved, and the stored commands typed at their prompts without running (REQ-TERM-011). The same run found that `cmd/umbrald/bootstrap_test.go` overrode only `XDG_RUNTIME_DIR`, so its autostarted daemon wrote to the **developer's real** `~/.local/share/umbral/umbral.db` — which now holds 2484 tabs and 2490 sessions created by tests — and leaked one `umbrald` per run, four of them found alive. Both are fixed, and the leftovers were cleaned on 2026-09-20: 2527 stray `/tmp` directories (168 MB) removed, and the test rows deleted from the real database (28 workspaces cascading to 2484 tabs and 2491 panes, plus 2486 sessions; 1.9 MB + 4.2 MB WAL down to 225 KB), **keeping** the 4 real sessions and 10 blocks of the `docs/qa/f0-tui.md` walkthrough. A full `task ci` now leaves no daemon, no temp directory and the real database untouched.
- The constitution carries three amendments dated 2026-09-20: two to Art. 5 (environment fallback for secrets, mandatory signatures for rule material) and one to Art. 6 (the workspace tree uses `w<n>`, `w<n>:t<m>` and `w<n>:p<m>` instead of prefixed ULIDs).
