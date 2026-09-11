# Tasks — Umbral F0 (Terminal core and blocks)

> **Source specs:** `specs/prd/umbral-mvp.md` · `specs/api/umbral-daemon-api-v1.md` · `specs/technical/umbral-architecture.md` · `specs/data-model/umbral-schema.md` · `specs/plans/umbral-mvp-plan.md`
> **Plan phase covered:** F0 · **Generated:** 2026-09-11

## Conventions for this file

- Order is execution order, except for tasks marked [P].
- States:
  - `[ ]` pending
  - `[~]` in progress
  - `[x] {date}` done
  - `[!]` blocked (with a note)
- Infrastructure tasks without a functional REQ cite the constitution article they implement (`Art. N`). This is the only accepted exception to "task without a REQ".
- Every test cites its REQ in its name: `Test…_REQ_XXX_NNN`.

## Tasks

### [x] 2026-09-11 T-F0-01 · Scaffolding and quality gate
- **What:** set up the repository and its quality gate:
  - `go mod init`, Tech Design §5.1 layout and a Taskfile (`task lint`, `task test`, `task arch`);
  - pre-commit with gofumpt, go vet, golangci-lint (+gosec), govulncheck and gitleaks;
  - `.go-arch-lint.yml` with the Tech Design §5.2 rules;
  - CI pipeline.
- **REQ:** Art. 1, Art. 3
- **Files:** `go.mod`, `Taskfile.yml`, `.pre-commit-config.yaml`, `.golangci.yml`, `.go-arch-lint.yml`, `.github/workflows/ci.yml`, `scripts/arch_selftest.sh`, `cmd/**`, `internal/**/doc.go`, `AGENTS.md`
- **Depends on:** —
- **Done:** `task lint && task arch && task test` green in CI; a forbidden test import (e.g. `sessions` → `agents`) makes `task arch` fail.
- **Result:** module `github.com/ecrespo/umbral` on Go 1.27.1. `task ci` runs specs, lint, arch, arch:selftest, test and build, all green locally. The forbidden-import criterion is automated in `scripts/arch_selftest.sh`, which injects `sessions` → `agents`, asserts that `go-arch-lint` rejects it and removes it again; CI runs it as its own step. `internal/` holds the §5.1 skeleton with one documented package per layer, and the three `cmd/` binaries build and run.

### [ ] T-F0-02 · Store and migration 0001 (terminal)
- **What:**
  - open SQLite with the Data Model §5 pragmas;
  - migration 0001 exactly as Data Model §5.1 lists it: `schema_migrations`, **`threads`**, `sessions`,
    `blocks`, `block_chunks`, `blocks_fts` and the three `blocks_fts_ai/ad/au` triggers (§2.4);
  - restart recovery (Data Model §6, steps 1-2).
- **REQ:** REQ-BLK-007, REQ-TERM-005, REQ-BLK-006
- **Files:** `internal/store/**`, `internal/store/migrations/0001_terminal.sql`
- **Depends on:** T-F0-01
- **Done:** `go test ./internal/store/... -run 'Migrat|Recover'` green; `TestRecoveryMarksOpenBlocksAbandoned_REQ_TERM_005` passes; `TestMigration0001InsertsWithForeignKeysOn` inserts into `sessions` and `blocks` with `foreign_keys=ON` and an FTS `MATCH` returns the new block (A-01, A-07).

### [ ] T-F0-03 · JSON-RPC API, authentication and bus
- **What:**
  - Unix listener with 0600 permissions and token generation;
  - NDJSON and method dispatch;
  - `system.hello` and `system.status`, with `UNAUTHORIZED` and connection close;
  - typed `internal/bus`;
  - error translation (Tech Design §5.4).
- **REQ:** REQ-SEC-003, REQ-SEC-007
- **Files:** `internal/api/**`, `internal/bus/**`, `cmd/umbrald/main.go`
- **Depends on:** T-F0-01
- **Done:** `TestHelloRejectsBadToken_REQ_SEC_003` and `TestSocketPermissions0600_REQ_SEC_007` green.

### [ ] T-F0-04 · [P] Spike Q-01: VT snapshot
- **What:** check whether go-libghostty's `Formatter` produces replayable VT output (styles + cursor) for `session.subscribe`; if not, implement the bounded replay fallback. Record the decision in the Tech Design (DD-001) through a Delta if the contract changes.
- **REQ:** REQ-TERM-004
- **Files:** `internal/sessions/adapters/ghostty/snapshot*.go`, `docs/spikes/q01-snapshot.md`
- **Depends on:** T-F0-01
- **Done:** `docs/spikes/q01-snapshot.md` with the decision; golden round-trip test of the snapshot (applying it to an empty emulator produces the same screen in plain text) green.

