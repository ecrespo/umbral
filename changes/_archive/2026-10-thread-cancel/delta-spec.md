# Delta — what `thread.cancel` takes, when it answers, and the state it leaves

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-10-01 — approved by E. Crespo as written, with decision 5's proposed option (only a cancel leads to `stopped`). Folded into API 1.24, Tech Design 1.32, Data Model 1.15 and the F1 tasks file` |
| **Date** | 2026-10-01 |
| **Task** | T-F1-16 |
| **Raised by** | T-F1-16: API §5.21 had a heading and a result, and no body |

## Evidence

- **API §5.21** read only "`thread.cancel` — REQ-AGT-007 → `{stopped_at}`". It gave no
  parameters and no errors, and did not say what a cancel of a thread with no turn running does.
  `task schema` refuses a served method whose parameters §5 does not document.
- **API §7** has `running --> stopped: cancel, max_steps, budget, tool_error` and
  `awaiting_approval --> stopped: thread.cancel`. T-F1-13's runtime ends **every** turn `idle`
  and left the cancel case to this task. The other three reasons still end `idle`, which
  diverges from the diagram.
- **REQ-AGT-007:** "stop the turn and terminate the processes it launched in under 500 ms". The
  kill itself is T-F1-10's (Tech §5.3b).

## Decisions

1. **Shape.** Params `{thread_id}`. Result `{stopped_at: int | null}`. Errors `NOT_FOUND` and
   `VALIDATION_ERROR`. §2's `cli` row already lists `thread.cancel`, and the method is served to
   it.
2. **It answers once the turn has ended**, not once the cancel is requested. `stopped_at` is
   the moment the turn's end was written (the thread's `updated_at`), so a `thread.get` right
   after the answer reads `stopped`. 500 ms is a budget, not a deadline: the answer waits for
   the turn, bounded only by the request. A tool that ignored its context would hold the
   answer.
3. **A cancelled turn leaves the thread `stopped`**, with `attention_state = idle` and
   `stop_reason = cancelled`. This holds whether the turn was cancelled by `thread.cancel` or by
   the daemon closing, and from `running` or from `awaiting_approval` (whose approval expires,
   T-F1-14).
4. **No turn running is not an error.** The thread is left as it is and `stopped_at` is `null`:
   the cancel lost the race with the turn's own end, and a script that cancels defensively
   should not have to tell the two apart through an error code. The precedent is §5.29's wait,
   which returns at once on a thread already in its target state; it departs from the
   `CONFLICT` of `session.input` on an exited session and of `approval.respond` on a decided
   approval. `null` is also the answer when the cancel finds the turn already recording another
   end, and when the turn's end could not be written (`storage_error`). `stopped_at` is set only
   for a turn this cancel stopped and whose end is recorded. The cancel takes `thread.send`'s
   per-thread lock, so a cancel issued after a send has answered always finds that turn.
5. **The other ends of API §7 (needs a choice).** *Proposed:* amend §7 so that only a cancel
   leads to `stopped`. `max_steps`, `budget`, `tool_error`, `provider_error`, `storage_error` and
   `context_overflow` end `idle`, as the runtime does today, with `attention_state = done` so the
   user sees that the turn ended. The `stop_reason` already says how it ended.
   *Alternative:* make the runtime follow §7 as written. Those turns would then end `stopped`
   with `attention_state = idle`, and `done` would be lost for them.

6. **Recovery agrees with a clean stop.** Data Model §6 step 3 also sets `attention_state = idle`
   on the threads it stops, so after a `kill -9` a stopped thread no longer reads as `working`
   or `blocked`. A shutdown that cancels the turn already leaves `stopped`/`idle` (decision 3).
   The recovery code changes in this task.
7. **Left to T-F1-23 (`thread.wait`).** A pinned turn can now end `stopped`, so §5.29 must say
   what a wait whose targets do not include `stopped` does then: return with
   `final_state = stopped`, or wait until it times out. This delta does not decide it.

## What changes in the specs on ratification

- **API §5.21:** written by T-F1-16 already (API 1.23); decisions 1–4.
- **API §7:** the first diagram, per decision 5.
- **Tech §3.3, error flow item 3:** "`thread.cancel` returns once the turn has ended, leaving the
  thread `stopped`".
- **Data Model §6 step 3:** "→ `stopped`, with `attention_state = idle`" (decision 6).
- **`specs/tasks/umbral-f1-tasks.md`, T-F1-23:** a note carrying decision 7.
