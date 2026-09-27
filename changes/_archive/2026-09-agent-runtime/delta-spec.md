# Delta — how the agent runtime persists, stops and rebuilds a turn

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-09-27 — approved by E. Crespo together with the other three F1 deltas of T-F1-11…T-F1-14, as written, with the optional REQ sharpenings folded. Folded into PRD 1.15, API 1.21, Tech Design 1.29 and Data Model 1.13` |
| **Date** | 2026-09-27 |
| **Task** | T-F1-13 |
| **Raised by** | T-F1-13, closing Analyze C-01 (A-10) |

## Evidence

- **Analyze C-01:** "nothing says what happens if SQLite cannot write … while DD-007 requires
  persisting before notifying. The agent would have to invent it." Suggested: stop the turn with
  `stop_reason = storage_error` without running the tool, and surface it.
- **API §6** lists six stop reasons; a request that cannot fit its window even compacted
  (REQ-CTX-004, delta `2026-09-context-budget` decision 7) has none of them.
- **DD-007** says messages, tool calls and approvals are persisted before publishing; it says
  nothing of streamed text or reasoning, nor of what a tool result is stored as.
- **API §5.23** gave `thread.get` "`{thread_id, include_messages?: bool, limit, cursor}`" and no
  result; `thread.list` no parameters; §3 no prefix for `turn_id`.

## Decisions

1. **`storage_error` (C-01).** A write that fails stops the turn: nothing it would have recorded
   is published, no tool runs whose call is not recorded, and `thread.turn_finished` carries
   `stop_reason = storage_error`. The turn's end is still written, on a context the cancel cannot
   stop, so the thread takes the next message.
2. **`context_overflow`.** `ErrContextOverflow` ends the turn with this reason; no request is sent.
3. **Streamed text is persisted chunk by chunk.** The assistant message is inserted with the
   first text delta and each later delta is appended before it is published (≈0.2 ms a write,
   DD-007's cost). **Reasoning is published, not persisted**: it is not part of the history a
   model reads back.
4. **A tool result is `tool_calls.result_json = {"text", "tainted"}`**; the history a later turn
   reads is rebuilt from the messages and their tool calls, and a call a crash left `pending` is
   answered as interrupted. Once any tool result or message in a thread is tainted, the thread's
   turns are (REQ-SEC-006).
5. **The user message persisted is what the model read**: the text with its attachments rendered
   after it (REQ-AGT-011); `attachments_json` records what was attached.
6. **Compaction across turns.** Each turn starts from the whole stored history and compacts again
   if it must; within the turn the summary and kept messages carry from step to step. The
   `system_note` rows are the record of each compaction, not an input. This supersedes delta
   `2026-09-context-budget` decision 5's "from then on sends the base prompt, that summary and the
   messages after it", which would need a column saying where each summary starts.
7. **An `ask` with no approval flow is `denied_by_policy`**, so no tool runs outside a decision
   that allowed it until T-F1-14 wires approvals.
8. **Wire details.** `turn_id` is a `trn_` ULID (Art. 6). `thread.get` returns the Thread and,
   with `include_messages`, a page of messages oldest first with `next_cursor`; `thread.list`
   takes `{}`. `thread.send` with `wait` answers `NOT_IMPLEMENTED` until T-F1-23; inline
   `data_b64` attachments are refused until T-F1-19.
9. **The thread's model.** A thread's `model`, when set, is its only candidate; otherwise its
   class picks them (REQ-AGT-010). The router gains `Call.Model` and `Window`.

10. **`PROVIDER_UNAVAILABLE` at send** (API §5.20): a thread none of whose candidates is known to
    the catalog and not down is refused before anything is persisted. A candidate that fails
    once the turn runs ends it with `provider_error`, which is also the reason for any other
    failure that is neither storage, overflow nor a cancel (the catch-all). Discovery runs in the
    background at start, so a send in the first moments after it may be `PROVIDER_UNAVAILABLE`
    until the catalog knows the model; a client retries.
11. **The turn runs with the thread `BeginTurn` read** inside its transaction, so a mode change
    that lands after the send's own read cannot leave the turn deciding with a stale mode.
12. **`umb` reaches `thread.create` and `thread.send` only**, as §2 says; `get`, `list` and
    `update` are the interactive clients'.
13. **A thread's `model` is not checked against the catalog** at `create` or `update`; a model the
    catalog does not know is `PROVIDER_UNAVAILABLE` at the next send. `model: ""` clears it, so
    the class picks again.

## Impact

- API 1.19: §3 `trn_`, §4 Message's `turn_id`, §5.20, §5.23, §6's stop reasons.
- Tech 1.27: §5.3d describes the runtime; DD-007 points at decisions 1 and 3 (written).
- Data Model 1.12: §2.6 says what `result_json` holds.
- Analyze C-01 closes with decision 1 on ratification. No PRD text changes; REQ-AGT-011 could
  gain "and SHALL stop the turn when a write fails" if the Tech Lead wants it said outright.
