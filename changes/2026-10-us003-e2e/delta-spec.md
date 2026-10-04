# Delta — how the live US-003 run is set up and judged, and what the repair message says

| Field | Value |
|---|---|
| **Status** | `PROPOSED` |
| **Date** | 2026-10-04 |
| **Task** | T-F1-21 |
| **Raised by** | T-F1-21: PRD §4.1's goals and US-003, REQ-AGT-006, which leave these points open |

## Evidence

- **T-F1-21's What** asks for a fixture, a script run 20 times and a report. It does not say:
  - what counts as a success;
  - what "auto-approve `run_command go test`" approves when a line only starts with `go test`;
  - what the other approvals get;
  - how offline is shown beyond an empty `egress_log`.
- **T-F1-21's Files** name `e2e/us003/**`. The harness that starts an isolated daemon — every
  XDG directory redirected, the process owned and stopped — lives in `cmd/umbrald`'s tests.
  AGENTS.md says nothing below `cmd` wires the daemon.
- **PRD §4.1** measures "the rate of invalid tool calls *after repair*" against < 5 %. No spec
  says how to count it from what a turn records.
- **REQ-AGT-006** retries an invalid call "with a repair message". API and Tech say the message
  names the tool and its schema, but not that the error is readable. The first 20-run pass sent
  `list_dir: /: &{[depth]}`, the validator's Go value. The model resent the same property, and
  two turns ended `tool_error`.
- **`task arch`** attaches every `.go` file in the tree to a component, and the fixture is not
  Umbral's code.

## Decisions

1. **Where the pieces live.**
   - `e2e/us003/fixture` is the repository: a module of its own, `example.com/us003fixture`. Its
     `Median` returns the upper middle of an even sample instead of the mean of the two middle
     values.
   - Being a separate module keeps it out of `go test ./...` and `golangci-lint ./...`.
     `.go-arch-lint.yml` excludes it.
   - `e2e/us003/run.sh` is the script.
   - The driver is `cmd/umbrald/us003_live_test.go`, behind the `live` tag like
     `live_test.go`. It reuses the daemon isolation the other real-daemon tests share
     (`isolatedRuntime`, `startDaemonLoggingTo`, `requireModel`) instead of copying it outside
     `cmd`.
   - `UMBRAL_US003_RUNS` (20 by default) and `UMBRAL_US003_REPORT`
     (`docs/reports/us003-<date>.md` by default) change it.
2. **One run.**
   - A fresh copy of the fixture, committed under git.
   - `session.create` in the copy, with `go test ./...` typed in it. The first `block.closed` of
     that session for that command, failing, is the block.
   - An `auto-edit` thread in the copy is sent `fix it` with `{kind: "block", ref}`.
   - The turn is followed to `thread.turn_finished`, or to a 10 min bound, after which it is
     cancelled.
3. **Approvals.**
   - `run_command` is approved `once` when its line is `go test` as a user approving it would
     mean it:
     - one command, with no `;`, `&`, `|`, backquote, `$`, `<`, `>`, quote, backslash or newline;
     - package patterns inside the repository (`.`, or `./…` with no `..` component);
     - flags from an allowlist only: `-v`, `-race`, `-short`, `-failfast`, `-run`, `-count` and
       `-timeout`.
   - `-exec`, `-toolexec`, `-o`, `-c` and the profile flags run another program or write
     elsewhere. Any of them is denied, which keeps decision 5's "offline" honest.
     `TestUS003ApprovesOnlyAPlainGoTest` holds the rule.
   - Anything else the policy asks about is denied `once`.
   - `auto-edit` already writes inside the repository without asking (Tech §5.3), so this is
     US-003's "approve the diff" with the diff already applied.
   - Nothing is remembered between runs: each run is a new thread, and `once` creates no rule.
4. **Success.**
   - `go test -count=1 ./...` passes when the harness runs it itself, outside the daemon.
   - `stats_test.go` is byte-identical to the fixture's.
   - `git status --porcelain --untracked-files=all` lists no path but `stats.go`.
   - A run that makes the tests pass by editing the test, by adding a file (say, a `TestMain`
     that exits 0) or by touching `go.mod` fails, and the report names what changed.
5. **Offline, shown rather than assumed.**
   - `models.toml` sets `[router] offline = true` and lists a remote candidate,
     `remote/big-model` at `https://remote.invalid/v1`, **first** in `code` and `fast`.
     REQ-LLM-004 must discard it without contacting it.
   - The daemon's environment carries `GOTOOLCHAIN=local` and `GOPROXY=off`, and the panes and
     the agent's commands inherit them. Go itself cannot fetch a toolchain or a module.
   - After the daemon stops, two counts are read from its database, read-only: `egress_log`, and
     the `usage` rows of the remote provider. Both must be 0.
   - **What the run shares with the developer's machine.** The daemon has its own XDG
     directories and its own `HOME`. The Go build cache (`GOCACHE`) and module cache
     (`GOMODCACHE`) are the developer's: they are read in the test process and passed to the
     daemon. That keeps each run's builds warm and fetches nothing. The socket, the database and
     the config are never the developer's.
   - `GOFLAGS=-mod=mod`, `num_ctx = 16384` and `keep_alive = "30m"` are set too. All of them
     shape the latency the report measures.