### [ ] T-F0-05 · PTY sessions and emulator
- **What:**
  - `Emulator` port and libghostty adapter;
  - PTY adapter with `creack/pty`;
  - `session.create`, `list`, `input`, `resize` and `close`;
  - `input_owner` lock;
  - notifications `session.exited`, `session.resized` and `session.input_owner`.
- **REQ:** REQ-TERM-001, REQ-TERM-005, REQ-TERM-007, REQ-TERM-008
- **Files:** `internal/sessions/{domain,ports,adapters/pty,adapters/ghostty}/**`
- **Depends on:** T-F0-02, T-F0-03
- **Done:** tests `TestCreateSession_REQ_TERM_001`, `TestExitedEmitsCode_REQ_TERM_005`, `TestResizeNotifies_REQ_TERM_007` and `TestInputLockedRejected_REQ_TERM_008` green.

### [ ] T-F0-06 · Subscription, snapshot and fan-out
- **What:**
  - `session.subscribe` and `session.unsubscribe`;
  - snapshot delivery with scrollback (≤ 10,000 lines);
  - `session.output` with `seq`, batched every 4 ms or 32 KiB;
  - 8 MiB queue per client, unsubscribing on overflow;
  - live sessions without clients.
- **REQ:** REQ-TERM-003, REQ-TERM-004, REQ-TERM-006
- **Files:** `internal/sessions/**`, `internal/api/fanout*.go`
- **Depends on:** T-F0-04, T-F0-05
- **Done:** `TestSessionSurvivesNoClients_REQ_TERM_003`, `TestSubscribeSnapshotBeforeLive_REQ_TERM_004` and `BenchmarkOutputLatency_REQ_TERM_006` (p95 < 5 ms) green.

### [ ] T-F0-07 · [P] VT conformance suite
- **What:** the closed case list is Tech Design §8.1: implement **VT-01 … VT-20 (MUST)** and, if time allows, VT-21 and VT-22 (SHOULD). Each case is a `testdata/vt/VT-NN-<slug>.in` / `.golden` pair (plain text + attributes), plus a runner that feeds the bytes into the emulator and compares the result.
- **REQ:** REQ-TERM-002
- **Files:** `testdata/vt/**`, `internal/sessions/adapters/ghostty/conformance_test.go`
- **Depends on:** T-F0-05
- **Done:** `go test -run Conformance_REQ_TERM_002 ./...` green with the 20 MUST cases of §8.1 present; a missing `.golden` fails the run instead of skipping it.

