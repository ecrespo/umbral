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

### [x] 2026-09-11 T-F0-02 · Store and migration 0001 (terminal)
- **What:**
  - open SQLite with the Data Model §5 pragmas;
  - migration 0001 exactly as Data Model §5.1 lists it: `schema_migrations`, **`threads`**, `sessions`,
    `blocks`, `block_chunks`, `blocks_fts` and the three `blocks_fts_ai/ad/au` triggers (§2.4);
  - restart recovery (Data Model §6, steps 1-2).
- **REQ:** REQ-BLK-007, REQ-TERM-005, REQ-BLK-006
- **Files:** `internal/store/**`, `internal/store/migrations/0001_terminal.sql`
- **Depends on:** T-F0-01
- **Done:** `go test ./internal/store/... -run 'Migrat|Recover'` green; `TestRecoveryMarksOpenBlocksAbandoned_REQ_TERM_005` passes; `TestMigration0001InsertsWithForeignKeysOn` inserts into `sessions` and `blocks` with `foreign_keys=ON` and an FTS `MATCH` returns the new block (A-01, A-07).
- **Result:** `internal/store` opens the database with the §5 pragmas carried in the DSN, because `database/sql` pools connections and a pragma issued once would apply to one of them only. `Open` reads `foreign_keys` and `journal_mode` back instead of assuming the driver honoured them. Migrations are embedded, forward-only, one transaction each, contiguous from 0001, and a database from a newer daemon is refused with `ErrSchemaTooNew`. `Recover` applies §6 steps 1-2 in a single transaction and reports what it repaired. Ten tests green under `-race`, and removing `threads` from migration 0001 makes the A-01 regression test fail with the exact error the Analyze predicted. The daemon wires all of it at startup.

### [x] 2026-09-11 T-F0-03 · JSON-RPC API, authentication and bus
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
- **Result:** `internal/bus` is a typed pub/sub that drops the oldest event and counts the loss rather than blocking a publisher, so a stalled client cannot stall a PTY reader; the per-client 8 MiB queue of T-F0-06 sits above it. `internal/api` owns the wire format alone: modules return the §5.4 sentinels and `toWire` maps them, with `trace_id` and a scrubbed message only on `INTERNAL_ERROR`. `capabilities` is derived from the method table, so it cannot advertise a namespace that is not registered. Identifiers come from `store.NewID` using `github.com/oklog/ulid/v2`, placed in `store` because the `CHECK` constraints that enforce the prefixes are in the migrations next door. New unspecified surface, all recorded in `changes/_archive/2026-09-api-f0-decisions/`: the `$TMPDIR/umbral-<uid>` runtime-directory fallback with an ownership and mode check, the connection id standing in for `trace_id` until T-F1-18, a required `protocol_version`, and `UNAUTHORIZED` on a repeated handshake. `umbrald` also gained a `-check` flag that opens the database, recovers and exits without serving, and `exitUnavailable = 69` for a socket it cannot serve. The package `doc.go` files added by T-F0-01 for `api` and `bus` were folded into `jsonrpc.go` and `bus.go`, which is the idiomatic place for a package comment. `threads_running` in `system.status` is a real count, not a placeholder.

### [x] 2026-09-11 T-F0-04 · [P] Spike Q-01: VT snapshot
- **What:** check whether go-libghostty's `Formatter` produces replayable VT output (styles + cursor) for `session.subscribe`; if not, implement the bounded replay fallback. Record the decision in the Tech Design (DD-001) through a Delta if the contract changes.
- **REQ:** REQ-TERM-004
- **Files:** `internal/sessions/adapters/ghostty/snapshot*.go`, `docs/spikes/q01-snapshot.md`
- **Depends on:** T-F0-01
- **Done:** `docs/spikes/q01-snapshot.md` with the decision; golden round-trip test of the snapshot (applying it to an empty emulator produces the same screen in plain text) green.
- **Result:** Q-01 answers **yes**: `FormatterFormatVT` plus the twelve `WithFormatterExtra*` options produces replayable VT, and the bounded-replay fallback is not needed. DD-001 stands; no Delta. Eight golden cases round-trip, the cursor survives, and the snapshot is a fixed point. The spike's real finding is a silent failure the spec would have walked into: `WithMaxScrollbackLines` alone is inert because libghostty's small default **byte** budget prunes first, so a terminal asked for 10,000 lines keeps 588. `NewTerminal` sets both budgets and the regression test calls that constructor, so removing either fails. Cost for T-F0-06: a full ~9,900-line snapshot is 704 KiB in 9.0 ms, comfortably inside the 8 MiB per-client queue but too slow to build on the goroutine draining the PTY. The palette is opt-in because it costs a flat 5.5 KiB, 98 % of a small screen. Risk recorded: the bindings have no tagged release and disclaim API stability; the `Emulator` port contains it and this adapter is the only file naming a libghostty symbol.

