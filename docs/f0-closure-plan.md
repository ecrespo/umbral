# Closing phase F0 — the remaining work, in order

> Written 2026-09-21 from the validation in `docs/checkpoints/2026-09-21-f0-validation.md`.
> This is the hand-off: what is left, who owns it, what "done" means for each item, and how
> to verify it. It is not a new plan — every item below already exists in
> `specs/plans/umbral-mvp-plan.md` or `specs/tasks/umbral-f0-tasks.md`; this file puts them in
> one place and in the order they can actually be worked.
>
> **Delete this file when F0 closes.** It exists to be finished, and a hand-off that outlives
> its hand-off is the next person's misinformation.

## Where F0 stands

Every implementation task from `T-F0-01` to `T-F0-19` is done, plus `T-PKG-01/02`. Four
things are between here and a closed phase, and **two of them are a human's** — no amount of
code shortens them.

| # | Item | Owner | Blocked on | Closes |
|---|---|---|---|---|
| 1 | ~~Ratify `changes/2026-09-cli-workspace-surface/`~~ — ratified 2026-09-26 | human | — | unblocks item 3 |
| 2 | ~~`T-F0-21` — REQ-BLK-003's verdict at exit~~ — done 2026-09-26 | code | — | a MUST, and a reliable `task ci` |
| 3 | `T-F0-20` — `umb workspace`/`tab`/`pane`/`layout` | code | an API §2 delta: the `cli` client kind may not call those methods | exit criterion 4 |
| 4 | A week of the TUI as the main terminal | human | — | exit criterion 3 |

Items 1 and 2 are done. Item 4 can run alongside everything else. Item 3's five tests exist,
red, on `feat/T-F0-20-cli-workspace`; before any code, API §2 has to let `umb` (`client_kind:
cli`) call `workspace.*`, `tab.*`, `pane.*` and `layout.*` — found on 2026-09-26, missed by the
CLI delta, which said the API Specification was not touched.

## The six exit criteria, and what each one still needs

From `specs/plans/umbral-mvp-plan.md` §F0. The phase closes when all six are met **and**
REQ-BLK-003 holds.

| # | Criterion | Status | What closes it |
|---|---|---|---|
| 1 | VT conformance suite 100 % MUST green (REQ-TERM-002) | ☑ met | `T-F0-07`, 20 MUST cases |
| 2 | Correct blocks in bash, zsh and fish in CI | ☑ met | `TestBlocksInEveryShell_REQ_BLK_005`; the Linux job installs all three |
| 3 | The TUI used one week as the main terminal, no blocking regressions | ☐ **open** | item 4 below |
| 4 | A script creates a workspace, splits, exports and reapplies a layout using only the CLI | ☐ **open** | items 1 and 3 below |
| 5 | The structure comes back after `kill` on the daemon | ☑ met | verified end to end 2026-09-20: `kill -9`, restart, five panes with labels, cwds, fresh sessions, focus preserved |
| 6 | Benchmarks within the NFRs | ☑ met | three of four are gates; idle memory measured 2026-09-21 at 37,6 MiB against 80 |

Criterion 6 is met but **ungated** — it carries no REQ id, so `task perf` does not defend it.
That is recorded in `T-F0-13` and is deliberate; the measurement below is the evidence, not a
regression test.

## 1 · Ratify the CLI delta (human) — ✅ done 2026-09-26

`changes/_archive/2026-09-cli-workspace-surface/` — `delta-spec.md` and `tasks.md`. Ratified
and archived on 2026-09-26; what follows is kept as the record of what was approved.

It exists because F0 exit criterion 4 asks for a feature nobody specified, and because Art. 6's
amendment justifies the `w<n>` identifiers on the strength of `umb pane split w1:t1` being
"the feature" — a command that does not exist. Six decisions are written down; the ones worth
a second look before signing:

- the CLI mirrors the served methods one to one, with **no client-side model** (DD-001);
- objects are addressed positionally — `w1`, `w1:t2`, `w1:p3` — which is exactly the grammar
  the Art. 6 exception was written for;
- **`pane.move` is deliberately out of scope**: its `destination` is a tagged union with more
  than one defensible flag syntax, the criterion does not need it, and a CLI verb is kept
  forever;
- the interchange form is `umb layout export --json` and `umb layout apply --from <file|->`.

**To ratify:** change the status line to `RATIFIED <date>`, move the directory to
`changes/_archive/`, and say so in `AGENTS.md`'s known status. Nothing else has to move —
the requirements are already in the PRD (1.11), which is this repo's convention for a delta
awaiting approval, and the checker reads pending deltas, so leaving them out of `specs/` is
what produces HIGH findings.

## 2 · `T-F0-21` — settle the integration verdict at exit (code) — ✅ done 2026-09-26

Full task in `specs/tasks/umbral-f0-tasks.md`. Summary:

