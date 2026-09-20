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
  - migration 0001 exactly as Data Model §5 lists it: `schema_migrations`, **`threads`**, `sessions`,
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

### [x] 2026-09-11 T-F0-10 · Block query and search
- **What:** `block.list`, `block.get` (including `"last"`) and `block.search` over FTS5, with cursor pagination; benchmark with a 100,000-block fixture.
- **REQ:** REQ-BLK-006, REQ-CLI-002
- **Files:** `internal/sessions/domain/query.go`, `internal/sessions/query.go`,
  `internal/sessions/adapters/blockstore/query.go`, `internal/api/blocks.go`
- **Depends on:** T-F0-09
- **Done:** `BenchmarkBlockSearch100k_REQ_BLK_006` with p95 < 200 ms; `TestBlockGetLast_REQ_CLI_002` green.
- **Result:** measured p95 **0.93 ms** for search and **0.22 ms** for a list page over 100,000
  blocks. Getting there needed two changes, folded into Data Model v1.3 and API Spec v1.4
  (delta `2026-09-block-query-performance`, approved 2026-09-11): an index on `(started_at DESC, id DESC)`,
  without which every unfiltered page was a full scan and a sort (141 ms), and ordering
  `block.search` by insertion position rather than by start time, without which a common term
  sorted its whole match set (139 ms, and no index helps). The 100,000-block corpus is
  generated from a fixed seed in `bench_test.go` rather than committed as
  `testdata/fixtures/blocks100k.sql.zst`: a binary of that size is unreviewable, and the
  vocabulary that makes the queries match is readable in the file instead.
  `TestBlockGetLast_REQ_CLI_002` exists twice on purpose, once in `internal/api` for the wire
  shape the requirement names and once in `internal/sessions/integration` for the resolution,
  because no package may see both.

### [x] 2026-09-20 T-F0-11 · Base `umb` CLI
- **What:** shared JSON-RPC client; `umbrald` autostart (3 s timeout → exit code 69); `umb status`; `umb block last --json`.
- **REQ:** REQ-CLI-002, REQ-CLI-003, REQ-CLI-004
- **Files:** `cmd/umb/**`, `internal/client/**`, `internal/config/{paths,instancelock}.go`, `internal/api/{paths,server}.go`, `cmd/umbrald/main.go`
- **Depends on:** T-F0-10
- **Done:** `TestUmbAutostartFailsWith69_REQ_CLI_003` and the JSON output test against the `Block` schema green.
- **Result:** the client could not reach `api.DefaultSocketPath`, because the §5.2 row for `client` allows `config` and never `api`. Rather than copy the runtime-directory resolution — which carries `verifyPrivateDir`, a security check, and two copies of a security check are one too many — the location helpers moved to `internal/config`, which both sides already may import; `api` keeps the token writer, since only the daemon creates tokens. Those §5.2 rows did not actually exist, nor did §5.1 list `api`, `client`, `config`, `store` or `tui`: the rule lived only in `.go-arch-lint.yml`, so the citation was false until the delta wrote it down. The schema test compares the CLI's `Block` against `api.Block` by reflection, so the two sides of the socket check each other; that they both match API Spec §4 was confirmed by hand and is T-F0-17's job to automate. REQ-CLI-002's "current session" comes from `UMBRAL_SESSION_ID`; what happens outside a managed pane was a decision living only in a code comment, and is now in the PRD and in API Spec §5.17.
- **Found by the `spec-guardian` review, not by the tests:** three real defects. **One,** two `umb` invocations at once each started a daemon: reproduced with five concurrent `umb status`, which left five daemons on one installation, the later ones unlinking the earlier one's socket and running Data Model §6 recovery over its live sessions — marking running sessions `exited` and open blocks `abandoned`. The comment that justified removing a stale socket cited a database lock that does not exist. `umbrald` now takes an exclusive `flock` on `umbrald.lock` before it opens the database, and a daemon that cannot take it exits 0 without serving; with it, the same five-way race leaves exactly one. **Two,** the handshake had no deadline and `Ctrl-C` could not interrupt it: `signal.NotifyContext` had disarmed the signal without anything acting on the cancellation, so `umb` against a daemon that accepts and never answers needed `SIGKILL`. The handshake now shares the dial budget and `call` expires the socket deadline on cancellation. **Three,** an autostarted daemon sent its log to `/dev/null`, so every daemon started the ordinary way discarded the structured log Art. 7 requires; it now appends to `umbrald.log` beside the socket, and the failure message points at it.
- **Found while fixing those:** a data race of my own making — the cancellation callback read `c.conn` while `Close` wrote it, which `-race` caught on about half of the runs. The callback now closes over the connection. And the fake daemons in the tests looped on `Accept` forever, leaving an orphan process per run; sixty-eight had accumulated, and a stale one still holding a socket path is a flaky neighbour for the next run. Both fakes now expire.
- **Sixteen tests, every one checked for teeth.** Two did not bite on the first attempt and were rewritten: the write-failure test passed for the wrong reason, because `printJSON` encoded straight into the stream so its error never reached the exit-code path; and the `--daemon-path` test passed with the flag removed, because `umbrald` is not on `PATH` under `go test` either, so it now asserts that the message names the path it was given.
- **Verified against the real daemon, not only against fakes:** `umb status` autostarts `umbrald` on a clean `XDG_RUNTIME_DIR` and its log lands in `umbrald.log`; after running `false` in a live session, `umb block last --json` prints that block with `exit_code: 1`; and `umb status | head -1` exits 0.

### [x] 2026-09-20 T-F0-12 · Base TUI
- **What:**
  - Bubble Tea v2 client with tabs and splits;
  - session rendering from `session.output` (client-side emulator);
  - block list with previous/next jump;
  - reconnection with snapshot.