### [ ] T-F0-08 · [P] Shell-integration bootstrap
- **What:** scripts for bash (`--init-file` that loads the user's rc), zsh (temporary `ZDOTDIR`) and fish (`--init-command`) that emit OSC 133 A/B/C/D, 633;E and 7 without breaking the user's prompt (Starship, p10k).
- **REQ:** REQ-BLK-005
- **Files:** `shell/bash/umbral.bash`, `shell/zsh/.zshrc`, `shell/fish/umbral.fish`, `internal/sessions/adapters/shellinteg/**`
- **Depends on:** T-F0-01
- **Done:** `TestBootstrapEmitsOSC133_REQ_BLK_005` in CI with real bash, zsh and fish.

### [ ] T-F0-09 · Block lifecycle
- **What:**
  - OSC parser (emulator callbacks) that drives the state machine in API Spec §7;
  - generation of `output_plain` (max 1 MiB) and zstd `block_chunks` (16 MiB cap);
  - `block.*` notifications;
  - alt-screen detection;
  - `integration: none` after 5 s without OSC.
- **REQ:** REQ-BLK-001, REQ-BLK-002, REQ-BLK-003, REQ-BLK-004, REQ-BLK-007
- **Files:** `internal/sessions/domain/block*.go`, `internal/sessions/adapters/shellinteg/**`, `internal/sessions/adapters/store/**`
- **Depends on:** T-F0-05, T-F0-08
- **Done:** tests `…_REQ_BLK_001` … `…_REQ_BLK_004` and `TestPlainOutputHasNoEscapes_REQ_BLK_007` green.

### [ ] T-F0-10 · Block query and search
- **What:** `block.list`, `block.get` (including `"last"`) and `block.search` over FTS5, with cursor pagination; benchmark with a 100,000-block fixture.
- **REQ:** REQ-BLK-006, REQ-CLI-002
- **Files:** `internal/sessions/adapters/store/**`, `internal/api/blocks.go`, `testdata/fixtures/blocks100k.sql.zst`
- **Depends on:** T-F0-09
- **Done:** `BenchmarkBlockSearch100k_REQ_BLK_006` with p95 < 200 ms; `TestBlockGetLast_REQ_CLI_002` green.

### [ ] T-F0-11 · Base `umb` CLI
- **What:** shared JSON-RPC client; `umbrald` autostart (3 s timeout → exit code 69); `umb status`; `umb block last --json`.
- **REQ:** REQ-CLI-002, REQ-CLI-003
- **Files:** `cmd/umb/**`, `internal/client/**`
- **Depends on:** T-F0-10
- **Done:** `TestUmbAutostartFailsWith69_REQ_CLI_003` and the JSON output test against the `Block` schema green.

### [ ] T-F0-12 · Base TUI
- **What:**
  - Bubble Tea v2 client with tabs and splits;
  - session rendering from `session.output` (client-side emulator);
  - block list with previous/next jump;
  - reconnection with snapshot.
- **REQ:** REQ-TUI-001
- **Files:** `cmd/umbral-tui/**`, `internal/tui/**`
- **Depends on:** T-F0-06, T-F0-09
- **Done:** `TestTUIBlockNavigation_REQ_TUI_001` (teatest) green; manual checklist in `docs/qa/f0-tui.md` completed.

### [ ] T-F0-13 · Performance gates in CI
- **What:** CI job that runs the `session.create` and output-latency benchmarks and fails if they exceed the NFRs.
- **REQ:** REQ-TERM-001, REQ-TERM-006
- **Files:** `.github/workflows/perf.yml`, `internal/sessions/bench_test.go`
- **Depends on:** T-F0-06
- **Done:** the perf job is green; an artificial regression (10 ms sleep) turns it red.

## Traceability matrix (F0)

| REQ | Tasks | Tests citing it |
|---|---|---|
| REQ-TERM-001 | T-F0-05, T-F0-13 | TestCreateSession_REQ_TERM_001, BenchmarkSessionCreate_REQ_TERM_001 |
| REQ-TERM-002 | T-F0-07 | Conformance_REQ_TERM_002 |
| REQ-TERM-003 | T-F0-06 | TestSessionSurvivesNoClients_REQ_TERM_003 |
| REQ-TERM-004 | T-F0-04, T-F0-06 | TestSubscribeSnapshotBeforeLive_REQ_TERM_004, TestSnapshotRoundTrip_REQ_TERM_004 |
| REQ-TERM-005 | T-F0-02, T-F0-05 | TestExitedEmitsCode_REQ_TERM_005, TestRecoveryMarksOpenBlocksAbandoned_REQ_TERM_005 |
| REQ-TERM-006 | T-F0-06, T-F0-13 | BenchmarkOutputLatency_REQ_TERM_006 |
| REQ-TERM-007 | T-F0-05 | TestResizeNotifies_REQ_TERM_007 |
| REQ-TERM-008 | T-F0-05 | TestInputLockedRejected_REQ_TERM_008 |
| REQ-BLK-001 | T-F0-09 | TestBlockStartsOnOSC133C_REQ_BLK_001 |
| REQ-BLK-002 | T-F0-09 | TestBlockClosedOnOSC133D_REQ_BLK_002 |
| REQ-BLK-003 | T-F0-09 | TestIntegrationNoneAfter5s_REQ_BLK_003 |
| REQ-BLK-004 | T-F0-09 | TestAltScreenMarksInteractive_REQ_BLK_004 |
| REQ-BLK-005 | T-F0-08 | TestBootstrapEmitsOSC133_REQ_BLK_005 |
| REQ-BLK-006 | T-F0-10 | BenchmarkBlockSearch100k_REQ_BLK_006 |
| REQ-BLK-007 | T-F0-02, T-F0-09 | TestPlainOutputHasNoEscapes_REQ_BLK_007 |
| REQ-SEC-003 | T-F0-03 | TestHelloRejectsBadToken_REQ_SEC_003 |
| REQ-SEC-007 | T-F0-03 | TestSocketPermissions0600_REQ_SEC_007 |
| REQ-CLI-002 | T-F0-10, T-F0-11 | TestBlockGetLast_REQ_CLI_002 |
| REQ-CLI-003 | T-F0-11 | TestUmbAutostartFailsWith69_REQ_CLI_003 |
| REQ-TUI-001 | T-F0-12 (+ T-F1-20) | TestTUIBlockNavigation_REQ_TUI_001 |

**Deferred:** REQ-BLK-008 (SHOULD, PowerShell) moves to F2 together with Windows.

## Execution log

| Date | Tasks | Result | Notes |
|---|---|---|---|
| 2026-09-11 | T-F0-01 | done | Go 1.27.1 installed under `~/.local/go` without root, since the machine had no Go at all. `task lint` (gofumpt, go vet, golangci-lint with gosec, govulncheck, gitleaks), `task arch`, `task arch:selftest` and `task test -race` all green. `gosec` G115 is excluded for now with a written reason: it fires on every epoch-ms and micro-USD conversion Art. 6 mandates, and it is re-enabled once the store layer exists. **Zig is still missing**, so T-F0-04 and T-F0-05 cannot build libghostty yet. |
