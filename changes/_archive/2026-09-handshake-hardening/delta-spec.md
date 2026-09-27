# Delta — a connection that never says hello, and a hello that expects no answer

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-09-26 — approved by E. Crespo together with the other three F1 deltas; the frame-limit reading "the CLI raises the limit" and the untainted catalog descriptions were chosen explicitly. Folded into PRD 1.13, Data Model 1.9, Plan 1.10 and the F1 tasks file; the API and Tech Design text is carried by each task's Spec edits and Files, and both documents say it is pending` |
| **Date** | 2026-09-26 |
| **Task** | T-F1-32, the first task of F1 |
| **Raised by** | The `spec-guardian` review of delta `2026-09-oversized-message` (findings 5 and 6), 2026-09-26; recorded as left open in `docs/checkpoints/2026-09-26-f0-closed.md` |

## Evidence

**1. A silent peer holds a slot forever.** API §8 caps concurrent connections at 32. Nothing
bounds how long a connection may stay before `system.hello`. The read loop waits on
`bufio.Scanner.Scan` with no deadline, and an empty line before the handshake is skipped
(`conn.serve`, `if len(line) == 0 { continue }`). Two cases follow:

- A local process that connects and sends nothing, or only newlines, keeps one of the 32
  slots for the life of the daemon.
- Thirty-two such processes lock every real client out.

The peer cannot read the token, but it does not need to. REQ-SEC-003 answers a *wrong* hello;
nothing answers *no* hello.

**2. A notification before the handshake is closed without a word.** JSON-RPC forbids replying
to a notification, a request with no `id`. So a well-formed notification sent before
`system.hello` falls to `reply`, which writes nothing, and the connection closes:

```json
{"jsonrpc":"2.0","method":"session.list"}
```

That contradicts §2 step 3, which says every earlier call "receives `UNAUTHORIZED`". It is
also inconsistent with the case T-F0-27 fixed: `{"method":"x"}`, which is not JSON-RPC, *does*
get `UNAUTHORIZED` with `id: null`.

The sharper version of the same gap is a **notification-form `system.hello`** carrying a valid
token. It authenticates the connection and sends back nothing. The client never learns the
daemon's version, `protocol_version` or capabilities, all of which §5.1 says a hello returns.

## Decisions

**1. A handshake deadline of 5 seconds.** A connection that has not completed `system.hello`
within 5 s of being accepted receives `UNAUTHORIZED` with `id: null`, and the daemon closes it.

- The clock starts at accept and is not reset by blank lines, partial lines or failed attempts.
  (A failed attempt already closes the connection.)
- 5 s is two and a half times the official client's whole `DialTimeout`, which covers connect
  *and* handshake. No honest client is near it.
- It is a constant, not a setting. A setting would be one more thing to get wrong, and nothing
  legitimate needs more.

**2. Before the handshake, a notification is answered.** The daemon replies `UNAUTHORIZED`
with `id: null`, then closes. JSON-RPC's rule against answering notifications assumes a session
the peer is entitled to. An unauthenticated peer is not in one: the reply is the connection-level
refusal of §2 step 3, not the answer to a call. `id: null` is already what the daemon sends when
it has no id to echo (JSON-RPC 2.0 §5, and T-F0-27).

**3. `system.hello` must carry a non-null `id`, at any point in the connection.** Two cases are
covered, and both get `UNAUTHORIZED` with `id: null` and a close:

- **Before the handshake:** a hello with no `id`, or with `"id": null`, is refused **whatever
  its token**.
- **After the handshake:** a repeated hello is already refused by §2 step 6. Today, if it carries
  no id, `reply` suppresses the answer and the connection just closes. From now on it gets the
  `UNAUTHORIZED` reply too.

The reasoning:

- **An answer that cannot be delivered is not a handshake.** Accepting such a hello silently
  would authenticate a connection that learned nothing about the daemon it talks to.