6. **What the report measures.**
   - Runs green, against 70 % and 14/20.
   - Rows in `egress_log`, against 0.
   - Invalid calls **before** repair: every `tool_calls` row `invalid_args` out of every row.
   - Invalid calls **left after** repair, PRD §4.1's measure, against < 5 %.
     - **Numerator:** the turns that ended `tool_error` with two or more invalid calls.
       REQ-AGT-006 gives an invalid call one repair and ends the turn `tool_error` at the first
       invalid retry, and nothing else ends a turn `tool_error` (`errRepairFailed`). So each such
       turn holds exactly one call the repair did not fix.
     - An invalid call whose turn ended another way before its retry is not counted:
       `provider_error`, a timeout, a cancel.
     - **Denominator:** every `tool_calls` row of the run. PRD §4.1 says "the rate of invalid
       tool calls after repair", a rate over tool calls, beside the rate before repair over the
       same calls.
     - The report also gives the same count over the invalid calls, "repairs that failed". That
       measures the repair, not the model's calls, and is not the PRD's target.
     - The PRD asks "per supported local model". This run measures one, `gpt-oss:20b`.
   - Every `end_turn` run must have streamed at least one `thread.delta`. That is REQ-AGT-001's
     response, and the harness asserts it.
   - Runs that edited the test.
   - Latency from the send to the turn's end: p50, p90 and max.
   - Tokens.
   - Per run: stop reason, tool calls, text deltas, tools, approvals and denials. The column is
     "Calls", not "Steps", which REQ-AGT-008 uses for model steps.
   - Every invalid call with its repair message, and every failed `usage` row with its error.
7. **The repair message names what is wrong in words** (REQ-AGT-006).
   - The registry sends the validator's English text for each failing location, for example
     `list_dir: /: additional properties 'depth' not allowed`.
   - It no longer sends the kind's Go value.
   - `TestTheRepairNamesWhatIsWrong_REQ_AGT_006` holds it.
   - This is a fix to T-F1-15's message, found by this run. No sentence of the spec described
     the old text.
8. **The result.** These are the final pass's figures, with decisions 3–7 as written. The
   report's "Analysis" section is written by hand; the rest is generated.
   - 16/20 green (80 %).
   - 0 rows in `egress_log`, and 0 `usage` rows for the remote candidate.
   - Invalid calls: 13 of 203 before repair (6.4 %) and 1 of 203 after (0.5 %). Repairs failed
     for 1 of 13 invalid calls.
   - p50 latency 1m37s. p90 9m59s: the three runs cancelled at 10 minutes.
   - The earlier passes are kept for comparison:
     - The pass before decision 7, from an earlier harness revision, scored 15/20, with 19
       invalid calls of 128. It is kept as `docs/reports/us003-2026-10-04-before-repair-fix.md`.
     - The pass right after decision 7, under looser approval and success rules, scored 17/20.
   - The four failures:
     - **Runs 11 and 14:** `read_file`/`edit_file` loops cut at 10 minutes.
     - **Run 6:** the same loop, ended by Ollama failing to parse gpt-oss's tool call
       (`provider_error`).
     - **Run 15:** a repair that did not take.
   - Run 1 counts as green: the fix was in place, but the turn ran on to the 10-minute cancel.
9. **Left for later, not decided here.**
   - Eight of the thirteen invalid calls are `list_dir` with a `depth`. Giving `list_dir` a
     `depth` would lower the rate before repair. It changes a built-in's schema (REQ-AGT-002),
     so it needs a delta of its own.
   - The plan's risk row, "local models below 70 % on US-003", did not trigger.
   - **`edit_file` calls that do not apply, read back and retried, cost the most time.** They
     account for every cancelled run. The harness does not record why an edit failed. Measuring
     that comes before any change to the tool or the prompt.
   - **Whether a run whose turn never ends should count as a failure** even when the repository
     is fixed. Run 1 is the case. Here it counts green, because success is defined by the
     repository's state (decision 4).

## What changes in the specs on ratification

- **PRD §4.1:** a note under the goals table. "After repair" counts the calls the one repair of
  REQ-AGT-006 did not fix, over every tool call, per decision 6.
- **`specs/tasks/umbral-f1-tasks.md`:**
  - T-F1-21 is marked done. Its **REQ** line gains REQ-AGT-006.
  - Its Files: `e2e/us003/**`, `cmd/umbrald/us003_live_test.go`, `.go-arch-lint.yml`,
    `internal/tools/adapters/registry/registry.go`, `docs/reports/us003-2026-10-04*.md`.
  - The matrix:
    - REQ-AGT-001: `e2e us003` becomes `TestUS003FixItOffline_REQ_AGT_001 (live, e2e/us003/run.sh)`;
    - REQ-LLM-004 and REQ-SEC-002 keep their named tests. They say in prose that the live run also
      exercises them, since a `-run REQ_LLM_004` does not select a test named for REQ-AGT-001;
    - REQ-AGT-006 gains T-F1-21 in its task column and adds
      `TestTheRepairNamesWhatIsWrong_REQ_AGT_006`.
- **Tech Design:**
  - §3.3 item 2, where T-F1-15's repair message is described: decision 7, the message carries
    the validator's text.
  - §8, Testing Strategy: the live US-003 run, decisions 1–6.
- **Plan:** T-F1-21 ☑, with the result.
