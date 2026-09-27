# Tasks — delta `2026-09-oversized-message`

### [x] 2026-09-26 T-F0-27 · The frame limit says what happens past it
- **What:** API Spec §1, §2 step 3 and §8 now say three things (API 1.14):
  - the 4 MiB limit counts the `\n`;
  - past the limit, the daemon replies `VALIDATION_ERROR` with `id: null` and closes;
  - before the handshake, an oversized, unparseable or non-JSON-RPC line gets
    `UNAUTHORIZED` and a close.

  The last point is also a code fix: two of those cases used to answer with a protocol error
  and leave the connection open (REQ-SEC-003).
- **REQ:** REQ-SEC-003; API Spec §1 (the frame limit).
- **Files:** `specs/api/umbral-daemon-api-v1.md`, `internal/api/conn.go`,
  `internal/api/server_test.go`
- **Depends on:** T-F0-26
- **Done:** `TestAMessageAtTheLimitIsAccepted` and
  `TestAnythingButHelloFirstIsUnauthorized_REQ_SEC_003` are green, and each was seen red,
  against a one-byte shift of the limit and against the old code respectively. `task schema`
  and `task ci` are green.
