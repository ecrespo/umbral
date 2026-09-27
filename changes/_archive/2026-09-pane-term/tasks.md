# Tasks — delta `2026-09-pane-term`

### [x] 2026-09-26 T-F0-25 · A pane says which terminal it is
- **What:** `environ` appends `TERM=xterm-256color` and `COLORTERM=truecolor` after the daemon's
  environment and before the pane's declared `env`; PRD gains REQ-TERM-013 (1.12) and Tech §5.2b
  the two variables (1.13).
- **REQ:** REQ-TERM-013
- **Files:** `internal/sessions/lifecycle.go`, `internal/sessions/integration/term_test.go`,
  `specs/prd/umbral-mvp.md`, `specs/technical/umbral-architecture.md`
- **Done:** `TestAPaneAdvertisesTheTerminalThatRendersIt_REQ_TERM_013` green, and the
  integration package green with `TERM` unset.