### [x] 2026-09-11 T-F0-05 · PTY sessions and emulator
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
- **Result:** the module is domain, ports, two adapters and a service. `domain` holds the rules that need no PTY, including `CanAcceptInputFrom`, which is the whole of REQ-TERM-008 in one function. `ports` publishes `Sessions`, `PTY`, `Emulator` and `Bootstrapper`, all injected, so libghostty and `creack/pty` stay confined to one directory each. The service persists before notifying (DD-007) and drains each PTY on a background context, because REQ-TERM-003 promises the session outlives its clients. Measured `session.create` p95: 2.2 ms over 20 runs against the 300 ms NFR. Two linter exceptions carry written reasons rather than being silenced: `noctx` and `contextcheck` both want the shell bound to a request context, which would kill the session when the call that created it returns. The integration tests live in their own directory and their own arch-lint component, because wiring real adapters is a composition root's job; excluding `_test.go` from the boundary rules would have been the easy answer and would have stopped enforcing them in test code.

### [x] 2026-09-11 T-F0-06 · Subscription, snapshot and fan-out
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
- **Result:** `session.subscribe` registers the subscription **before** taking the snapshot, so a chunk arriving between the two is queued rather than lost; the snapshot's sequence number then discards whatever it already contains, which is what makes the stream gapless without being duplicative. The 4 ms figure in API Spec §8 is implemented as a ceiling rather than a target: a fixed 4 ms wait measured p95 4.24 ms against REQ-TERM-006's 5 ms budget, leaving nothing for the rest of the path, so the writer sends at once and coalesces only what piles up behind it. Measured after that change: **p95 12 µs**, 340 times better, with batching still working. Each subscription has its own writer goroutine and an 8 MiB budget; past it the subscription is dropped and the client is told with `session.unsubscribed`, which is new API surface and went into `changes/2026-09-slow-client-notification/`. Output goes only to subscribers, unlike the control notifications, which every authenticated connection receives.

### [x] 2026-09-11 T-F0-07 · [P] VT conformance suite
- **What:** the closed case list is Tech Design §8.1: implement **VT-01 … VT-20 (MUST)** and, if time allows, VT-21 and VT-22 (SHOULD). Each case is a `testdata/vt/VT-NN-<slug>.in` / `.golden` pair (plain text + attributes), plus a runner that feeds the bytes into the emulator and compares the result.
- **REQ:** REQ-TERM-002
- **Files:** `testdata/vt/**`, `internal/sessions/adapters/ghostty/conformance_test.go`
- **Depends on:** T-F0-05
- **Done:** `go test -run Conformance_REQ_TERM_002 ./...` green with the 20 MUST cases of §8.1 present; a missing `.golden` fails the run instead of skipping it.
- **Result:** the closed list lives in Go and the fixtures on disk, both generated from the same table with `-update-vt`, so they cannot drift. Each case writes a `.in` with the exact byte stream, kept so a failure can be replayed outside Go, and a `.golden` with the screen as plain text, the cursor, the title, the working directory and the replayable VT with its escapes made visible. The VT section is what covers *and attributes*: plain text alone would pass an emulator that dropped every colour. Three checks were verified by breaking them: a missing `.golden` fails rather than skips, deleting a MUST case from the list fails with `the suite has 19 MUST cases`, and a `.in` that no longer matches its case is reported. VT-14 applies a resize after the input, which is the only case whose fixture is not a pure byte stream.

