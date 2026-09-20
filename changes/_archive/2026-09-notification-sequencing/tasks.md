# Tasks — delta `2026-09-notification-sequencing`

### [x] 2026-09-20 T-NS-01 · Pin the notification sequence number in the specs
- **What:** API Spec §1 shows the notification envelope; §6's `seq` paragraph states the
  envelope member, the daemon-run scope, gaps, the reconnect rule and the distinction from
  `session.output`'s `params.seq`; §5.3 states which notifications the discard rule covers
  and the read-order guarantee, types `focused` as nullable throughout and drops the
  `events.subscribe` step that named no real method; PRD REQ-API-002 says "per daemon run".
- **REQ:** REQ-API-001, REQ-API-002
- **Files:** `specs/api/umbral-daemon-api-v1.md`, `specs/prd/umbral-mvp.md`
- **Done:** `python3 tools/sdd_check.py` green; the three tests named in the delta's
  Verification section green.
