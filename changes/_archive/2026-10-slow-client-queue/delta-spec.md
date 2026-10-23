# Delta — a client that stops reading holds up no other

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-23 by the Tech Lead` ("si, hazlo", on the proposal of a per-connection queue instead of a message broker); folded into PRD 1.22 (REQ-API-006) and API 1.47 (§6, §8) |
| **Date** | 2026-10-23 |
| **Task** | T-HRD-04 |
| **REQ** | REQ-API-006 (new) |
| **Raised by** | The review of the `umb ai` stream fix (T-HRD-03, PR #41) |

## Evidence

`Server.broadcast` ran on the daemon's single dispatch goroutine and wrote each control
notification to every connection's socket in turn, through `conn.notify` and `writeRaw`, with no
queue and no deadline. API §8's 8 MiB per-client queue covered `session.output` only. A client
that stopped reading — a TUI suspended with `ctrl+z`, a script paused in a pager — filled its
socket buffer, and the next broadcast blocked the dispatcher: every other client stopped
receiving notifications, and the bus began dropping events for all of them, while the stalled
client held nothing but a socket. `TestASlowClientHoldsUpNoOtherClient_REQ_API_006` reproduced it:
the dispatcher was still writing after 20 s.

## Decisions

1. **A queue per connection, not a broker.** The blocking was the last hop, a write to one
   socket, which a message broker between modules would not remove. Each connection gets an
   outbox of framed control notifications that its own goroutine writes; `broadcast` only
   queues. No new dependency or process (Art. 1, local-first).
2. **8 MiB, then the connection is closed.** The limit is the same as a subscription's. Past it
   the daemon closes the connection instead of dropping notifications: a lost `pane.closed` or
   `thread.turn_finished` would leave the client's cache different from the daemon's with
   nothing to say so, while a closed connection does, and the snapshot taken on reconnecting
   replaces what it missed (REQ-API-001). Nothing is sent first: a client 8 MiB behind would not
   read it. The limit counts what is unwritten, not why: a client that reads but falls 8 MiB behind a
   burst is closed too, which the test met with one CPU, where the dispatcher can queue a burst
   before the reader's writer runs. Control traffic does not come in such bursts. An empty queue
   always takes a line, so a single notification larger than 8 MiB, possible with
   `[api] max_message_bytes` up to 64 MiB, is held to the frame limit (§1) alone.
3. **No write deadline.** The proposal also had one, as a safety net. With the outbox a client
   that does not read harms only itself, so a deadline would only disconnect a TUI left suspended
   for longer than it, with nobody else waiting. Replies are written by the connection's read
   loop, so a reply to a client that does not read holds up that connection alone.
4. **Order.** One connection's control notifications are written in the order they were
   dispatched. Output keeps its own queue per subscription, so the two still interleave as §6
   already allows, and replies are written beside them as before.
5. **What is not queued.** `session.output` and `session.unsubscribed` keep their subscription's
   writer and limit (§8), and `notification.posted` its foreground queue of 16 (§5.35): each
   already had a writer of its own.
6. **The handshake gate.** A connection receives broadcasts from the moment `system.hello`
   succeeds and its writer runs, read through an atomic flag rather than the read goroutine's
   `authenticated`, which the dispatcher had been reading unsynchronised. The flag is set last,
   after the connection id and the writers, so nothing the dispatcher reads is written after it.

## Tests

- `TestASlowClientHoldsUpNoOtherClient_REQ_API_006`: one client stops reading after the
  handshake; 192 broadcasts of 64 KiB, paced to the reading client, reach it in full, the dispatcher finishes, and the
  stalled connection is closed. Red before the change (the dispatcher blocked); removing the
  close on overflow reddens its last check.
- `TestASlowClientIsClosedNotTrimmed_REQ_API_006`: the outbox takes a line over the limit when
  empty, refuses a line past the limit, holds nothing afterwards, and hands its writer nothing
  more. Requiring the queue to be under the limit even when empty reddens it.
- The spec-guardian review (FIX FIRST) found the first test unpaced, so with one CPU the reading
  client overflowed too (8 of 8 runs), and the connection id written after the flag that
  publishes it; both are fixed, the test paced to the reader and green over 20 runs at
  `-cpu 1,2,4`.
