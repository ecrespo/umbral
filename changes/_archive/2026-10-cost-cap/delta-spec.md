# Delta — enforcing `max_cost_usd_per_thread`

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-10-04` — approved in advance by E. Crespo, to be reviewed afterwards; amended after a spec-guardian review. Folded into PRD 1.20, API 1.31, Tech Design 1.40 and the F1 tasks file |
| **Date** | 2026-10-04 |
| **Task** | none; asked for by the Tech Lead on 2026-10-04, after T-F1-22's delta, decision 12 |
| **Raised by** | `router.max_cost_usd_per_thread` was parsed and converted to micro-USD (Tech §5.1), and nothing enforced it |

## Evidence

- **Tech §5.1** documents `max_cost_usd_per_thread` and its conversion "to micro-USD at load
  (Art. 6)". No module read the value.
- **API §3** describes `BUDGET_EXCEEDED` as "Thread token/cost budget exhausted". Only the token
  budget raised it.
- **REQ-AGT-008** stops a turn at `max_steps` "or the thread's token budget". It says nothing
  about cost.
- **`examples/models.toml`** ships `max_cost_usd_per_thread = 1.5`, so a user reading it would
  expect a cap.

## Decisions

1. **The cap is a budget like the token one, under REQ-AGT-008.**
   - **Mid-turn.** Before each model call, a turn whose thread has spent the cap stops with
     `stop_reason = budget`, the spend being the thread's `cost_micro_usd` plus this turn's cost
     so far. That cost includes the compaction calls.
   - **At send.** `thread.send` to a thread whose `cost_micro_usd` has reached the cap answers
     `BUDGET_EXCEEDED`, and persists nothing.
   - The check sits in `Store.BeginTurn`'s IMMEDIATE transaction, beside the token budget. A turn
     that finishes while the send is in flight is therefore seen.
   - Both budgets answer in the same place, after the provider check. A thread over either one
     whose provider is down answers `PROVIDER_UNAVAILABLE`, as the token budget already did.
2. **What "reached" means.**
   - The check runs before every priced call, never during one: before each step, and again
     between a step's compaction and its main call.
   - The call that crosses the cap finishes, is recorded and paid for, and the turn stops before
     the next call. A thread can therefore end slightly above the cap; the overshoot is at most
     one call. A provider's stream cannot be priced until it ends, which is why.
   - "Reached" is `>=`: two calls of 6 against a cap of 12 stop there.
3. **No cap.**
   - An absent `max_cost_usd_per_thread`, or 0, is no cap. Local models cost 0 and never reach
     one.
   - A positive cap that rounds to 0 micro-USD (under 0.0000005 USD) would silently mean no
     cap, so it is a malformed file.
4. **Live.**
   - The runtime reads the cap at every check (`agents.Config.MaxCostMicroUSD`, a func), so
     `config.reload` applies a new cap to the next call.
   - API §5.28 today says a reload applies "the providers and `[secrets] allow_env`; every other
     key still needs a restart". This delta adds `models.toml`'s `[router]` table to what a
     reload applies.
   - The policy and `offline` already travel with the provider catalog the reload rebuilds; the
     cap now does too.
5. **Where it lives.**
   - `cmd/umbrald` passes `providerConfig.MaxCostMicroUSD`, which is read under the
     configuration's lock.
   - The domain error's text becomes "the thread's budget is spent", since it now covers both
     budgets. The wire code and `stop_reason` are unchanged.
6. **The tests.**
   - `TestACostCapStopsTheTurn_REQ_AGT_008` and `TestASpentCostCapRefusesTheSend_REQ_AGT_008`
     run in `internal/agents`.
   - `TestTheCostCapFollowsModelsToml_REQ_AGT_008` checks the value from models.toml, and that a
     reload changes it.
   - `TestTheDaemonEnforcesTheCostCap_REQ_AGT_008` runs a real daemon whose thread has spent a
     1 µUSD cap: `BUDGET_EXCEEDED`, and no model call.
   - `TestTheCostCapCountsEarlierTurns_REQ_AGT_008` covers the thread's earlier spend.
   - `TestACompactionThatReachesTheCapStopsTheTurn_REQ_AGT_008` covers a priced summary.
   - The threadstore test checks the cap inside `BeginTurn`.
   - The malformed-file test checks a cap under one micro-USD.
   - Sixteen mutations were made, all killed. That includes the three a spec-guardian review
     found surviving: earlier spend ignored, `>` for `>=`, and the summary's cost dropped.

## What changes in the specs on ratification

- **PRD REQ-AGT-008:** "…`max_steps` (50 by default), the thread's token budget, or the cost cap
  `router.max_cost_usd_per_thread` where one is set, THEN THE SYSTEM SHALL stop it with
  `stop_reason = max_steps` or `budget`." Add decisions 1–3 as a sentence.
- **API §5.20:** `BUDGET_EXCEEDED` names both budgets.
- **API §5.28:** a reload also applies `models.toml`'s `[router]` table.
- **Tech §5.1:**
  - the `max_cost_usd_per_thread` line says what enforces it (decisions 1–4);
  - the reload paragraph names the `[router]` table.
- **`specs/tasks/umbral-f1-tasks.md`:**
  - an execution-log row;
  - the REQ-AGT-008 matrix row gains the four tests.