- **REQ:** REQ-TUI-001
- **Files:** `cmd/umbral-tui/**`, `internal/tui/**`, `internal/client/{client,autostart,stream}.go`, `.go-arch-lint.yml`
- **Depends on:** T-F0-06, T-F0-09
- **Done:** `TestTUIBlockNavigation_REQ_TUI_001` green; manual checklist in `docs/qa/f0-tui.md` completed.
- **State:** `[x]` as of 2026-09-20. Both halves of the Done line are met. The second half was a person's job — raw mode, a real keyboard, a real font — and Ernesto Crespo walked all fourteen steps of `docs/qa/f0-tui.md` on Ubuntu 26.04.1 with none failing. That table is an attestation rather than a machine result, and it says so: the terminal emulator went unrecorded, so a second walk on a different one before release would still be worth doing.
- **Result:** two things the task description did not name had to be decided first, and both needed a Delta (`2026-09-tui-renderer`). DD-001 already says the client parses for itself — "clients receive raw bytes and process them with their own renderer", with "double parsing" listed as the accepted cost — but no §5.2 row gave the TUI anywhere to put one, and splits make it unavoidable: bytes can go to the host terminal only while one session owns the whole window. The renderer is libghostty, the daemon's own, in `internal/tui/adapters/ghosttyvt`. A pure-Go VT from Bubble Tea's ecosystem would have kept `umbral-tui` free of cgo, and was rejected because the two sides parse the same stream: two emulators are two chances for the screen to drift from the block history, and T-F0-07's conformance suite covers only one of them. The second decision was Bubble Tea v2 itself, which the Tech Design §5.1 already named; its module path is now `charm.land/bubbletea/v2`.
- **Result (client):** `internal/client` was request/response only, which a TUI cannot use — output arrives when nothing was asked for. `Stream` owns the reader instead and routes frames by whether they carry an id, so calls and notifications work at once; past a bounded buffer it stops and says so rather than growing, which is the client-side twin of the daemon's `session.unsubscribed`.
- **Result (found by running it):** the handshake hardcoded `client_kind: "cli"`, and the daemon's §2 allowlist keeps `session.*` out of the `cli` set, so `session.create` came back `METHOD_NOT_FOUND` — a daemon that looked unimplemented rather than a client that had misidentified itself. No test with a fake could have caught it: every fake answered the handshake without reading it. The kind is now an option, `umbral-tui` sets `tui`, and this is the second time this review cycle that the handshake's own contents went unchecked (`spec-guardian` finding 5 on T-F0-11 said so).
- **Result (found by re-reading it):** `session.unsubscribed` was mapped to "the daemon is gone", which is wrong twice over: the connection is still up, and API Spec §6 says exactly what to do instead — subscribe again and take the fresh snapshot. As written it would have stranded a working connection on a frozen screen, and stopped listening for everything else. It is now its own event kind, the pane's `seq` is reset before re-attaching, and a test pins the difference.
- **Scope delivered, and what was left out on purpose.** Two panes per tab: the pane tree, portable layouts and the `w1:t1:p2` identifiers are T-F0-14 and T-F0-15, and half a tree would be work thrown away. A dropped *subscription* is re-attached automatically (API Spec §6); a lost *daemon connection* is reported and not retried, so "reconnection with snapshot" in the What line means the first and not the second. No mouse, no function keys, no Kitty keyboard protocol: `keyBytes` covers what a shell needs and the rest waits for something that tests it. No agent panel — REQ-TUI-001's other half is T-F1-20, which both matrices agree on. No scrollback view; the pane shows the screen and `umb block search` reaches the history. Lip Gloss, which the constitution's stack table names beside Bubble Tea, is not used: the delta records why and what it costs.
- **Result (what the gates do not cover, on purpose).** Three scope decisions, recorded here because each was otherwise an argument living in a code comment. **One:** `BenchmarkSessionCreate_REQ_TERM_001` times `sessions.Service.Create`, not a client's round trip, so the handshake, the JSON-RPC decode, the reply marshal and the socket write are inside REQ-TERM-001's wording and outside the gate — and no other requirement bounds them either; REQ-TERM-006 covers the notification fan-out, a different path, and an earlier draft of the benchmark comment wrongly borrowed it as cover. The margin is 2.1 ms against 300 ms and the handler is an unmarshal and a marshal, so the risk is small, but it is unmeasured rather than bounded. **Two:** PRD §7's fourth bullet, idle daemon memory under 80 MiB with five sessions, is gated nowhere. It carries no REQ id and this task's What and REQ lines name only `session.create` and output latency, so Art. 2 does not bite; it also needs a running daemon and an RSS probe rather than a benchmark, which is a different kind of test than anything here. It stays ungated and now stays ungated in writing. **Three:** REQ-BLK-006 is gated but not self-tested — it has no injection point — which `scripts/perf_selftest.sh` states in its header.
- **Result (a gate nobody required).** `perf_gates.sh` said "a miss blocks the merge", which was prose: the new job was in no required-checks list. Worse, the list in `scripts/github_bootstrap.py` had drifted so far that none of its four context names matched a job in `ci.yml` any more — a name that matches no job is not an error on GitHub's side, the check is simply never required and the protection weakens in silence. The list now carries the six real `ci.yml` contexts and the perf job, grouped under the workflow each comes from, and the script's message points at where the requirement is configured instead of asserting it.
- **Result (a duplication with a threshold).** `perfProbeEnv`, `perfProbe` and `percentile` exist twice, in `internal/api` and `internal/sessions/integration`, because Go test helpers do not cross a package boundary. Two copies held honest by a selftest that would redden if either drifted is a fair trade; a third gate is not. At that point they belong in an `internal/perftest` package with its own `.go-arch-lint.yml` entry, since code the boundary rules do not map is code the boundary rules do not enforce.
- **Result (what the review found in the wire layer).** The tree held up; the five hundred lines of translation above it did not, and the reason is that nothing tested them. Two were contradictions of the approved spec, both measured against a running daemon rather than argued: **one**, `toWire` consulted only the sessions module's sentinels, so every `ErrNotFound`, `ErrValidation` and `ErrConflict` the tree raised reached the client as `INTERNAL_ERROR` — the wrong code, and one that mints the `trace_id` §3 reserves for real faults, from an ordinary client typo. **Two**, the `workspace.*`, `tab.*` and `pane.*` notifications wrapped their object in an envelope, while §6 types those payloads as `Workspace`, `Tab` and `Pane` exactly as `block.started` carries a `Block`; a client written to §6 would have found no `id`. `internal/api/workspaces_wire_test.go` now covers the §5.4-§5.6 results field by field, the three `true` defaults, the §3 error table and the §6 payloads, and each of the two was confirmed to turn it red.
- **Result (a capability nobody could find).** The handshake advertised `workspace`, `tab` and `pane`. API Spec §2 enumerates the legal entries and names the tree `workspaces` — one capability for three method prefixes — so a client branching on §2's vocabulary would have concluded the tree was absent while all seventeen methods answered. The derivation was honest about the method table and wrong about §2's words. `capabilityOf` maps prefixes to namespaces, which also fixed the same drift in `session` and `block`, pre-existing and enshrined by three tests.
- **Result (things accepted and quietly ignored).** `pane.split` took §5.6's `command`, stored it and started a plain shell: a client would have watched its command not run, with no error to explain it. `sessdomain.CreateParams` carries no argv, and adding one is the launch path `layout.apply` needs, so the parameter is now refused with `VALIDATION_ERROR` naming T-F0-15 rather than silently dropped. `workspace.create` likewise accepted an absent `cwd`, which §5.4 requires, and started the root shell wherever the daemon happened to be; it is now a validation error, and a workspace created as a side effect of `pane.move` inherits the moved pane's directory, since there is no caller to ask.
- **Result (notifications that were not sent).** Closing a workspace or a tab emitted nothing for the tabs and panes it took with it, so a client caching the tree kept ghosts; both now announce every object they closed, panes first. `workspace.closed` carried a record read before the close, which said the workspace was open. And a move that had to build its destination announced only `pane.moved`, leaving the new tab or workspace to be inferred from a reference inside it; `MovePane` now reports what it created and the service publishes those first.
- **Result (identifiers at the edge).** A malformed identifier reached the store, found nothing, and came back as `NOT_FOUND` — telling a client its pane was gone when its request was malformed. `checkWorkspaceID`/`checkTabID`/`checkPaneID` run the REQ-WS-002 grammar at the service boundary, which is what `ErrValidation`'s own doc comment had been claiming all along.
- **Result (a fourth undocumented decision).** A `ratio` outside §5.6's 0.1-0.9 is clamped, not refused, and the integration test enshrines it. Dragging a divider past the edge should give the edge; a client cannot meaningfully recover from an error there. It belongs in this list with the other three rather than only in a test.
- **Result (verification):** sixteen tests, all checked for teeth; three were broken deliberately to confirm it. Then the whole path was driven against a real `umbrald`: create a session, subscribe, replay the snapshot through the client's own libghostty, resize, send `echo`, receive `session.output` and `block.closed`, render the output, and read the block back from `block.list` with `exit=0`. That is what caught the `client_kind` bug.

