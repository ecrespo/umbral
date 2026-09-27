# Delta — what the daemon does with a message past the frame limit

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-09-26 — draft approved by E. Crespo ("apruebo el delta anterior"); the amendment to decision 3 (split out of the draft's decision 2) confirmed by E. Crespo the same day, before the commit` |
| **Date** | 2026-09-26 |
| **Task** | T-F0-27 |
| **Raised by** | The `spec-guardian` review of T-F0-26's `TestOversizedMessageIsRejected` fix (finding 1), 2026-09-26 |

## Evidence

**The limit is specified; crossing it is not.** API Spec §1 says "Maximum message size:
4 MiB" and §8 repeats the number. Neither says whether the delimiter counts, what a client
that sends more receives, or whether the connection survives. The daemon has an answer to all
three, written in `internal/api/conn.go` (`conn.serve`) since T-F0-03, and a client author can
learn it only by reading Go.

**The gap has already cost a test.** On the macOS runner, `TestOversizedMessageIsRejected`
failed with `write: broken pipe` (CI run 36284097081): the daemon answered and hung up while
the client was still writing, and nothing in the spec said that hanging up was intended.
T-F0-26 fixed the test; the behaviour it relies on is still nobody's contract.

**What the daemon does today, measured on 2026-09-26** against a test server, with a padded
request to an unknown method:

| Bytes sent, `\n` included | Answer | Connection |
|---|---|---|
| 4 194 303 | the message is parsed and handled (`UNAUTHORIZED`, as the first call) | per that answer |
| 4 194 304 | the message is parsed and handled (`UNAUTHORIZED`) | per that answer |
| 4 194 305 | `{"id":null,"error":{"code":-32602,"message":"message exceeds the 4194304 byte limit","data":{"domain_code":"VALIDATION_ERROR",…}}}` | closed by the daemon |

## Decisions

**1. The limit counts the delimiter.** A message is at most **4 194 304 bytes including its
terminating `\n`**. That is where the daemon's line reader already stops, and counting what is
actually on the wire is the only reading a client can check without knowing the reader.

**2. Past the limit, the daemon answers once and closes.** On an authenticated connection
it replies `VALIDATION_ERROR` (`-32602`) with `id: null` and a message naming the limit, then
closes the connection.

- `id` is `null` because the id was never read. JSON-RPC 2.0 §5 prescribes null when the
  request's id cannot be determined, and the daemon does not parse a prefix to guess one.
- The connection closes because the framing is lost. The rest of the oversized line is
  still arriving, and reading on would parse its tail as a new message. There is no
  delimiter to resynchronise on that the daemon can trust.
- `-32602` ("invalid params" in JSON-RPC's own table) is kept knowingly. It is what the daemon
  has always sent, and `VALIDATION_ERROR` is this API's code for "you sent something outside
  the contract's limits" (§8 uses it the same way for the wait limit). Changing it now would
  break a client that already handles it, and would buy nothing.

**3. Before the handshake, anything but a valid `system.hello` is `UNAUTHORIZED` — oversized,
unparseable or not JSON-RPC alike.** *Amended before implementation.* The draft ratified on
2026-09-26 said an oversized first message was `VALIDATION_ERROR`. The `spec-guardian` review
of the draft (HIGH) showed that this contradicts REQ-SEC-003 (MUST) and API §2 step 5,
*"REQ-SEC-003 admits no exception, so no validation error may answer first"*. The draft changed
neither of them. So the reply code stays the one §2 already requires, and the delta says so
instead of carving out an exception.

The same review (MEDIUM) found that the code broke §2 for two neighbouring cases:

- Invalid JSON before `system.hello` answered `PARSE_ERROR` and **left the connection open**.
- A message that is not JSON-RPC answered `INVALID_REQUEST` and did the same.

An unauthenticated peer could therefore keep trying on the same connection. It can still
*hold* a slot by sending nothing, because neither §2 nor §8 sets a handshake deadline. That is
a separate gap, left for a delta of its own (see "Left open"). The spec was right there and the code was wrong, so it is fixed under this task
without a new decision. All three cases now answer `UNAUTHORIZED` and close:

- An unparseable line gets `id: null`.
- A line that is not JSON-RPC echoes the id if it carried one.

**4. A client may see its write fail.** The daemon closes while the client is still writing,
so the client's write can fail with `EPIPE` or `ECONNRESET`. That is expected: the answer is
already on the socket, and a client SHOULD read it before reporting the error. The official
clients never get here, because `umb` and the TUI send nothing near the limit. The rule exists
for third-party clients.

**5. Out of scope: the other direction.** The client library, `internal/client`, refuses to *read* a
frame over the limit, but nothing in this delta promises that the daemon never *writes* one.
`block.get` with `include: "raw"` is the method that could, and it returns
`output_raw_truncated` from what the store kept. Whether the store's cap sits below the frame
limit, once the JSON and base64 overhead is added, deserves its own check. It is not folded
in here.

## Left open

The review found two neighbouring gaps. Both are older than this delta and both are left for
their own deltas:

- **No handshake deadline.** A peer that connects and sends nothing, or only blank lines, holds
  one of the 32 slots indefinitely.
- **A notification before `system.hello` gets no reply.** A well-formed JSON-RPC notification
  sent before the handshake is closed without any reply, because JSON-RPC forbids answering a
  notification. §2 step 3 says every earlier call "receives `UNAUTHORIZED`". Either that
  sentence gains the exception, or the daemon answers with a null id. A notification-form
  `system.hello` with a valid token authenticates without a reply.

## Specification changes

- **API Spec §1**, the framing sentence becomes: "Framing: JSON messages delimited by `\n`
  (NDJSON). A message is at most 4 MiB (4 194 304 bytes) including its delimiter. Past that
  limit the daemon replies `VALIDATION_ERROR` with `id: null` — it has not read the id — and
  closes the connection, because the framing cannot be recovered; before the handshake the
  reply is `UNAUTHORIZED` instead (§2). A client may see its own write fail with `EPIPE` or
  `ECONNRESET` and SHOULD read the reply first."
- **API Spec §2 step 3** names what "any earlier call" includes: a line that is not JSON, is
  not a JSON-RPC request, or is past the frame limit.
- **API Spec §8**, the `JSON message` row: "4 MiB including the `\n`; past it,
  `VALIDATION_ERROR` (before the handshake `UNAUTHORIZED`) and the connection is closed (§1)".
- **API Spec version** 1.14, with a history row naming this delta.

No REQ changes, no schema change, and `protocol_version` stays 1. Every sentence above either
documents behaviour clients already met or restates what REQ-SEC-003 already required. Art. 8
is satisfied because nothing is added to the protocol.

## Verification

- `TestOversizedMessageIsRejected` (`internal/api`) now authenticates first, sends 1 MiB past
  the limit, and asserts `VALIDATION_ERROR`, `id: null` and the close.
- **New**, `TestAMessageAtTheLimitIsAccepted`: exactly 4 194 304 bytes with the `\n` is an
  ordinary call and the connection stays open; one byte more gets `VALIDATION_ERROR` and a
  close. Its teeth were checked by moving the scanner's limit one byte each way. `-1` fails the
  first half and `+1` fails the second.
- **New**, `TestAnythingButHelloFirstIsUnauthorized_REQ_SEC_003`, with three cases before the
  handshake: not JSON, not JSON-RPC, and past the frame limit. Each must get `UNAUTHORIZED`
  and a close. All three were red before the change: two protocol errors that left the
  connection open, and one `VALIDATION_ERROR`.
- `TestInvalidJSONIsAParseError`, `TestNonJSONRPCMessageIsAnInvalidRequest` and
  `TestErrorsWithAnUndeterminedIDCarryNull` authenticate first. The protocol errors they check
  are what an **authenticated** client gets.
- `task schema` stays green, because the protocol document does not change shape.

## Phase

**F0**, as `T-F0-27`. It is small, it documents F0 code, and it closes a REQ-SEC-003 gap
that F0 shipped.
