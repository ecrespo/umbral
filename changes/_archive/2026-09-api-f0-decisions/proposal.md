# Proposal — API decisions taken while implementing T-F0-03

> **Status:** draft
> **Affected base specs:** `specs/api/umbral-daemon-api-v1.md`
> **Date:** 2026-09-11 · **Author:** Ernesto Crespo (assisted draft)
> **Evidence:** `spec-guardian` review of T-F0-03, findings 2, 3, 4, 7 and 8

**Problem/motivation.**

Implementing `T-F0-03` needed five answers the API Spec does not give. Each was decided in
code and, without this Delta, the decision would live only in a comment, which is the
silent divergence Art. 9 forbids.

| # | Question the spec does not answer | Decided in code as |
|---|---|---|
| 1 | Where does the socket live on Linux without `XDG_RUNTIME_DIR`? | `$TMPDIR/umbral-<uid>`, refused unless owned by the current user at mode 0700 |
| 2 | What goes in `error.data.trace_id` before OpenTelemetry exists? | the connection id, and an empty field before the handshake |
| 3 | What does an entry in `capabilities` promise? | that the namespace's methods are callable now |
| 4 | Is `protocol_version` optional in `system.hello`? | no |
| 5 | What happens on a second `system.hello`? | `UNAUTHORIZED` and the connection closes |

**Scope.**

- What changes: five clarifications in `specs/api/umbral-daemon-api-v1.md` §2, §3 and the
  Metadata row. No new REQ, no renamed field, no changed error code.
- What does **not** change: the constitution, the MVP scope, the JSON-RPC error table and
  `protocol_version`, which stays at 1 because every change is a clarification rather than
  a contract change (Art. 8).

**Impact.**

- No client exists yet, so nothing breaks.
- Decision 1 has a security consequence and is the reason this Delta is not cosmetic: the
  fallback parent is world-writable, so an unchecked directory would let another local
  user pre-create it and read the token. That defeats REQ-SEC-003 before the daemon
  starts. The check is implemented and tested; this Delta records the location itself.
- Decision 2 is temporary. When T-F1-18 lands OpenTelemetry, `trace_id` carries a real
  trace id and this clarification is superseded.

**Alternative considered for decision 1.** Refuse to start without `XDG_RUNTIME_DIR`. It is
the stricter reading of the spec, but it breaks a bare `su` session and a container without
systemd, with an error the user cannot act on. The ownership check gives the same guarantee
without the breakage.

**Constitution check.**

- **Art. 5:** decision 1 strengthens the token's protection rather than weakening it.
- **Art. 7:** decision 2 is a documented substitute until tracing exists, not a permanent
  reinterpretation of the field.
- **Art. 8:** every change is additive or clarifying; `protocol_version` stays at 1.
- **Art. 9:** the decisions enter through this Delta instead of living in code comments.
