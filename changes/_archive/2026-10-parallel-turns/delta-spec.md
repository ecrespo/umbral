# Delta — parallel turns and per-provider queues

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-23 by the implementing agent under the Tech Lead's standing F2 instruction; folded into API 1.45 (§4 `Thread`, §5.20, §5.21, §7) and Tech 1.60 (DD-004, §5.1, §5.2, §5.3d)` |
| **Date** | 2026-10-23 |
| **Task** | T-F2-04 |
| **REQ** | REQ-ORC-001, REQ-ORC-002, REQ-ORC-005 |

## Decisions the PRD and the task file left open

1. **What waits is the turn's place, not the thread.** A send takes a place in an arrival-order
   queue before its transaction, so the store says `queued` from the moment `thread.send`
   answers and turns are granted in the order the runtime admitted their sends — after their
   attachments were read, not the order the requests reached the socket. A send that will be
   refused `CONFLICT` takes and returns a place inside its critical section, so a send to
   another thread in that instant may be written `queued` and granted at once; the flag is
   then cleared before the turn reads anything. The thread is
   written `running` and `working` as for any turn; `queued` is the only difference. Every guard
   that reads "a turn runs" from the store — a second send's `CONFLICT`, a mode change's, the
   `worktree.*` guard of delta `2026-10-worktree-methods` (decision 3) — therefore treats a held
   turn as running with no change of its own.
2. **A held turn reads nothing until it starts.** It waits for its place before it reads its
   rules, history, worktree or context, so a turn started late runs with the state of when it
   starts. Its trace span opens at the send and includes the wait.
3. **Cancelling a held turn** ends it `cancelled` with no model call and no tool run: the thread
   is `stopped`, `queued` is cleared by the end's own write, and the place leaves the queue. The
   answer's `stopped_at` is that end, as for a running turn. A daemon stop cancels held turns
   the same way; a crash leaves them to Data Model §6 step 3, which T-F2-01 wrote.
4. **No notification for a held turn's start.** Its first `thread.delta` or `thread.tool_call`
   shows it; `thread.get` and `thread.list` show `queued`. The attention state does not move —
   it is `working` throughout, as REQ-ORC-002 says — so `thread.attention_changed` is not sent.
   T-F2-06 may add one if the panel needs it.
5. **A held turn is not stall-checked** (REQ-AUT-008): waiting for a slot is not inactivity. Its
   stall clock starts when it takes its place, with `turn_started` as the last event.
6. **`[agents] max_concurrent_turns`** is a whole number from 1 to 64, 3 by default, read at
   start like `[waits]`. A value out of range stops the daemon naming the key.
7. **`max_parallel`** is a whole number from 1 to 64 in a `[[providers]]` entry, checked by the
   models schema. Absent, a local provider takes one call at a time and a remote one has no
   limit (REQ-ORC-005 asks a default for local providers only). It is applied at start and on
   `config.reload`; a lower limit lets the calls already sent finish. Each queue remembers its
   provider's locality, so a reload that names no `max_parallel` puts a local provider back to
   one call, never to no limit.
8. **What "queue time is counted separately" means.** The call waits for its provider's place
   first, and the first-token timer, `first_token_ms` and the usage row's latency all start after
   it: a call behind another thread's is never timed out for that wait. The wait is logged as
   `queued_ms` at debug level; no column or metric is added, since no REQ asks to report it. A
   call given up in the queue was never sent and records no `usage` row.
9. **The breaker is consulted before the queue.** A half-open provider's one probe may wait in
   its queue holding the probe; the alternative, consulting the breaker after the wait, would let
   a call skip the queue ordering when the breaker moves.
10. **`internal/slots`** is a new leaf package — a counting semaphore that grants in arrival
    order, standard library only — that both the runtime and the router use. A leaf keeps
    `llmgw` from reaching `agents` (Tech §5.2) without two copies of the same code; Tech §5.2 and
    `.go-arch-lint.yml` list it.

11. **A wait for the provider is not a stall** (REQ-AUT-008). A call that must wait yields the
    router's own `queued` event before anything else, and `sent` once it holds its place; no
    adapter produces either. The runtime holds the turn's stall clock between the two — for the
    main call and a compaction's, through one wrapper of the model stream — and starts it again
    at `sent`, so a provider silent once the call is sent stalls as before, each fallback
    candidate included. Without it, a third thread on an Ollama with `max_parallel = 1` would be
    announced stalled for a wait the user configured.
12. **The place is the request's, not the call's.** A call abandoned at its first-token timeout,
    by a cancel or by a caller that stopped reading keeps its provider's place until the
    adapter's stream has returned, so the next queued call is never sent beside a request still
    open at the provider (an Ollama loading a model, say). The consequence: an adapter that
    ignored its context and never returned would now block its provider's queue for every
    thread, where before it leaked a goroutine. `ports.Provider` requires honouring the context,
    and every adapter does.
13. **`slots` may import nothing of the project.** It has no `deps` entry in `.go-arch-lint.yml`,
    which go-arch-lint reads as no project import allowed; an injected import was refused.

## Tests

- `TestThreeThreadsRunConcurrently_REQ_ORC_001`: three held model calls at once.
- `TestAFourthTurnIsQueued_REQ_ORC_002`, `TestHeldTurnsStartInArrivalOrder_REQ_ORC_002`,
  `TestASecondSendToAQueuedThreadConflicts_REQ_ORC_002`, `TestCancellingAQueuedTurnDropsIt_REQ_ORC_002`,
  `TestAQueuedTurnDoesNotStall_REQ_ORC_002`, `TestAWorktreeChangeRefusesAQueuedThread_REQ_ORC_002`,
  `TestQueuedRoundTrips_REQ_ORC_002` (the real store), `TestClosingTheRuntimeStopsAQueuedTurn_REQ_ORC_002`
  and `TestMaxConcurrentTurnsIsConfigurable_REQ_ORC_002`.
- `TestLocalCallsQueuePerProvider_REQ_ORC_005`, `TestQueueTimeIsNotFirstTokenTime_REQ_ORC_005`,
  `TestACallCancelledInTheQueueIsNotSent_REQ_ORC_005`, `TestAReloadKeepsTheLocalDefault_REQ_ORC_005`,
  `TestOnlyAWaitingCallSaysItIsQueued_REQ_ORC_005`, `TestATimedOutCallHoldsItsPlaceUntilItsRequestCloses_REQ_ORC_005`,
  `TestAWaitForTheProviderDoesNotStall_REQ_ORC_005`, `TestASentCallStallsAgain_REQ_ORC_005`,
  `TestACompactionWaitingForItsProviderDoesNotStall_REQ_ORC_005` and `TestMaxParallelIsReadPerProvider_REQ_ORC_005`.
- The spec-guardian review (FIX FIRST) found a reload dropping the local default, the provider
  wait counted as a stall, the place freed before the abandoned request closed, no test of a
  graceful stop with held turns, an allowance `slots` did not need, an unlabelled §7 edge and
  the meaning of "arrival"; each is fixed, the first four with a test that was mutated and
  reddened. A second pass (FIX FIRST) found the compaction's call outside the exemption and the
  clock restarting at the provider's first event instead of at `sent`; both are fixed by the
  stream wrapper and the `sent` event, each with a mutated test.
- `internal/slots`' own tests, the concurrency one under `-race`.