### [x] 2026-09-20 T-F0-13 · Performance gates in CI
- **What:** CI job that runs the `session.create` and output-latency benchmarks and fails if they exceed the NFRs.
- **REQ:** REQ-TERM-001, REQ-TERM-006
- **Files:** `.github/workflows/perf.yml`, `internal/sessions/integration/bench_test.go`, `internal/api/latency_test.go`, `scripts/perf_gates.sh`, `scripts/perf_selftest.sh`
- **Depends on:** T-F0-06
- **Done:** the perf job is green; an artificial regression sized against each budget — 10 ms for REQ-TERM-006, 400 ms for REQ-TERM-001 — turns it red.
- **Done line amended 2026-09-20.** It read "an artificial regression (10 ms sleep) turns it red". That is true of the 5 ms latency budget and false of the 300 ms `session.create` budget, where 10 ms is noise: measured, the benchmark stays green. One number could not serve both, and leaving the sentence standing would have meant closing the task against a criterion the repo knew it did not meet. No spec sentence moves — the PRD, API, Tech Design and data model are untouched — and the replacement is strictly stronger, so this is an amendment and not a Delta.
- **State:** `[x]` as of 2026-09-20. The job ran for the first time on the push that closed T-F0-12 (`Performance`, run 35525499133) and both halves of the Done line held on the reference hardware PRD §7 names. No threshold needed the adjustment §7 provides for — the 4 vCPU runner is *faster* than the manual machine on two of the three gates, because the laptop figures were taken while a full `task ci` was competing for the same cores:

  | Gate | Budget | CI runner p95 | ThinkPad p95 |
  |---|---|---|---|
  | `session.create` (REQ-TERM-001) | 300 ms | 1.14 ms | 2.1 ms |
  | added output latency (REQ-TERM-006) | 5 ms | 26 µs | 26 µs |
  | `block.search` over 100k (REQ-BLK-006) | 200 ms | 1.14 ms | 2.4 ms |

  The selftest also fired on the runner: 10.26 ms caught against the 5 ms budget and 402 ms against the 300 ms one. That is the half that matters, because it is the half proving the green above is a measurement rather than a formality.