- **A null id is refused too.** JSON-RPC allows it, but discourages it. It tells the client
  nothing it can match a reply against, and the daemon itself uses a null id only for
  "undetermined".
- **The check precedes the token comparison.** §2 step 6 puts the repeated-hello check first for
  the same reason: no path may let a peer probe tokens.

This applies to `system.hello` only. After the handshake, notifications to other methods keep
their JSON-RPC meaning: they get no reply.

**4. The deviation from JSON-RPC is written into the contract.** JSON-RPC 2.0 §4.1 says a server
does not reply to a notification. Decisions 2 and 3 do reply, and **only** to a peer that is not
authenticated, or to a `system.hello`. API §1 records this deviation and its scope, so a client
author learns it from the specification rather than from the daemon.

## Specification changes

- **PRD §6.6** gains two requirements:
  - **REQ-SEC-017** · MUST · unwanted — IF a connection has not completed `system.hello` within 5 s of being accepted, THEN THE SYSTEM SHALL reply `UNAUTHORIZED` with a null id and close the connection, whatever the connection sent in the meantime.
  - **REQ-SEC-018** · MUST · unwanted — IF a JSON-RPC notification arrives before the handshake, or a `system.hello` without an `id` or with a null `id` arrives at any time, THEN THE SYSTEM SHALL reply `UNAUTHORIZED` with a null id and close the connection, without comparing any token it carries.
- **API §1** records the one deviation from JSON-RPC 2.0 §4.1: an unauthenticated peer's
  notification, and an id-less `system.hello`, are answered.
- **API §2**, three steps change:
  - step 3 names notifications among the earlier calls;
  - a new step 3a sets the 5 s deadline;
  - step 5 says `system.hello` must carry a non-null `id`, checked before the token. Step 6's
    repeated hello is answered even without an id.
- **API §8**, a new row: "Handshake deadline | 5 s from accept (§2)".
- **Versions** are assigned in the order deltas are ratified. If this one goes first, as planned,
  it is PRD 1.13 and API 1.15. `protocol_version` is unchanged. Every client that works today already sends
  an `id` with its hello and completes it in milliseconds.

## Verification

- `TestASilentConnectionIsClosedAfterTheDeadline_REQ_SEC_017` shortens the deadline instead of
  faking time. The deadline is a server field, which the test sets to 300 ms; production uses the
  5 s constant. The test connects, sends nothing, and must receive `UNAUTHORIZED` with a null id
  and see the close, measured in real time between 300 ms and 1.3 s. A variant sends a blank
  line every 100 ms and must be closed on the same schedule. A third case asserts that the
  production constant is 5 s.
- `TestANotificationBeforeHelloIsUnauthorized_REQ_SEC_018`, five cases:
  - `session.list` without an id;
  - `system.hello` without an id and with a **valid** token;
  - the same with an invalid token;
  - `system.hello` with `"id": null`;
  - an id-less `system.hello` repeated **after** a successful handshake.

  Each gets `UNAUTHORIZED`, a null id and a close. The valid-token case must also show that the
  connection is **not** authenticated.
- The teeth of each are checked by removing the deadline and the id check in turn.

## Phase

**F1, first.** `T-F1-32` runs before `T-F1-01`. F1 brings more local processes to the socket:
external integrations reporting state (REQ-INT) and scripts driving threads (REQ-AUT). These two
gaps are how a caller that never authenticates can still hold a slot, or get a silent `yes`.
(MCP does not dial the socket: the daemon is an MCP *client*.)

**On ratification** the order has to reach the files the next session reads, not stay inside this
delta:

- `specs/tasks/umbral-f1-tasks.md` gains T-F1-32 and its matrix rows;
- T-F1-01's **Depends on** becomes `T-F1-32` and, if `2026-09-frame-limit-monitoring` is
  ratified too, `T-F1-33`;
- the plan's F1 table gains the row, first.
