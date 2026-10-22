# Delta — policy router: capabilities, circuit breakers, policies, `model.route`

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-22 by the implementing agent under the Tech Lead's standing F2 instruction; folded into API 1.43 (§2, §5.26, §5.40) and Tech 1.58 (DD-004, §5.1)` |
| **Date** | 2026-10-22 |
| **Task** | T-F2-11 |
| **REQ** | REQ-RTR-001, REQ-RTR-002, REQ-RTR-003 |

## Decisions the PRD and the task file left open

1. **What each policy orders by.** The catalog has locality, prices and capabilities, and no
   measure of quality.
   - `local-first`: local candidates before remote ones.
   - `cost`: input plus output price per million tokens, ascending. A local model costs
     nothing. A remote model whose provider publishes no price comes after every priced one:
     an unknown cost is not a free one.
   - `quality`: the declared order. A class lists its candidates best first (DD-004).
   - Ties keep the declared order, and a pinned model (a thread's chosen model) is never
     reordered.
   - A thread pinned to a model has that one candidate. While its provider's breaker is open,
     its calls are `PROVIDER_UNAVAILABLE` with nothing sent, until a probe succeeds; F1 would
     have sent each one and let it fail.
   - `local-first` is the default, so a class that lists a remote model before a local one now
     tries the local one first. F1 always walked the declared order whatever the policy said.
2. **Where the filters sit.** In this order: in the catalog, not `down` (offline included,
   REQ-LLM-004), then capabilities, then breakers, then the policy. `model.route` reports the
   first filter that dropped a candidate, and lists the dropped ones in declared order after
   the kept ones. REQ-RTR-003 also names REQ-SEC-006 as a hard constraint, but REQ-SEC-006
   requires approvals for tools under taint and constrains no candidate, so routing has no
   filter for it.
3. **Vision.** `domain.Request` gains `Vision`, set by a caller whose request carries an image.
   No adapter sends images yet; the filter and `model.route` read it.
4. **What a breaker counts.**
   - A failure is what a call falls back on: a 429, a 5xx, a transport failure, a stream that
     ends empty, or no first token in time.
   - Counting nothing: a 4xx (the request's fault), an adapter refusing an unsupported request,
     a cancel.
   - A success is a first event delivered: a later failure is not the provider being down. It is
     reported to the breaker at that moment, not when the answer ends, so a probe frees its slot
     and a success resets the count without waiting for a long generation.
   - While a breaker is open only its probe counts. A call that started before it opened, and
     ends after, says nothing about recovery: neither its success nor its failure moves it.
   - Every opening and closing is logged with the provider, so a user can see why a provider is
     skipped.
   - "Consecutive calls" counts model calls, one per `usage` row. So one routed call that walks
     three models of one provider can open its breaker by itself.
5. **The probe.** After `breaker_cooldown` the next real call to the provider is the probe; no
   synthetic request is sent. One probe at a time: while it is in flight the provider reads
   `breaker_open` and other calls skip it. A probe that says nothing (a 4xx, a cancel) frees the
   slot for the next call.
6. **Keys.** `breaker_failures` (1–100, default 3) and `breaker_cooldown` (whole seconds,
   1–3600, default 60) live in `models.toml`'s `[router]` table beside `policy`. They are
   applied at start and on `config.reload`. The breakers' state lives in memory, survives a
   reload and not a restart.
7. **`model.route`** (API §5.40):
   - It takes a class or a pinned model and the request's shape (`tools`, `vision`,
     `json_schema`, `reasoning`, `input_tokens`).
   - It answers every candidate: the kept ones ranked in call order, then the dropped ones in
     declared order, each with its reason, locality, health, prices, window and declared index.
   - It sends nothing and takes no probe.
   - It echoes the `class`, or the `model` when one is pinned. A `not_in_catalog` candidate has
     an empty provider and health, and zero prices and window.
   - A class `models.toml` does not declare answers no candidates.
   - Every client kind may call it, `umb` included; there is no `umb` command for it yet.

## Folded into

- API 1.43: §2 (`model.route` for `cli`), §5.26 (pointer) and §5.40 (`model.route`).
- Tech 1.58: DD-004 (vision, breakers, policies) and §5.1 (`breaker_failures`, `breaker_cooldown`).
