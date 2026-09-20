# Delta — where a notification's `seq` lives, what it counts, and which `seq` it is not

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-09-20 — applied to specs/ and archived` |
| **Date** | 2026-09-20 |
| **Task** | T-F0-16 |
| **Raised by** | T-F0-16: §6's sentence admits two incompatible readings, and clients cannot differ |

## Evidence

API Spec §6 says: "Every notification carries `seq`, a monotonic counter per session shared
by all subscribers, and the `session.snapshot` result reports the `seq` it contains
(REQ-API-002). A client applies only the events whose `seq` is greater than the snapshot's."

Three things that sentence does not settle, each of which every client has to get identically
right or the bootstrap of REQ-API-002 silently loses events.

**1. `seq` already means something else on this wire.** §5.11 `session.subscribe` returns a
`seq`, and `session.output` carries a `seq` in its parameters — per PTY session, anchoring
the screen to the byte stream (REQ-TERM-004). The counter §6 describes cannot be that one: a
`session.snapshot` covers the whole tree and reports **one** number, which would be
meaningless against a per-PTY-session count, and `workspace.created` belongs to no session at
all.

And there is a third. §5.15 `report_state` and §5.16 `report_progress` take a `seq` *parameter*
that counts per `source` and exists to drop a stale report: "a `seq` lower than or equal to the
last accepted one for that `source` returns `ok` and changes nothing". Those are F1 methods, but
the name is already spoken for three times over and nothing says so.

**2. Nowhere to put it.** §1 frames messages as NDJSON and §6 says the payloads are the
objects themselves — `workspace.created` carries a `Workspace`, `block.started` a `Block`.
Adding `seq` to those payloads would change every documented notification shape and would
collide head-on with `session.output`'s existing `params.seq`.

**3. "per session" has no referent.** Umbral's `session` is a PTY (`ses_…`). A counter "per
session shared by all subscribers" cannot be per PTY, because most notifications have no PTY.
The only scope every subscriber shares is the daemon itself.

There is a fourth, which the other three hide. If the discard rule — "apply only the events
whose `seq` is greater than the snapshot's" — is read as applying to *every* notification,
then a client bootstrapping with `session.snapshot` discards terminal output it has not seen:
§5.3's result contains workspaces, tabs, panes, layouts and threads, and **no screen**. The
output would be gone with nothing to notice.

## Decision

**1. The counter lives in the notification envelope**, as a member beside `jsonrpc`, `method`
and `params`:

```json
{"jsonrpc":"2.0","method":"workspace.created","seq":42,"params":{"id":"w1", …}}
```

Not in `params`: that would rewrite every payload §6 documents and overload
`session.output`'s own field. A client that ignores the member behaves exactly as before,
which is what §9's additive rule asks of a protocol change at `protocol_version` 1.

**2. Its scope is one daemon run, shared by every connection.** The same event carries the
same `seq` for everyone, which is what "shared by all subscribers" can only mean. It starts
at 0 when the daemon starts, and a client SHALL NOT carry one across a reconnect: the
connection dies with the daemon, and a number from the previous run names nothing in this one.

**Gaps are expected and are not loss.** A client receives only the notifications it is
eligible for — output for sessions it subscribed to, and nothing for a subscription that was
dropped — so its `seq` values skip. A missing number means "an event that was not yours",
never "an event you lost". Loss has its own signal, `session.unsubscribed` (§6, §8).

**3. The discard rule applies to what the snapshot contains.** `session.snapshot` bootstraps
the *tree*: the workspace, tab, pane and layout notifications, and the thread ones from F1. A
client discards those when their `seq` is not greater than the snapshot's. `session.output`
is bootstrapped separately by §5.11, against that method's own per-session `seq`, and a client
SHALL NOT discard output on the envelope counter — the two answer different questions and
§5.3 carries no screen to have contained it.

**4. The snapshot reads its `seq` before it reads the tree.** The daemon persists before it
notifies (DD-007), so an event whose `seq` has been assigned is already in the database.
Reading the counter first therefore guarantees that everything at or below the reported `seq`
is in the snapshot; the snapshot may additionally contain a few events *above* it, which
makes the client apply an event it already has. That direction is safe — every tree
notification carries the whole record — and the other is not.

**5. "Monotonic" is a property of assignment, not of arrival.** The number is taken when the
daemon dispatches the event. Output then travels through the per-client queue of §8, where it
waits and may be batched; control notifications are written to the socket directly. So a
connection subscribed to a session can be handed a higher `seq` before a lower one, and the
daemon does not promise otherwise.

This matters because it rules out the reading a client would most naturally reach for. "Monotonic"
invites "discard anything at or below the last number I applied", and a client that did that would
drop live tree events whenever output was in flight. The only comparison the protocol supports is
against the `seq` that `session.snapshot` reported — a fixed number, taken once — and decision 3
already limits which notifications it applies to. THE SYSTEM SHALL NOT be assumed to deliver `seq`
in order on a connection, and a client SHALL NOT compare a notification's `seq` against the
previous notification's.

**6. `focused` is nullable in all three members.** §5.3 typed only `thread_id` that way, but a
daemon that has just started has no focused workspace and no focused tab either, and that is the
state every client meets before the first `workspace.create`. Null means "nothing is focused". It
is not an error and not "unknown": the daemon always knows what is focused, and sometimes the
answer is nothing.

## MODIFIED

### specs/api/umbral-daemon-api-v1.md → §6, the `seq` paragraph

- **Before:** "Every notification carries `seq`, a monotonic counter per session shared by
  all subscribers…".
- **After:** the same intent, said precisely: the envelope member, the daemon-run scope, the
  reconnect rule, gaps, and the sentence naming `session.output`'s `params.seq` as a
  different number so nobody conflates them.
- **After:** "increases by one per notification" is corrected to "per event the daemon
  dispatches" — a number is burnt whether or not anyone is listening, and a batched delivery
  carries only the highest of the numbers it collapses.
- **After:** the paragraph of decision 5, stating that the counter is ordered in assignment
  only, and the clause naming `report_state`/`report_progress`'s `seq` as the third one.

### specs/api/umbral-daemon-api-v1.md → §1, framing

- **After:** the notification envelope is shown, with `seq` in it, next to the NDJSON rule.

### specs/api/umbral-daemon-api-v1.md → §5.3 `session.snapshot`

- **After:** the bootstrap list says which notifications the discard rule covers and which it
  does not, and the ordering guarantee of decision 4.
- **After:** the result types all three members of `focused` as nullable (decision 6).
- **Before:** bootstrap step 1 read "open `events.subscribe` on a second connection and wait for
  its acknowledgement".
- **After:** "open a second connection and complete `system.hello`". `events.subscribe` appeared
  in that one line and nowhere else in the specification or the code: there is no such method,
  and none is needed, because tree notifications go to every authenticated connection.

### specs/prd/umbral-mvp.md → REQ-API-002

- **After:** "monotonic per session" becomes "monotonic per daemon run, shared by every
  connection". The criterion's substance is unchanged; the word `session` was the problem,
  because in this system it already names a PTY.

## NOT MODIFIED

No data-model change: the counter is not persisted, and must not be — it numbers one run.
`session.subscribe` and `session.output` keep their `seq` exactly as they are.
`protocol_version` stays at 1: an added envelope member a client may ignore is additive
under §9.

## Verification

- `TestSnapshotCarriesSeq_REQ_API_001` asserts the result's shape and that the reported `seq`
  is the counter as of before the tree was read.
- `TestNoGapBetweenSnapshotAndStream_REQ_API_002` runs a client that subscribes, buffers,
  snapshots and applies the buffer while a second client is changing the tree, and asserts
  the reconstructed tree equals the daemon's.
- `TestOutputIsNotDiscardedOnTheEnvelopeSeq` pins decision 3, which is the one a reasonable
  reading of the old sentence would have got wrong.
- `TestSeqIsNotOrderedPerConnection_REQ_API_002` pins decision 5 by constructing the interleaving
  — output queued, a tree notification dispatched behind it — and asserting the client sees the
  numbers out of order. It is written to fail if the daemon ever starts promising ordering, so the
  documented weakness and the code cannot drift apart.
- `TestSnapshotFocusIsNullOnAFreshDaemon_REQ_API_001` pins decision 6 against a daemon with no
  workspace.
- `TestSnapshotReturnsEveryPaneAndLayout_REQ_API_001` covers the half of §5.3's result that was
  only ever asserted against a fake: it runs over real SQLite.
