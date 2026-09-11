# Delta — `session.unsubscribed` notification

## ADDED

### specs/api/umbral-daemon-api-v1.md → §6 Notifications

| Method | Payload | REQ |
|---|---|---|
| `session.unsubscribed` | `{session_id, reason}` | REQ-TERM-004 |

`reason`: `slow_client`.

WHEN THE SYSTEM drops a subscription for exceeding the per-client queue limit of §8, THE
SYSTEM SHALL emit `session.unsubscribed` for that session before it stops delivering.

A client that receives it and still wants the session SHALL call `session.subscribe` again,
which returns a fresh snapshot. Anything the dropped subscription had not yet delivered is
in that snapshot, so nothing is lost: the screen is re-sent rather than the backlog.

## MODIFIED

### specs/api/umbral-daemon-api-v1.md → §8 Limits, queue row
- **Before:** "8 MiB; beyond that the daemon drops the subscription and the client
  re-subscribes (receiving a new snapshot)".
- **After:** the same, plus "the daemon announces the drop with `session.unsubscribed`
  (§6)".
- **Reason:** as written, the client has no way to learn that its subscription ended.
  Output stops, which is indistinguishable from a session where nobody is typing.

## REMOVED
— (none)
