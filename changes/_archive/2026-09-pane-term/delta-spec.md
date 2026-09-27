# Delta — a pane says which terminal it is

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-09-26 — commissioned by E. Crespo ("reparar el CI completo"); drafted, applied and archived by the implementing session` |
| **Date** | 2026-09-26 |
| **Task** | T-F0-25 |
| **Raised by** | The GitHub CI run of `develop` at `cfed2b6`, 2026-09-26, and every run since `eeab3fb` |

## Evidence

**Every pane inherits the daemon's terminal type.** `internal/sessions/lifecycle.go`'s
`environ` copies `os.Environ()` into the child and adds nothing about the terminal. The daemon
is not attached to a terminal — autostarted detached, or started by `systemd --user`, `launchd`
or a CI runner — so it usually has no `TERM` at all, and when it was started inside tmux it has
`TERM=screen`. Either way a pane describes a terminal that is not the one rendering it.

**Measured.** On the GitHub runners, where `TERM` is unset, bash ran readline as a dumb
terminal: the line scrolled horizontally (`<cho umbral-once-2d54`) and the first byte typed at
a fresh prompt was lost — `tput cols` arrived as `put cols`, `echo` as `cho`. Four tests failed
on every run on both Linux and macOS: `TestAPendingCommandIsShownAndNotRun_REQ_TERM_011`,
`TestPendingTextIsDeliveredOnce_REQ_TERM_011`,
`TestPendingTextIsDeliveredWithoutShellIntegration_REQ_TERM_011` and
`TestResizeNotifies_REQ_TERM_007`. Reproduced locally with `env -u TERM go test`: the same four,
the same screens. The code was right about everything except what it told the shell.

**No requirement says it.** REQ-INT-001 lists the `UMBRAL_*` variables a pane receives; Tech
§5.2b repeats them. Neither says what `TERM` a pane has, so the defect was a gap in the
specification, not only in the code.

## Decisions

**1. `TERM=xterm-256color` and `COLORTERM=truecolor`, in every pane.** The emulator is
xterm-compatible (REQ-TERM-002's conformance suite), and `xterm-256color`'s terminfo entry is
installed on every supported platform, which a Ghostty-specific name is not. `COLORTERM` states
the truecolor the same suite requires.

**2. Over the daemon's own values, under the pane's declared ones.** The daemon's terminal is
never the pane's, so it is always replaced. A pane or layout that declares `TERM` in its `env`
(REQ-WS-005) asked for it explicitly, and keeps it.

## Specification changes

- **PRD §6.1**, verbatim as applied (PRD 1.12):

  > **REQ-TERM-013** · MUST · ubiquitous — THE SYSTEM SHALL set `TERM=xterm-256color` and `COLORTERM=truecolor` in the environment of every process it launches in a pane, replacing the values the daemon itself inherited, so that a program in a pane addresses the emulator that renders it rather than whatever terminal — or none — the daemon was started from; a value the pane's own `env` declares (REQ-WS-005) SHALL take precedence.

- **Tech §5.2b** gains the two variables and the precedence. Tech 1.13.

## Verification

- `TestAPaneAdvertisesTheTerminalThatRendersIt_REQ_TERM_013` — the daemon's process has
  `TERM=screen` and no `COLORTERM`; a pane prints `xterm-256color`/`truecolor`, and a pane
  declaring `TERM=vt100` prints `vt100`.
- `go test ./internal/sessions/integration/` passes with `TERM` unset, as on a CI runner.

## Phase

**F0**, as `T-F0-25`: it is the terminal core, and the CI gate cannot be green without it.