### [x] 2026-09-11 T-F0-08 · [P] Shell-integration bootstrap
- **What:** scripts for bash (`--init-file` that loads the user's rc), zsh (temporary `ZDOTDIR`) and fish (`--init-command`) that emit OSC 133 A/B/C/D, 633;E and 7 without breaking the user's prompt (Starship, p10k).
- **REQ:** REQ-BLK-005
- **Files:** `shell/bash/umbral.bash`, `shell/zsh/.zshrc`, `shell/fish/umbral.fish`, `internal/sessions/adapters/shellinteg/**`
- **Depends on:** T-F0-01
- **Done:** `TestBootstrapEmitsOSC133_REQ_BLK_005` in CI with real bash, zsh and fish.
- **Result:** three scripts under `shell/`, embedded through `shell/shell.go` and materialised by `internal/sessions/adapters/shellinteg`. Each shell gets the only injection that both runs before the first prompt and keeps the user's own configuration: bash `--init-file` sourcing the rc back, zsh a temporary `ZDOTDIR` restoring the real one, fish `--init-command`, which runs after `config.fish` so nothing needs restoring. `Bootstrap.Argv` assembles the argument list because bash requires long options first: `bash -i --init-file F` exits 2. Tested under a real PTY, because every hook involved only runs in an interactive shell and a test on a pipe would pass against a bootstrap that emits nothing. Two real bugs were found this way and both now have a regression test: the bash DEBUG trap opened a phantom block for the prompt framework's own hook, recording `starship_precmd` as the first command of every session, and markers applied once are lost by any prompt that rewrites itself.

### [x] 2026-09-11 T-F0-09 · Block lifecycle
- **What:**
  - OSC parser (emulator callbacks) that drives the state machine in API Spec §7;
  - generation of `output_plain` (max 1 MiB) and zstd `block_chunks` (16 MiB cap);
  - `block.*` notifications;
  - alt-screen detection;
  - `integration: none` after 5 s without OSC.
- **REQ:** REQ-BLK-001, REQ-BLK-002, REQ-BLK-003, REQ-BLK-004, REQ-BLK-007
- **Files:** `internal/sessions/domain/{block,marker,plaintext,recorder}.go`,
  `internal/sessions/adapters/shellinteg/scanner.go`,
  `internal/sessions/adapters/blockstore/**`, `internal/sessions/blocks.go`
- **Depends on:** T-F0-05, T-F0-08
- **Done:** tests `…_REQ_BLK_001` … `…_REQ_BLK_004` and `TestPlainOutputHasNoEscapes_REQ_BLK_007` green.
- **Result:** the OSC scanner lives in `internal/sessions/adapters/shellinteg/scanner.go`, not in
  emulator callbacks: libghostty's Go bindings expose neither the OSC 133 payload nor OSC 633,
  so the daemon cannot get the command line or the exit code from the emulator. The block state
  machine is a pure `domain.Recorder`; persistence is `internal/sessions/adapters/blockstore`
  with zstd chunks (`klauspost/compress`, the first compression dependency). Also fixed here
  because blocks in fish and zsh depended on them: the bash and zsh bootstraps reported exit
  code 0 for every command whenever another `PROMPT_COMMAND`/`precmd` hook ran first, and the
  daemon never answered terminal device queries, which made every fish session hang for two
  seconds and permanently lose features. Extra tests beyond the matrix:
  `TestBlocksInEveryShell_REQ_BLK_005` (bash, zsh and fish), the `TestScanner…` suite and
  `TestBlockNotificationsMatchTheSchema_REQ_BLK_001_REQ_BLK_002`. Five decisions that narrow
  approved spec text went to `changes/2026-09-block-lifecycle-decisions/` rather than staying
  in comments: what `abandoned` means, which sequences `block_chunks` keeps, what
  `output_truncated` covers, whether a late marker promotes a session, and the zstd
  dependency.

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

### [x] 2026-09-11 T-PKG-01 · Branding kit and pinned checksums
- **What:** carry the icon kit in `assets/branding/umbral-icons/`, pin every artifact in
  `CHECKSUMS.sha256`, and add a CI job that regenerates the kit with `tools/build.py` and
  compares it byte for byte.
- **REQ:** REQ-PKG-001, REQ-PKG-006
- **Files:** `assets/branding/umbral-icons/**`, `scripts/icons_check.sh`, `.github/workflows/ci.yml`, `Taskfile.yml`
- **Depends on:** —
- **Done:** the `icons` job is green; changing one pixel of a PNG turns it red.
- **Result:** `scripts/icons_check.sh` does three things rather than one: verify the
  checksums, regenerate with `build.py` and compare, then validate the `.desktop` file. The
  regeneration step is what REQ-PKG-006 actually asks for, because checking the checksums
  alone would pass a source edit committed together with its new checksum. Verified by
  flipping one byte of the 48 px PNG: exit 1. The kit reproduces byte for byte from source,
  so 55 of the 56 artifacts were already correct; only the `.desktop` file changed, in
  T-PKG-02. The regeneration step degrades to a warning when `cairosvg` and `Pillow` are
  missing, so a contributor without them still gets the checksum and `.desktop` checks; CI
  installs both, so the full check always runs there.

### [x] 2026-09-11 T-PKG-02 · `.desktop` file for release 0.1
- **What:** set the `.desktop` file to `Exec=umbral-tui` and `Terminal=true`, validate it in
  CI, and regenerate `CHECKSUMS.sha256` (finding A-15).
- **REQ:** REQ-PKG-002, REQ-PKG-003
- **Files:** `assets/branding/umbral-icons/tools/build.py`, `assets/branding/umbral-icons/linux/share/applications/io.github.ecrespo.Umbral.desktop`, `assets/branding/umbral-icons/CHECKSUMS.sha256`
- **Depends on:** T-PKG-01
- **Done:** `desktop-file-validate` without errors.
- **Result:** the change went into the `DESKTOP` template in `build.py`, not into the
  generated file, so the next regeneration keeps it. `desktop-file-validate` exits 0; it
  emits one *hint* about `Categories` listing more than one main category, which is
  pre-existing, is not an error, and is left for whoever revisits the kit's categories.
  `scripts/icons_check.sh` additionally asserts the four lines REQ-PKG-002 and REQ-PKG-003
  name, because the validator does not know which binary release 0.1 ships. Checksums
  regenerated, closing A-15.

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
| REQ-PKG-001 | T-PKG-01 | `icons` job: checksums + byte-for-byte regeneration |
| REQ-PKG-002 | T-PKG-01, T-PKG-02 | `desktop-file-validate`, asserted keys in `scripts/icons_check.sh` |
| REQ-PKG-003 | T-PKG-02 | asserted `Exec=umbral-tui` and `Terminal=true` in `scripts/icons_check.sh` |
| REQ-PKG-006 | T-PKG-01 | `icons` job regenerates with `build.py` and compares |

**Deferred:** REQ-BLK-008 (SHOULD, PowerShell) moves to F2 together with Windows.
REQ-PKG-004, 005, 007 and 008 are not MVP requirements; finding A-12 moved them to
`specs/prd/umbral-f2-desktop.md`, and tasks T-PKG-04 and T-PKG-05 go with them.

## Execution log

| Date | Tasks | Result | Notes |
|---|---|---|---|
| 2026-09-11 | T-F0-09 | done | Two defects in already-"done" work surfaced only when a real shell was asked for a real exit code. First: the bash and zsh bootstraps read `$?` in a hook registered last, and every element of `PROMPT_COMMAND`/`precmd_functions` leaves `$?` set to its own result, so with Starship installed every command was recorded as exit 0. The work is now split in two, a capture hook first and the marker hook last, because the two halves want opposite positions. Second: the daemon never wired libghostty's write-pty effect, so no program's query to the terminal was ever answered; fish waits two seconds for a Primary Device Attributes reply and then permanently disables features, and under a retrying test it never timed out at all. Both were found by `TestBlocksInEveryShell_REQ_BLK_005`, which exists because REQ-BLK-005 covers three shells and only running all three proves they agree. A third, smaller one was found by the plain-text test: carriage return was treated as "erase the line", which is right for a progress bar and wrong for the CRLF a PTY ends every line with, so the decision is now deferred one byte. The scanner and the recorder were each checked for teeth by breaking three and four properties respectively. Deliberately not done: `block.list`/`get`/`search` are T-F0-10, and chunk writes sit on the drain goroutine, after the chunk has been published, so they delay the history and never the screen. |
| 2026-09-11 | T-F0-07 | done | 22 cases: VT-01…VT-20 MUST plus VT-21 and VT-22 SHOULD. Everything passes against libghostty on the first run, which is expected rather than suspicious: the emulator is Ghostty's own and these are the sequences it exists to implement. The value is the regression net, not the discovery. Worth noting from the fixtures: VT-20 confirms the shell-integration OSC sequences leave no visible mark, which is what lets T-F0-09 read them without corrupting the screen. |
| 2026-09-11 | T-F0-06 | done | All three tests were checked for teeth. Two bit immediately; the ordering test did **not**, because the fake published output after subscribe returned and the race window was never exercised. Making the fake publish *inside* the snapshot call still did not bite reliably, since whether the dispatch goroutine ran in the window was up to the scheduler. It now asserts the ordering where it happens, that a subscription exists at the moment the snapshot is taken, which fails deterministically when the order is reversed. Verified end to end against a real bash session: the snapshot carries the pre-subscribe output, the live stream carries only what came after, and the sequence numbers are strictly increasing and all above the snapshot's. |
| 2026-09-11 | T-F0-05 | done | Both security-relevant tests were checked for teeth by breaking what they guard: writing the input before checking the lock fails REQ-TERM-008's canary check, and resizing only the bookkeeping fails REQ-TERM-007's `tput cols`. Verified end to end over the real socket: create, input, resize with its notification, list, close and the exited notification. One observation worth keeping: input written to a PTY before the shell's line editor is ready can be lost, so an end-to-end script that types immediately after `session.create` sees a wrong exit code. With the shell settled, bash, zsh and fish all report the real code. `session.*` is restricted to the `tui` and `desktop` client kinds, since API Spec §2 does not grant it to `cli`. |
| 2026-09-11 | T-F0-08 | done | bash 5.3.9, zsh 5.9 and fish 4.2.1, each driven through a PTY with `creack/pty`. The prompt-framework clause is covered by a fake rc that reassigns the prompt on every prompt, so it holds on a CI runner with neither Starship nor powerlevel10k installed; the machine that found the bug had Starship in its own rc. Both new tests were checked for teeth by breaking the thing they guard. fish skips the configuration-preservation case by design: `--init-command` runs after `config.fish`, so there is nothing to restore. |
| 2026-09-11 | T-F0-04 | done | First cgo in the repository. `libghostty-vt` is built from ghostty's source with Zig 0.16.0 by `scripts/build_libghostty.sh` (`task deps:ghostty`); the Taskfile points `PKG_CONFIG_PATH` at the default prefix so no Go target needs the caller to export anything, and CI builds it in the lint and test jobs. Measured numbers and the scrollback finding are in `docs/spikes/q01-snapshot.md`. Not covered here and deliberately left to their own tasks: reflow on resize (T-F0-07), real PTY output (T-F0-05), macOS. |
| 2026-09-11 | T-PKG-01, T-PKG-02 | done | Closes Phase 0. The kit reproduces byte for byte from `tools/build.py`, verified by regenerating it: 55 of 56 artifacts identical, the `.desktop` file the only intended change. Finding A-12 honoured: REQ-PKG-004, 005, 007 and 008 went to `specs/prd/umbral-f2-desktop.md` instead of becoming MVP MUSTs nothing could close. Finding A-15 closed: the checksums were regenerated after the `.desktop` change. Finding A-16 closed: the superseded Spanish copy of the delta was deleted. |
| 2026-09-11 | T-F0-03 | done after a `spec-guardian` round | The review returned FIX FIRST with 1 CRITICAL and 5 HIGH. The critical was real and reproduced with a probe: a token file that existed but was empty kept its old mode, because `os.WriteFile` does not apply its mode argument to an existing file, so the token landed at 0664. It is now written through `OpenFile` with an explicit `Chmod` and the mode is read back. Also fixed: the handshake validated `client_kind` before the token, so a bad token plus a bad kind answered `VALIDATION_ERROR` and left the connection open, against REQ-SEC-003; the runtime-directory fallback trusted a world-writable parent; `trace_id` carried a freshly minted id that correlated to nothing; `capabilities` advertised `sessions` and `blocks` with no such method registered; `go.mod` recorded a direct dependency as indirect. Five decisions the spec does not cover went into `changes/_archive/2026-09-api-f0-decisions/` instead of staying in comments. |
| 2026-09-11 | T-F0-02 | done | `internal/store` with migration 0001, pragma verification, forward-only migrations and restart recovery. Driver: `modernc.org/sqlite` (pure Go, no cgo). `threads` is created in 0001 per the folded delta, and the regression test for A-01 was checked by reverting the fix: it fails with `no such table: main.threads`, exactly as the Analyze described. Steps 3-4 of Data Model §6 wait for T-F1-01, which creates the tables they touch. |
| 2026-09-11 | T-F0-01 | done | Go 1.27.1 installed under `~/.local/go` without root, since the machine had no Go at all. `task lint` (gofumpt, go vet, golangci-lint with gosec, govulncheck, gitleaks), `task arch`, `task arch:selftest` and `task test -race` all green. `gosec` G115 is excluded for now with a written reason: it fires on every epoch-ms and micro-USD conversion Art. 6 mandates, and it is re-enabled once the store layer exists. **Zig is still missing**, so T-F0-04 and T-F0-05 cannot build libghostty yet. |