`internal/sessions/blocks.go:120` holds the only thing in the daemon that ever writes
`integration: none`, and `internal/sessions/lifecycle.go:246` cancels it when the shell exits.
A session whose process ends inside the five-second window therefore stays `pending` forever —
and `finish` leaves the session in the live map on purpose, so `session.list` and the row both
go on saying `pending` about a process that is gone. REQ-BLK-003 says it SHALL be `none`.

**Shape of the fix:** settle the verdict in `finish` instead of cancelling it. `pending`
becomes `none`; a session already on `osc133` is untouched, which `setIntegration`'s existing
transition rule gives for free — so the change is about *which* call replaces `Stop()`, not
about new state logic.

**TDD, per Art. 2.** Write `TestAnExitInsideTheWindowStillSettlesIntegration_REQ_BLK_003`
first, from the criterion: a session whose command exits in about a second reaches
`integration: none` and never sits on `pending`. Watch it fail. **Then check the teeth** —
put `integrationTimer.Stop()` back and confirm it reddens.

**Done when:** that test is green, and
`TestACommandPaneGetsNoShellIntegration_REQ_BLK_003` passes without needing its sixty-second
wait, because once the verdict is settled at exit there is nothing left to wait for.

**Why it is not a delta:** REQ-BLK-003 already says the right thing. The code does not do it.

**Why it goes first:** that same test launches `sh -c "sleep 5"` against a five-second window,
so whether the timer fires before the process exits is a race machine load decides. It passes
in isolation (≈ 6 s) and under its own package (≈ 26 s); it failed after 60,52 s inside a full
`task ci` on 2026-09-21. Until this lands, a green gate proves nothing and a red one is
ambiguous.

**Evidence it is real, and older than the current branch** — two panes launched with
`sh -c "sleep 1"` against a real daemon, its own database twenty-five seconds later:

```
ses_01M33B3GKWBH5MFVDTWT6A0SEW|exited|pending|0
ses_01M33B1WQ29Q82PV3BP3XCEBNF|exited|pending|0
```

Reproduced independently at `c7d7e18` in a detached worktree.

## 3 · `T-F0-20` — the CLI for the workspace tree (code, after item 1)

Full task in `specs/tasks/umbral-f0-tasks.md`. It is the one open criterion that code can
close. Do not start it before the delta is ratified: its requirements are in the PRD already,
but no code is written before a human approves it.

- `umb workspace create|list|focus|rename|close`, `umb tab create|list|focus|rename|close`,
  `umb pane split|list|get|focus|rename|close`, `umb layout export|apply` — each one call to
  the JSON-RPC method of the same name, no client-side model;
- positional addressing by `w<n>`, `w<n>:t<m>`, `w<n>:p<m>`;
- `--json` and the REQ-CLI-004 exit codes, like every other `umb` command;
- `umb layout apply --from <file|->`, so the round trip is a pipe.

**Done when:** the five tests named in the task's Done line are green **and**
`scripts/cli_roundtrip.sh` passes against a real daemon — which *is* exit criterion 4,
performed rather than argued.

## 4 · A week of the TUI as the main terminal (human)

`docs/qa/f0-tui.md` was walked once on 2026-09-20 with no step failing, and that is what
closed `T-F0-12`. One session is not one week, and the criterion is deliberately about
sustained use: raw mode, a real keyboard, a real font, and the regressions that only show up
on day four.

**Done when:** seven days of the TUI as the daily driver with no blocking regression, recorded
where the previous walkthrough was. Start it now and run it in parallel with items 1 to 3;
it is the longest pole and nothing else depends on it.

## Before calling F0 closed

- [ ] `task ci` green, on a tree where `T-F0-21` has landed — the full gate, not `go test`.
- [ ] `python3 tools/sdd_check.py` with no CRITICAL and no HIGH.
- [ ] `spec-guardian` run on the final diff.
- [x] `changes/2026-09-cli-workspace-surface/` archived (2026-09-26).
- [ ] All six criteria marked in `specs/plans/umbral-mvp-plan.md`, with what closed each.
- [ ] `AGENTS.md`'s known status rewritten to say the phase is closed — and *only* then, since
      it has claimed that once before while two criteria were open.
- [ ] A closing checkpoint in `docs/checkpoints/`, verified against the filesystem and a
      running daemon rather than against these notes.
- [ ] This file deleted.

## The measurement, kept

Criterion 6 had never been measured before 2026-09-21, so the number is recorded here rather
than left in a transcript. An isolated `umbrald` — every `XDG_*` directory redirected — with
one workspace and four splits, left idle:

```
 10s  VmRSS=38524 kB  PSS=36885 kB  threads=13
 …    (unchanged through 80s)
 90s  VmRSS=42820 kB  PSS=41181 kB  threads=18   ← a sixth session was added here, not drift
```

Five live sessions confirmed two ways: five `zsh` children of the daemon, and
`select count(*), state, integration from sessions` answering `5|alive|osc133`. **37,6 MiB
RSS against a budget of 80.** The figure is the daemon's own; the shells are separate
processes, which is what "daemon memory" means. Taken on the developer's machine, not the
reference hardware of PRD §7 — evidence, not a gate, and the headroom is far larger than that
difference.
