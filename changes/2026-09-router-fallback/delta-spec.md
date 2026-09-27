# Delta — how the router falls back, and what `usage` records

| Field | Value |
|---|---|
| **Status** | `PROPOSED 2026-09-27 — pending the Tech Lead's ratification. T-F1-07 implements it and is merged with it open, at the user's instruction to carry on through T-F1-10.` |
| **Date** | 2026-09-27 |
| **Task** | T-F1-07 |
| **Raised by** | The `spec-guardian` review of T-F1-07, 2026-09-27 (findings 3 and 4) |

## Evidence

REQ-LLM-003: "IF a provider replies 429 or 5xx, or does not deliver the first token within
`first_token_timeout` (30 s remote, 120 s local), THEN THE SYSTEM SHALL try the next candidate
of the task class and record the failure in `usage`." REQ-LLM-005: "record, for every model
call, the input tokens, output tokens, time to first token and cost in micro-USD." DD-004 lists
the filters and their order, and nothing more.

A router has to answer questions these sentences leave open:

1. What happens to a failure **after** the first token, when the caller already holds part of
   the answer?
2. Is a transport failure (connection refused, reset) or a stream that ends without an event a
   fallback, although neither is a 429 or a 5xx?
3. Does any other 4xx (400, 401, 404) fall back?
4. Is a call an adapter refuses before sending — it cannot carry a response schema or a
   reasoning setting — a "model call" that `usage` must record?
5. How is "the request does not fit the window" judged before a provider has counted it?
6. What if the `usage` row cannot be written? `egress_log` fails closed (DD-008, T-F1-05);
   REQ-LLM-005 is a MUST ubiquitous, and Analyze C-01 is about persist-before-notify for turns,
   not about this table.
7. When does the first-token clock start?

## Decisions

1. **Fallback is decided before the first event reaches the caller.** After one is delivered,
   a failure ends the call and is returned: restarting elsewhere would duplicate text the
   caller already rendered.
2. **A transport failure and an empty stream fall back**, like a 5xx: neither says anything
   about the request, and the next candidate may well answer.
3. **Any other 4xx ends the call**, and so does a request the adapter could not build (tool-call
   arguments that are not JSON): they would fail the same way on every candidate.
4. **A refusal before sending is not a call.** An adapter that cannot carry what the request
   asks for returns `ErrUnsupported` and the router moves on without a `usage` row, since
   nothing reached a provider. The capability filter already drops models that do not
   advertise those features; this covers adapters that do not carry them yet.
5. **The window filter estimates** four characters a token over the system prompt, messages and
   tool-call arguments, plus `max_output_tokens`. It is only a filter: what a call consumed is
   the provider's own count.
6. **A `usage` row that cannot be written is logged and does not fail the call** — fail-open,
   unlike `egress_log`. The row is an account of a call that already happened; refusing to
   return the answer would not bring the row back, while an unlogged egress is a request that
   has not happened yet and can still be stopped. The loss must be visible: a later task with
   REQ-OBS metrics should count it. The Tech Lead may prefer to fold this into C-01's
   resolution instead.
7. **The first-token clock starts before the request is sent**, so a server that never sends
   its response headers (an Ollama loading a model) times out like one that sends them and
   then nothing. A timed-out candidate is recorded `timeout`.

## Impact

- Tech Design DD-004 ("How the router walks them", 1.20) states decisions 1–7.
- No PRD text changes if ratified as is; REQ-LLM-003 could gain "before the first token reaches
  the caller" and REQ-LLM-005 "made to a provider" to say it outright.
- No API or Data Model change: `usage.status` already has `timeout`, `rate_limited` and `error`.
