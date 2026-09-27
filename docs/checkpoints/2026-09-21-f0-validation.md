# CHECKPOINT — Can phase F0 be closed? (measured against a running daemon)

> 2026-09-21 · scope: the six F0 exit criteria and the state of the branch
> `feat/T-F0-19-bootstrap-sweeper` · verified by running, not by reading the notes.

## Verdict

**No. F0 cannot be closed.** `T-F0-19` shipped, and one criterion that had never been
measured turned out to be met with room to spare — but two criteria remain open, one of them
a feature nobody has built yet, and the validation found a **MUST that the code does not
keep**.

| # | Criterion | 2026-09-20 | 2026-09-21 |
|---|---|---|---|
| 1 | VT conformance 100 % MUST | ☑ | ☑ |
| 2 | Correct blocks in bash, zsh and fish in CI | ☑ | ☑ |
| 3 | The TUI used for one week as the main terminal | ☐ | ☐ **open** — a human's to close; one walkthrough is not one week |
| 4 | A script drives the workspace tree using only the CLI | ☐ | ☐ **open** — `T-F0-20`, blocked on a delta awaiting ratification |
| 5 | The structure comes back after `kill` | ☑ | ☑ |
| 6 | Benchmarks within the NFRs | ◐ never measured | ☑ **met** — see below |

Plus, new on 2026-09-21: **`T-F0-21`**, a REQ-BLK-003 defect that predates this branch.

## Criterion 6, measured for the first time

PRD §7: *idle daemon memory < 80 MiB with 5 sessions and no embedded model loaded.* It has no
REQ id, so `task perf` does not gate it, and nothing in the repo had ever put a number to it.

An `umbrald` was started with every `XDG_*` directory redirected to a throwaway tree, given a
workspace and four splits, and left alone:

```
 10s  VmRSS=38524 kB  PSS=36885 kB  threads=13
 …    (unchanged)
 80s  VmRSS=38524 kB  PSS=36885 kB  threads=13
```

`VmRSS 37.6 MiB`, `PSS 36.0 MiB`, flat across 90 s. Five live sessions confirmed two ways —
five `zsh` children of the daemon, and `select count(*), state, integration from sessions`
answering `5|alive|osc133`. The number is the daemon's own; the shells are separate
processes, which is what "daemon memory" means.

**47 % of the budget, with the other 53 % unused.** Measured on the developer's machine and
not on the reference hardware of PRD §7, so this is evidence rather than a gate — but the
headroom is far larger than that difference.

## The defect: REQ-BLK-003 has no verdict for a short-lived session

REQ-BLK-003 (PRD:157): *IF a session emits no shell-integration sequences within the first
5 s, THEN THE SYSTEM SHALL mark it `integration: none`.*

`internal/sessions/blocks.go:120` is the only thing in the daemon that ever writes `none`:

```go
live.integrationTimer = time.AfterFunc(domain.IntegrationWindow, func() {
    s.setIntegration(live, domain.IntegrationNone)
})
```

and `internal/sessions/lifecycle.go:246` cancels it when the shell exits:

```go
s.abandonOpenBlock(live)
if live.integrationTimer != nil {
    live.integrationTimer.Stop()
}
```

A session whose process exits before the window closes therefore never gets a verdict. It is
not a transient state either: `finish` deliberately leaves the session in the live map so
`session.list` and a late `session.get` still find it, so the daemon goes on reporting
`pending` about a process that is gone, and the row says the same.

**Measured, not argued.** Two panes launched with `sh -c "sleep 1"` against a real daemon;
twenty-five seconds later — five times the window — its own database:

```
ses_01M33B3GKWBH5MFVDTWT6A0SEW|exited|pending|0
ses_01M33B1WQ29Q82PV3BP3XCEBNF|exited|pending|0
```

Reproduced independently at `c7d7e18` in a detached worktree, so it is **older than this
branch**; `T-F0-19` only added enough build and daemon load to make the existing test notice.

### It is also why the gate is unreliable

`TestACommandPaneGetsNoShellIntegration_REQ_BLK_003` launches `sh -c "sleep 5"` against a
five-second window. Whether the timer fires before the process exits is a race that machine
load decides. It passes in isolation (≈ 6 s) and under its own package (≈ 26 s); it failed
after 60.52 s inside a full `task ci` on 2026-09-21. The test is not flaky about nothing — it
is a coin flip over a real defect, and only fixing the defect can make it deterministic.

Recorded as `T-F0-21`, on its own because `AGENTS.md` is one task per branch and this one is
not `T-F0-19`'s. No delta: REQ-BLK-003 already says the right thing; the code does not do it.

## State of the branch

`feat/T-F0-19-bootstrap-sweeper`, **nothing committed**. `T-F0-19` is implemented and its
specs are updated; `spec-guardian` returned FIX FIRST with 13 findings and all 13 are
addressed. The one thing between it and a commit is a `task ci` that cannot be trusted while
`T-F0-21` is open.

## What is left for F0

| Item | Owner | Blocked on |
|---|---|---|
| Ratify `changes/2026-09-cli-workspace-surface/` | human | — |
| `T-F0-20` — `umb workspace`/`tab`/`pane`/`layout` (criterion 4) | code | the ratification above |
| `T-F0-21` — REQ-BLK-003's verdict at exit | code | — |
| Criterion 3 — a week of the TUI as the main terminal | human | — |

Two of the four are a human's, and neither can be shortened by writing more code.
