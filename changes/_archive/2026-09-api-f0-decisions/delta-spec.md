# Delta — API decisions for F0 (findings 2, 3, 4, 7, 8 of the T-F0-03 review)

## MODIFIED

### specs/api/umbral-daemon-api-v1.md → Metadata, transport row (finding 2)
- **Before:** `Unix socket $XDG_RUNTIME_DIR/umbral/umbral.sock (macOS: ~/Library/Application Support/Umbral/umbral.sock)`.
- **After:** the same two locations, plus a third for Linux sessions that have no
  `XDG_RUNTIME_DIR`, such as a bare `su` or a container without systemd:
  `$TMPDIR/umbral-<uid>/umbral.sock`.
  Because that parent directory is world-writable, THE SYSTEM SHALL refuse to use the
  fallback directory when it already exists and is not a directory owned by the current
  user with permissions exactly `0700`. Without that check another local user could
  pre-create it and read the token, defeating REQ-SEC-003 before the daemon starts.
- **Reason:** the spec named no fallback, so the daemon had nowhere defined to put its
  socket in those sessions.

### specs/api/umbral-daemon-api-v1.md → §3 Error format, `trace_id` (finding 3)
- **After:** `trace_id` carries the OpenTelemetry trace id of the turn that produced the
  error. Until tracing exists (T-F1-18) THE SYSTEM SHALL put the `connection_id` there
  instead, and SHALL leave the field empty when the error precedes the handshake and there
  is no connection id yet. THE SYSTEM SHALL NOT mint an identifier that correlates to
  nothing: an id that appears in no other log line is worse than an absent one.
- **Reason:** Art. 7 exists so a log line can be joined to something. The field's format
  was shown in an example but never stated, and F0 has no tracer.

### specs/api/umbral-daemon-api-v1.md → §2 Handshake, `capabilities` (finding 4)
- **After:** each entry names a method namespace (`sessions`, `blocks`, `threads`, `mcp`,
  `models`) whose methods the daemon serves **at that moment**. THE SYSTEM SHALL derive the
  list from its method table rather than declaring it statically, and SHALL NOT advertise a
  namespace whose methods are not registered. `system` is never listed: every client may
  always call it. An empty list is valid and is what F0 returns until `session.*` lands.
- **Reason:** the spec showed an example list and never said what an entry promised, so a
  client reading it could branch onto methods that do not exist.

### specs/api/umbral-daemon-api-v1.md → §2 Handshake, `protocol_version` (finding 7)
- **After:** `protocol_version` is **required** in `system.hello`. A request that omits it
  receives `UNSUPPORTED_PROTOCOL_VERSION` with the accepted versions in `data.supported`.
- **Reason:** treating an absent field as compatible silently pairs this daemon with a
  client built for a version it never declared.

### specs/api/umbral-daemon-api-v1.md → §2 Handshake, repeated handshake (finding 8)
- **After:** IF `system.hello` arrives on a connection that already completed the
  handshake, THEN THE SYSTEM SHALL reply `UNAUTHORIZED` and close the connection.
  The check happens before the token is compared, so an authenticated connection cannot be
  reused to test tokens.
- **Reason:** `CONFLICT` is defined in §3 as an incompatible *state* such as `thread.send`
  during a turn. A repeated handshake is a protocol violation, and the spec covered neither.

### specs/api/umbral-daemon-api-v1.md → §2, order of validation in `system.hello`
- **After:** THE SYSTEM SHALL compare the token before validating any other parameter
  except the repeated-handshake check above. REQ-SEC-003 admits no exception, so no
  validation error may answer first and leave the connection open for another attempt.
- **Reason:** as implemented before this Delta, a handshake with a bad token *and* an
  invalid `client_kind` received `VALIDATION_ERROR` and the connection stayed open.

## ADDED
— (none: no new REQ, no new field)

## REMOVED
— (none)