- **Result:** three gates, not two. `BenchmarkSessionCreate_REQ_TERM_001` and `BenchmarkOutputLatency_REQ_TERM_006` are what the task names; `BenchmarkBlockSearch100k_REQ_BLK_006` already carried a budget from T-F0-10 and was the only NFR benchmark that checked itself, so it joins them rather than being the one gate nobody runs. `scripts/perf_gates.sh` picks which benchmarks are gates, how many samples each gets, and refuses a run in which a gate produced no result line; the budgets stay in the benchmarks, because a threshold written in two places is a threshold that will disagree with itself. `task perf` runs it, and `task ci` now runs `task perf`.
- **Result (the done line is half right).** "An artificial regression (10 ms sleep) turns it red" holds for REQ-TERM-006, whose budget is 5 ms. It does not hold for REQ-TERM-001: 10 ms against a 300 ms budget is noise, and the benchmark stays green — measured, not assumed. `scripts/perf_selftest.sh` therefore sizes each injection against the budget it has to break: 10 ms for the latency gate, 400 ms for the create gate. One number could not have done both, and using one would have left the create gate looking self-tested while nothing tested it.
- **Result (a real defect in the gate itself).** `BenchmarkOutputLatency_REQ_TERM_006` computed p95 as `latencies[int(float64(len(latencies))*0.95)]` and then, meaning to bound the index, compared the *duration* against the *sample count*: `if p95 >= time.Duration(len(latencies))`. At a full one-second run that condition is false and the p95 is right, which is why nobody saw it; at the small sample counts a CI job would use — 100 samples is 100 ns — it is true for any latency above a microsecond, and the benchmark silently gated on the maximum instead. A max-based gate on a shared runner is a flaky gate, and a flaky gate gets disabled. Both call sites now use one `percentile` helper, and the 5 ms budget is one named constant instead of three literals.
- **Result (the selftest is the point).** `scripts/perf_gates.sh` proves the budgets are met; `scripts/perf_selftest.sh` proves they are still being checked. It is the sibling of `scripts/arch_selftest.sh` and exists for the same reason: a dropped `b.Errorf`, a threshold typed in seconds, or a `b.Skipf` on a runner without bash all leave a green job that measures nothing. It refuses to accept a bare non-zero exit — a package that stopped compiling also exits non-zero — and requires the failure to name the requirement whose budget was missed.
- **Result (found by teeth-checking my own script).** The first version of `perf_gates.sh` treated a zero exit as a pass, and I added a `--- SKIP` guard on the assumption that a skipped benchmark says so. It does not: without `-v`, a benchmark that calls `b.Skipf` prints *nothing at all* — no skip line, no result line, just `ok`. `BenchmarkSessionCreate_REQ_TERM_001` skips when bash is missing, so on a runner without it the gate would have disappeared in silence under a green tick. The check is now that the benchmark's result line is present, which also catches the other way a gate quietly stops existing: a rename that leaves a stale name in the table, matching no benchmark and exiting zero. Both were verified by doing them.
- **Result (verification):** both gates teeth-checked in both directions. Injecting the regression turns each red with the expected message; widening `outputLatencyBudget` from 5 ms to 5 s makes the selftest report `FAILED — accepted a 10ms regression`; breaking compilation in `internal/api` makes it report `failed for some other reason than the REQ-TERM-006 budget` rather than counting it as a catch. Current margins, all on the manual machine and none yet on the CI runner: `session.create` 2.1 ms p95 against 300 ms, added output latency 26 µs p95 against 5 ms, `block.search` over 100,000 blocks 2.4 ms p95 against 200 ms.

### [x] 2026-09-20 T-F0-14 · Workspace, tab and pane model
- **What:**
  - `workspaces`, `tabs`, `panes` and `pane_aliases` tables in **migration 0003**, not 0001: 0001 is already applied and migrations are forward-only (Art. 6, Data Model §5);
  - public identifiers `w<n>`, `w<n>:t<m>`, `w<n>:p<m>` with an allocator per session;
  - methods `workspace.*`, `tab.*` and `pane.split|list|get|focus|rename|move|close`;
  - binding of one live session per pane;
  - rollup state per tab and workspace derived from panes.
