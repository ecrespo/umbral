# Delta — how the context budget counts tokens and compacts

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-09-27 — approved by E. Crespo together with the other three F1 deltas of T-F1-11…T-F1-14, as written, with the optional REQ sharpenings folded. Folded into PRD 1.15, API 1.21, Tech Design 1.29 and Data Model 1.13` |
| **Date** | 2026-09-27 |
| **Task** | T-F1-12 |
| **Raised by** | T-F1-12, answering Tech Design Q-03 |

## Evidence

REQ-CTX-004: "IF the estimated context exceeds the model window minus the response reserve, THEN
THE SYSTEM SHALL compact the history with a summary before sending and record the
`context.compacted` event." Tech §3's module table gives `context` a "tiktoken tokenizer in Go",
and Q-03 asks whether "tiktoken for OpenAI/gpt-oss, ×1.1 approximation for the rest" is enough.
Neither says what the response reserve is, which messages the summary replaces, where the
summary goes, or what happens when even the newest message does not fit.

Measured against the constraints:

- A tiktoken encoding loads a BPE table of 100k–200k entries, tens of MiB of heap, into a daemon
  whose idle budget is 80 MiB (PRD §7; measured 37.6 MiB). It covers one family; Qwen, Llama and
  Gemma, the local defaults, would still be approximated.
- The router already estimates at four characters a token for its window filter (DD-004).

## Decisions

1. **Q-03 answered: no tokenizer is loaded.** Every family is estimated at one token per three
   bytes of UTF-8, rounded up, plus four tokens per message. That over-counts English (≈4 bytes a
   token) and code (3–4), and matches CJK (3 bytes, ≈1 token): the error compacts a little early
   instead of overflowing a window. Everything sent counts: tool names, descriptions and input
   schemas, tool-call ids and a response schema. The router's own window filter stays at four
   characters a token (DD-004) on purpose: it is a rough pre-filter, and being looser than this
   estimate means a request compacted to fit passes it for the same model.
2. **The response reserve** is the call's `max_output_tokens` when set, otherwise a quarter of
   the window up to 8192, and never more than half the window; when the cap applies, the request's
   `max_output_tokens` is lowered to it, so the request and its answer fit the window together.
3. **Which window.** The runtime (T-F1-13) budgets against the smallest known window among the
   thread's candidates — its model, or its class's candidates — so a fallback to a smaller model
   is compacted for rather than skipped by the router's window filter. A model with no known
   window (`context_window = 0`, API §4) does not constrain it; with none known, nothing is
   compacted.
4. **What is kept.** The newest messages that fit, in order. The kept history starts with a user
   message: the user's latest request is kept verbatim even when it lies in the summarized part —
   mid-turn the newest messages are tool calls and results — and a tool result is never kept
   without its call.
5. **Where the summary lives.** In the system prompt, under a header saying the conversation was
   compacted, so no role sequence changes. It may take a fifth of the available budget and is cut
   to it, saying so. The runtime persists it as a `system_note` message (DD-007) and, within the turn,
   sends the base prompt, that summary and the messages after it; each turn starts from the whole
   history (delta `2026-09-agent-runtime`, decision 6). **A later compaction replaces
   the summary**: the previous one is summarized again with the newly old messages, so the prompt
   holds one summary however long the thread.
6. **The summary is made by the `fast` class** on the previous summary (at most half of the input)
   and a transcript of the summarized messages, clipped together (keeping the most recent part,
   with a note) to the fast model's own available budget minus the summarizing instructions, which
   the runtime sets.
7. **Nothing to compact is an error.** When what must stay — system prompt, tools and the user's
   latest request — does not fit, the turn fails with `ErrContextOverflow` rather than sending a
   request the provider will refuse; no model is called. A summary that cannot be made is
   `ErrContextOverflow` too, carrying its cause; a turn cancelled meanwhile is a cancel. Mid-turn,
   when only the user's request fits beside what must stay, everything after it is summarized,
   including the newest tool result: the model then continues from the summary, and may repeat a
   call.
8. **The event.** T-F1-12 returns the `context.compacted` payload (`before_tokens`,
   `after_tokens`); T-F1-13 persists the summary and then publishes the notification, tested by
   `TestCompactedEventPersistedThenPublished_REQ_CTX_004`.

## Impact

- Tech §3's module table: `context` uses "a byte estimate (Q-03)" instead of "tiktoken"; Q-03
  closes with decision 1; §5.3c gains the budget.
- No PRD, API or Data Model change: `context.compacted` keeps `{thread_id, before_tokens,
  after_tokens}`, which the estimate fills.
