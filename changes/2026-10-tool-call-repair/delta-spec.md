# Delta — what an invalid tool call is, what "retry once" means, and where its count goes

| Field | Value |
|---|---|
| **Status** | `PROPOSED` |
| **Date** | 2026-10-01 |
| **Task** | T-F1-15 |
| **Raised by** | `spec-guardian`'s review of T-F1-15 (verdict "needs a delta", findings 1–4) |

## Evidence

- **REQ-AGT-006:** "IF the **arguments** of a tool call do not validate against its schema, THEN
  THE SYSTEM SHALL retry once with a repair message and, if it fails again, end the turn with
  `stop_reason = tool_error`." It says nothing of a call to a tool that does not exist, which has
  no schema, nor of input that is not JSON. It does not say whether "once" is per turn or per
  invalid call. It also leaves open what happens to the other calls of the step that fails again.
- **T-F1-13's runtime** replaced input that was not JSON with `{}` and **ran** it, and a tool with
  no required field accepted it.
- **REQ-OBS-002:** "THE SYSTEM SHALL **expose** the metric `umbral_tool_calls_invalid_total`
  labeled by model." Tech §7.2 lists it with no note. T-F1-18, the task that brings OpenTelemetry,
  lists REQ-OBS-001/003/004 and not 002. The `umbral_frames_refused_total` row of §7.2 already
  shows the pattern for a count that waits for OTel.
- **Tech §5.1** lists an `obs` module, but **§5.2** has no `obs` row, so `.go-arch-lint.yml` had
  no rule to encode.

## Decisions

1. **What is an invalid tool call.** A call is invalid when its tool does not exist, when its
   input is not JSON, or when its input fails the tool's schema. Each is recorded `invalid_args`,
   repaired the same way and counted. Input that is empty or only whitespace is the empty object
   `{}`, as the registry reads it. Input that is not JSON is never run. Its row stores `{}` so
   that `arguments_json` stays JSON. An error that is not the model's fault, such as an
   environment the daemon built wrong (`ErrInvalidEnv`), is a tool `error`: it gets no repair,
   is not counted, and cannot end the turn with `tool_error`.
2. **"Retry once" is per invalid call, not per turn.** The repair message names the tool and
   what failed, and asks for a call that matches the schema. The model's next step is the one
   retry. An invalid call in that step ends the turn with `tool_error`, whichever tool it names.
   A step whose calls are all valid spends the retry, so a later invalid call in the same turn
   gets its own repair. A model that keeps alternating invalid and valid calls is bounded by
   `max_steps` (REQ-AGT-008).
3. **The step that fails again stops at its invalid call.** The calls before it in that step have
   already run and keep their results. The invalid call is recorded `invalid_args`. The calls
   after it are not run and get no `tool_calls` row, because the turn has ended. They therefore
   do not appear in the history a later turn reads. No new status is added to the CHECK.
4. **The metric's label** is the catalog id of the model that made the call, as the router reports
   it in the step's usage. When there is none, it is the thread's `model`, and failing that,
   `unknown`.
5. **Where the count goes.** Until OpenTelemetry arrives, the counter lives in memory in
   `internal/obs`. T-F1-18 exports it with the rest of §7.2, so REQ-OBS-002 joins T-F1-18's REQ
   line and the traceability matrix. §7.2's row gets the same interim note as the frames row.
   *Alternative, not recommended:* expose it now under `system.status`. That would be an API
   change for a count that T-F1-18 moves to OTel a few tasks later.
6. **`obs` in Tech §5.2:** "`obs`: standard library, and OpenTelemetry from T-F1-18; imported only
   by `cmd/*`; modules count through a port of their own, which `cmd/umbrald` gives the `obs`
   implementation." `.go-arch-lint.yml` encodes this row. Its one `bus` allowance stays unused,
   because go-arch-lint requires at least one.

## What changes in the specs on ratification

- **PRD §6 REQ-AGT-006:** append "An invalid call is one whose tool does not exist, whose input
  is not JSON, or whose input fails the schema; the retry is the model's next step, one per
  invalid call (delta `2026-10-tool-call-repair`)."
- **Tech §3.3, error flow item 2:** decisions 1–3 in two sentences.
- **Tech §5.2:** the `obs` row of decision 6.
- **Tech §7.2:** the `umbral_tool_calls_invalid_total` row says "labelled by the model the router
  reports, else the thread's, else `unknown`; counted in memory, exported when OTel lands
  (T-F1-18)".
- **`specs/tasks/umbral-f1-tasks.md`:** REQ-OBS-002 is added to T-F1-18's REQ line and Done
  criteria, and to the matrix row (T-F1-15 counts it, T-F1-18 exports it).