- **REQ:** REQ-WS-001, REQ-WS-002, REQ-WS-003, REQ-WS-006, REQ-WS-007
- **Files:** `internal/workspaces/**`, `internal/store/migrations/0003_structure.sql`, `internal/api/workspaces.go`, `.go-arch-lint.yml` (new `workspaces` component, Tech Design §5.2)
- **Depends on:** T-F0-03, T-F0-05
- **Note:** this is the largest task in F0. If its first estimate slips, split it into "model and identifiers" and "methods and rollup" (finding B-08).
- **Done:** tests `TestWorkspaceCreateReturnsTree_REQ_WS_001`, `TestPaneIdsStable_REQ_WS_002`, `TestSplitAttachesSession_REQ_WS_003`, `TestRollupPrefersBlocked_REQ_WS_006` and `TestMovedPaneKeepsAlias_REQ_WS_007` green.
- **State:** `[x]` — all five named tests exist under those exact names, run and pass. It closes with one thing outstanding that is not mine to settle: `changes/2026-09-pane-attention-state/` is still `PENDING APPROVAL` while its decisions are already in `specs/api/` and `specs/prd/`, and `rollup_state: "unknown"` on the wire depends on it. `AGENTS.md` and `README.md` say so rather than claiming a clean slate.
- **Result:** `internal/workspaces` in the shape §5.2 prescribes — `domain` with the identifiers, the five attention states, the rollup and the portable layout tree; `ports` with the inbound surface, the tree's persistence and a two-method view of the sessions module; `adapters/treestore` over migration 0003; a service that orders the writes and publishes the §6 notifications; and `internal/api/workspaces.go` with the seventeen methods of §5.4 to §5.6. Every forbidden import the new rules describe was injected and confirmed rejected — `workspaces` into `api`, into `sessions/adapters`, and its `domain` and `ports` into `store` — rather than taken on trust.
- **Result (two spec gaps, raised as delta `2026-09-pane-attention-state`).** REQ-WS-006's last clause propagates `unknown`, and API §4's `rollup_state` listed four values without it. In F0 that is not a corner case but the only case: a pane's state comes from `pane_state_reports`, which is migration 0004, so every workspace holding a pane rolls up to a value the wire type forbade. And nothing in F0 gives a pane any state at all — §4 names `umbral:shell` as "derived from the block lifecycle", which is F0 code, but no requirement mandates the derivation and no artifact says which block state maps to which attention state. `unknown` joins the type; the derivation is recorded as deferred rather than invented.
- **Result (the allocator has to look somewhere the schema does not).** `panes.id` and `pane_aliases.alias_id` are primary keys of different tables, so nothing in migration 0003 stops a new pane being called `w1:p2` while an alias of that name still points at the pane that moved away. `GetPane` would then answer with whichever table it read first, and REQ-WS-002's uniqueness would fail for the one identifier a user is most likely to have written down. `nextPaneOrdinal` therefore takes its maximum over both tables. Nothing else in the suite catches this: `TestANewPaneNeverTakesAnAliasedName_REQ_WS_002` was added after a teeth check showed the alias half of that query could be deleted with every other test still green.
- **Result (where two artifacts had nothing to say).** Three decisions the specs leave open, written down rather than left in the code. **One:** `tabs` has no `focused_pane_id` column but API §4's `Tab` has the field, so it lives in `layout_json` — which Data Model §2.4b describes as holding the "portable tree (API Layout)", and the API `Layout` is the object that carries it. **Two:** §5.6 says where a moved pane goes but not where it lands inside the destination; it goes beside that tab's focused pane, split right at the default ratio, and an empty tab takes it as the root. **Three:** `pane.split` takes no size because the daemon cannot see the client's window, so a pane's terminal starts at 80x24 and the client's first `session.resize` corrects it.
- **Result (a pane rename that the schema would have refused).** `pane_aliases.pane_id` is a foreign key on `panes(id)` with no `ON UPDATE`, so renaming a pane that any alias references fails outright with foreign keys on. The move clears the alias rows, renames the parent, then rewrites them against the new identifier — which also keeps the chain flat, so a pane moved three times answers to all four of its names directly instead of through three lookups.
- **Result (capabilities stopped being true, and were made true).** Registering the seventeen methods unconditionally made the handshake advertise `workspace`, `tab` and `pane` on a daemon where the tree was not wired and every one of them answered METHOD_NOT_FOUND — exactly the drift `capabilities()` derives itself from the method table to avoid. The table is now built from the configuration, so a module that is not wired contributes no methods and advertises no capability.
- **Result (a defect only concurrency showed).** Eight clients calling `workspace.create` at once: five of the eight failed. Allocating an identifier is a read — `max(...)` over the rows — and then the insert that uses it, and SQLite's default `BEGIN DEFERRED` starts such a transaction as a reader, taking the write lock only at the insert. Two of them cannot both finish; the loser gets `SQLITE_BUSY_SNAPSHOT`, which the `busy_timeout` already in the DSN does **not** wait out, because there is nothing to wait for — its snapshot is stale and the only cure is to roll back. Uniqueness was never in danger; the writes happening at all was. The DSN now sets `_txlock=immediate`, so a transaction takes the write lock up front and contenders queue on the timeout instead of failing, and `treestore.Layout` dropped its transaction so a single-statement read does not queue behind writers for nothing. It is a change to `internal/store`, shared by every module, because the flaw is in the shape — read then write — and not in this module. `TestConcurrentCreatesDoNotCollideOrFail_REQ_WS_002` is the regression test and was teeth-checked by putting the default back.
- **Result (verification):** `task ci` green, including the boundary probes and 0 lint issues. Twelve tests over a real SQLite database plus the domain suite, and five deliberate mutations confirmed the named ones bite: restoring SQLite's default transaction mode, dropping the alias half of the allocator, dropping the alias record on a move, pinning the rollup to `idle`, and skipping the root pane's terminal each turn a criterion test red. Then the whole surface was driven against a real `umbrald`: `workspace.create` returned `w1`, `w1:t1`, `w1:p1` with a live session and `rollup_state: "unknown"`; `pane.split` returned the pane and a layout whose root is a `right` split at 0.6; `pane.move` carried `w1:p2` to `w2:p2` **keeping the same `session_id`**; `pane.get` on the retired `w1:p2` answered with `w2:p2` and `aliases: ["w1:p2","w2:p2"]`; and a `cli` client asking for `workspace.list` was refused with METHOD_NOT_FOUND, as API Spec §2 requires.
- **Not in this task:** `layout.export` and `layout.apply` are T-F0-15, which is why the tree is built and stored but only `pane.split` and `pane.move` return it — and why §6's `layout.updated`, which §6 attributes to REQ-WS-004, is not emitted either. Restoring the structure after a restart is T-F0-18. Two clauses of §5.4/§5.6 wait on F1: `workspace.close` failing with `CONFLICT` while a thread of that workspace is `running` has nothing to check yet, since threads arrive with T-F1-01, and `pane.split`'s `command` waits on the launch path T-F0-15 builds.

### [ ] T-F0-15 · Portable layouts
- **What:** `layout.export` producing the binary tree with labels, cwd and command; `layout.apply` recreating a tab from that tree and declaring in its response that processes and scrollback are not reproduced.
- **REQ:** REQ-WS-004, REQ-WS-005
- **Files:** `internal/workspaces/layout/**`, `internal/api/layout.go`
- **Depends on:** T-F0-14
- **Done:** round-trip test `TestLayoutExportApplyRoundTrip_REQ_WS_004` plus `TestApplyWarnsNoProcesses_REQ_WS_005` green.

