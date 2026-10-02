# Tasks — delta `2026-10-tool-call-repair`

### [x] 2026-10-01 T-F1-15 · Repair of invalid tool calls
- **What:** decisions 1–4 in `internal/agents` (`repair.go`, `turn.go`). The counter of
  decision 5 is in `internal/obs`, and the arch rule of decision 6 is in `.go-arch-lint.yml`.
- **REQ:** REQ-AGT-006, REQ-OBS-002

### [ ] Ratification
- The Tech Lead ratifies or amends the delta, folds it into the PRD, the Tech Design and the F1
  tasks file, and moves it to `changes/_archive/`.

### [ ] T-F1-18 · OTel observability (existing task, widened)
- **What:** also exports `umbral_tool_calls_invalid_total{model}` from `obs.Metrics`.
- **REQ:** REQ-OBS-002, in addition to the task's own.