### [ ] T-F0-16 · Snapshot and event sequencing
- **What:**
  - monotonic `seq` per session on every notification;
  - `session.snapshot` with focused ids, records, layouts and the `seq` it contains;
  - documented bootstrap protocol (subscribe → snapshot → apply the buffer).
- **REQ:** REQ-API-001, REQ-API-002
- **Files:** `internal/api/snapshot.go`, `internal/bus/**`
- **Depends on:** T-F0-14
- **Done:** `TestSnapshotCarriesSeq_REQ_API_001` and `TestNoGapBetweenSnapshotAndStream_REQ_API_002` (concurrent client under load) green.

### [ ] T-F0-17 · Protocol schema and capability degradation
- **What:**
  - capability list in `system.hello`;
  - unknown method → `METHOD_NOT_FOUND` without closing the connection;
  - `umb api schema --json` generated from the Go types;
  - CI check that compares the schema with `specs/api/umbral-daemon-api-v1.md` (methods and error codes).
- **REQ:** REQ-API-003, REQ-API-004
- **Files:** `internal/api/schema.go`, `cmd/umb/api.go`, `tools/api_schema_check.py`
- **Depends on:** T-F0-03
- **Done:** `TestUnknownMethodKeepsConnection_REQ_API_003` and `TestSchemaMatchesSpec_REQ_API_004` green; a method added to the code without the spec turns CI red.

### [ ] T-F0-18 · Structure restore after restart
- **What:**
  - persist the structure and relaunch shells or `command_json` on start;
  - mark previous sessions `exited`;
  - `pane_history` table and its opt-in replay, disabled by default.
- **REQ:** REQ-TERM-009, REQ-TERM-010, REQ-TERM-011
- **Files:** `internal/workspaces/restore*.go`, `internal/store/**`, `internal/config/**`
- **Depends on:** T-F0-14
- **Done:** `TestRestoreRebuildsStructure_REQ_TERM_009`, `TestPaneHistoryDisabledByDefault_REQ_TERM_010` and `TestRestoreNeverRunsStoredCommand_REQ_TERM_011` green.

## Traceability matrix (F0)

| REQ | Tasks | Tests citing it |
|---|---|---|
| REQ-TERM-001 | T-F0-05, T-F0-13 | TestCreateSession_REQ_TERM_001, BenchmarkSessionCreate_REQ_TERM_001 |
| REQ-TERM-002 | T-F0-07 | Conformance_REQ_TERM_002 |
| REQ-TERM-003 | T-F0-06 | TestSessionSurvivesNoClients_REQ_TERM_003 |
| REQ-TERM-004 | T-F0-04, T-F0-06 | TestSubscribeSnapshotBeforeLive_REQ_TERM_004, TestSnapshotRoundTrip_REQ_TERM_004, TestRebaseKeepsWhatTheSnapshotDoesNotContain_REQ_TERM_004 |
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
| REQ-CLI-003 | T-F0-11 | TestUmbAutostartFailsWith69_REQ_CLI_003, TestAutostartFailureExits69_REQ_CLI_003 |
| REQ-CLI-004 | T-F0-11 | TestBlockLastExits69WhenTheDaemonIsUnavailable_REQ_CLI_003, TestWriteFailureIsNotReportedAsSuccess_REQ_CLI_004, TestBrokenPipeIsNotAFailure_REQ_CLI_004 |
| REQ-TUI-001 | T-F0-12 (+ T-F1-20) | TestTUIBlockNavigation_REQ_TUI_001, TestTabsAndSwitching_REQ_TUI_001, TestSplitResizesBothPanes_REQ_TUI_001 |
| REQ-WS-001 | T-F0-14 | TestWorkspaceCreateReturnsTree_REQ_WS_001 |
| REQ-WS-002 | T-F0-14 | TestPaneIdsStable_REQ_WS_002 |
| REQ-WS-003 | T-F0-14 | TestSplitAttachesSession_REQ_WS_003 |
| REQ-WS-004 | T-F0-15 | TestLayoutExportApplyRoundTrip_REQ_WS_004 |
| REQ-WS-005 | T-F0-15 | TestApplyWarnsNoProcesses_REQ_WS_005 |
| REQ-WS-006 | T-F0-14 | TestRollupPrefersBlocked_REQ_WS_006 |
| REQ-WS-007 | T-F0-14 | TestMovedPaneKeepsAlias_REQ_WS_007 |
| REQ-API-001 | T-F0-16 | TestSnapshotCarriesSeq_REQ_API_001 |
| REQ-API-002 | T-F0-16 | TestNoGapBetweenSnapshotAndStream_REQ_API_002 |
| REQ-API-003 | T-F0-17 | TestUnknownMethodKeepsConnection_REQ_API_003 |
| REQ-API-004 | T-F0-17 | TestSchemaMatchesSpec_REQ_API_004 |
| REQ-TERM-009 | T-F0-18 | TestRestoreRebuildsStructure_REQ_TERM_009 |
| REQ-TERM-010 | T-F0-18 | TestPaneHistoryDisabledByDefault_REQ_TERM_010 |
| REQ-TERM-011 | T-F0-18 | TestRestoreNeverRunsStoredCommand_REQ_TERM_011 |

**Deferred:** REQ-BLK-008 (SHOULD, PowerShell) moves to F2 together with Windows.

## Execution log

| Date | Tasks | Result | Notes |
|---|---|---|---|
| 2026-09-20 | T-F0-12 | code done, `[~]` pending the manual checklist, after a `spec-guardian` round | The interesting part was not the Bubble Tea model. It was that the task's one line — "session rendering from `session.output` (client-side emulator)" — had no home in the boundary rules, and that choosing a renderer is a decision with a long tail: sharing the daemon's libghostty costs `umbral-tui` its pure-Go build and buys the guarantee that the screen and the block history cannot disagree about the same byte stream. Delta `2026-09-tui-renderer` records it. Driving the finished path against a real daemon found a bug no fake could: the client announced `client_kind: "cli"` for every connection, and `session.*` is outside the `cli` allowlist of API Spec §2, so the TUI's first call failed as `METHOD_NOT_FOUND`. Eleven tests, all checked for teeth. The manual checklist is `docs/qa/f0-tui.md` and is a human's job: raw mode, a real keyboard and a real font are what it covers — which is why the task is `[~]` and not `[x]`, a distinction I had got wrong before the review caught it. The review found four more things worth having. Re-attaching after a dropped subscription replayed the fresh snapshot into a screen that still held the old one, which violates the snapshot's own precondition — it reproduces the daemon's screen only when replayed into an *empty* emulator — so `ports.Screen` gained `Reset`. The test I had written for that path could not have caught it: it asserted that bytes had been appended, which is true either way. The snapshot and the first live chunk race each other into the model as independent messages, so output is now held until the snapshot lands and `lastSeq` never goes backwards. And the overflow path reported "you fell behind" with a blocking send on the channel that was full because the reader had fallen behind, which would have parked the goroutine forever and never delivered the message. `internal/tui/adapters/daemon` also had no tests at all, which is precisely where the `client_kind` bug had lived; it now has table tests against literal API Spec §6 frames. |
| 2026-09-20 | T-F0-11 | done after a `spec-guardian` round | The task's own design question was where the socket path lives: `internal/client` may not import `internal/api`, so the runtime-directory resolution moved to `internal/config` rather than being copied, because copying it would have copied `verifyPrivateDir` with it. Two things turned up that the task description did not predict. `errcheck`, firing on the `io.Writer` refactor, was pointing at a real defect: `umb block last --json > /full/disk` exited 0 with nothing written, so every write now goes through one printer whose first failure becomes exit 1, with `EPIPE` excluded because `umb status | head -1` closes the pipe deliberately. And the teeth check on that very test failed: it passed for the wrong reason, because `printJSON` encoded straight into the stream and its write error never reached the exit-code path. Encoding into a buffer first put both paths back together, and the test then bit. Sixteen tests, all checked for teeth. The review then found three defects the tests had not: concurrent `umb` invocations each starting a daemon that recovered over the previous one's live sessions (reproduced: five daemons on one installation, now exactly one, behind an `flock` taken before the database is opened); a handshake with no deadline that `Ctrl-C` could not interrupt; and an autostarted daemon logging to `/dev/null`, against Art. 7. Fixing the second introduced a data race of my own, caught by `-race` on half the runs. Two deltas came out of the review and are pending ratification: `2026-09-cli-surface` and, from the reconciliation, `2026-09-structure-migration`. Verified against the real daemon rather than only against fakes: `umb status` autostarts `umbrald` on a clean `XDG_RUNTIME_DIR`, and `umb block last --json` prints the block for a `false` that really ran, with `exit_code: 1`. |
| 2026-09-20 | reconciliation | done | Merging the two spec lineages surfaced a real defect in T-F0-06, not just the flaky test that exposed it. `session.subscribe` registered the subscription, took the snapshot, and then called `subscribe` again with the snapshot's seq — which closes the subscription and creates a new one, throwing away everything queued in between. The early registration exists precisely to keep that window; output produced during the snapshot survived only when the bus happened to deliver it after the replacement, which is why `TestSubscribeSnapshotBeforeLive_REQ_TERM_004` failed about one run in three under `-race` rather than always. The handler now rebases the existing subscription, dropping the prefix the snapshot already shows. `TestRebaseKeepsWhatTheSnapshotDoesNotContain_REQ_TERM_004` pins it deterministically and was checked for teeth. Separately, `TestNoChunkWaitsLongerThanTheBatchInterval` timed a single socket round-trip against the 4 ms batch ceiling, which the race detector's own overhead exceeds; it now takes the median of 25 lone chunks and still fails at a 4.48 ms median when a fixed timer is injected. |
| 2026-09-11 | T-F0-10 | done | The first task whose requirement was missed on the first measurement, by six times: `block.search` p95 was 1.23 s against a 200 ms budget, and a `LIMIT 50` list page took 129 ms. `EXPLAIN QUERY PLAN` named both causes. `blocks` had no index on its default ordering, only on `(session_id, started_at)`, so an unfiltered page scanned and sorted 100,000 rows. And ordering a full-text search by `started_at` puts a temporary B-tree over the whole match set, so a term matching half the history sorted fifty thousand rows to return fifty; no index helps, because the rows arrive from the full-text index in rowid order. Ordering by that rowid instead is 300 times faster and returns the same list, since a block's row is written when its command starts. Both changes needed spec text and were folded on approval into Data Model v1.3 and API Spec v1.4. Six properties were checked for teeth. Two things found while writing it: FTS5 rejects a bare path as a syntax error, so a client typing one has to quote it; and `blocks_fts` is keyed on implicit rowids, which `VACUUM` may renumber, silently desynchronising the index, which the delta records. |
| 2026-09-11 | T-F0-09 | done | Two defects in already-"done" work surfaced only when a real shell was asked for a real exit code. First: the bash and zsh bootstraps read `$?` in a hook registered last, and every element of `PROMPT_COMMAND`/`precmd_functions` leaves `$?` set to its own result, so with Starship installed every command was recorded as exit 0. The work is now split in two, a capture hook first and the marker hook last, because the two halves want opposite positions. Second: the daemon never wired libghostty's write-pty effect, so no program's query to the terminal was ever answered; fish waits two seconds for a Primary Device Attributes reply and then permanently disables features, and under a retrying test it never timed out at all. Both were found by `TestBlocksInEveryShell_REQ_BLK_005`, which exists because REQ-BLK-005 covers three shells and only running all three proves they agree. A third, smaller one was found by the plain-text test: carriage return was treated as "erase the line", which is right for a progress bar and wrong for the CRLF a PTY ends every line with, so the decision is now deferred one byte. The scanner and the recorder were each checked for teeth by breaking three and four properties respectively. Deliberately not done: `block.list`/`get`/`search` are T-F0-10, and chunk writes sit on the drain goroutine, after the chunk has been published, so they delay the history and never the screen. |
| 2026-09-11 | T-F0-07 | done | 22 cases: VT-01…VT-20 MUST plus VT-21 and VT-22 SHOULD. Everything passes against libghostty on the first run, which is expected rather than suspicious: the emulator is Ghostty's own and these are the sequences it exists to implement. The value is the regression net, not the discovery. Worth noting from the fixtures: VT-20 confirms the shell-integration OSC sequences leave no visible mark, which is what lets T-F0-09 read them without corrupting the screen. |
| 2026-09-11 | T-F0-06 | done | All three tests were checked for teeth. Two bit immediately; the ordering test did **not**, because the fake published output after subscribe returned and the race window was never exercised. Making the fake publish *inside* the snapshot call still did not bite reliably, since whether the dispatch goroutine ran in the window was up to the scheduler. It now asserts the ordering where it happens, that a subscription exists at the moment the snapshot is taken, which fails deterministically when the order is reversed. Verified end to end against a real bash session: the snapshot carries the pre-subscribe output, the live stream carries only what came after, and the sequence numbers are strictly increasing and all above the snapshot's. |
| 2026-09-11 | T-F0-05 | done | Both security-relevant tests were checked for teeth by breaking what they guard: writing the input before checking the lock fails REQ-TERM-008's canary check, and resizing only the bookkeeping fails REQ-TERM-007's `tput cols`. Verified end to end over the real socket: create, input, resize with its notification, list, close and the exited notification. One observation worth keeping: input written to a PTY before the shell's line editor is ready can be lost, so an end-to-end script that types immediately after `session.create` sees a wrong exit code. With the shell settled, bash, zsh and fish all report the real code. `session.*` is restricted to the `tui` and `desktop` client kinds, since API Spec §2 does not grant it to `cli`. |
| 2026-09-11 | T-F0-08 | done | bash 5.3.9, zsh 5.9 and fish 4.2.1, each driven through a PTY with `creack/pty`. The prompt-framework clause is covered by a fake rc that reassigns the prompt on every prompt, so it holds on a CI runner with neither Starship nor powerlevel10k installed; the machine that found the bug had Starship in its own rc. Both new tests were checked for teeth by breaking the thing they guard. fish skips the configuration-preservation case by design: `--init-command` runs after `config.fish`, so there is nothing to restore. |
| 2026-09-11 | T-F0-04 | done | First cgo in the repository. `libghostty-vt` is built from ghostty's source with Zig 0.16.0 by `scripts/build_libghostty.sh` (`task deps:ghostty`); the Taskfile points `PKG_CONFIG_PATH` at the default prefix so no Go target needs the caller to export anything, and CI builds it in the lint and test jobs. Measured numbers and the scrollback finding are in `docs/spikes/q01-snapshot.md`. Not covered here and deliberately left to their own tasks: reflow on resize (T-F0-07), real PTY output (T-F0-05), macOS. |
| 2026-09-11 | T-PKG-01, T-PKG-02 | done | Recorded here because the hardening tasks file did not exist yet; both tasks now live in `umbral-hardening-tasks.md`. Closes Phase 0. The kit reproduces byte for byte from `tools/build.py`, verified by regenerating it: 55 of 56 artifacts identical, the `.desktop` file the only intended change. Finding A-12 honoured: REQ-PKG-004, 005, 007 and 008 went to `specs/prd/umbral-f2-desktop.md` instead of becoming MVP MUSTs nothing could close. Finding A-15 closed: the checksums were regenerated after the `.desktop` change. Finding A-16 closed: the superseded Spanish copy of the delta was deleted. |
| 2026-09-11 | T-F0-03 | done after a `spec-guardian` round | The review returned FIX FIRST with 1 CRITICAL and 5 HIGH. The critical was real and reproduced with a probe: a token file that existed but was empty kept its old mode, because `os.WriteFile` does not apply its mode argument to an existing file, so the token landed at 0664. It is now written through `OpenFile` with an explicit `Chmod` and the mode is read back. Also fixed: the handshake validated `client_kind` before the token, so a bad token plus a bad kind answered `VALIDATION_ERROR` and left the connection open, against REQ-SEC-003; the runtime-directory fallback trusted a world-writable parent; `trace_id` carried a freshly minted id that correlated to nothing; `capabilities` advertised `sessions` and `blocks` with no such method registered; `go.mod` recorded a direct dependency as indirect. Five decisions the spec does not cover went into `changes/_archive/2026-09-api-f0-decisions/` instead of staying in comments. |
| 2026-09-11 | T-F0-02 | done | `internal/store` with migration 0001, pragma verification, forward-only migrations and restart recovery. Driver: `modernc.org/sqlite` (pure Go, no cgo). `threads` is created in 0001 per the folded delta, and the regression test for A-01 was checked by reverting the fix: it fails with `no such table: main.threads`, exactly as the Analyze described. Steps 3-4 of Data Model §6 wait for T-F1-01, which creates the tables they touch. |
| 2026-09-11 | T-F0-01 | done | Go 1.27.1 installed under `~/.local/go` without root, since the machine had no Go at all. `task lint` (gofumpt, go vet, golangci-lint with gosec, govulncheck, gitleaks), `task arch`, `task arch:selftest` and `task test -race` all green. `gosec` G115 is excluded for now with a written reason: it fires on every epoch-ms and micro-USD conversion Art. 6 mandates, and it is re-enabled once the store layer exists. **Zig is still missing**, so T-F0-04 and T-F0-05 cannot build libghostty yet. |
