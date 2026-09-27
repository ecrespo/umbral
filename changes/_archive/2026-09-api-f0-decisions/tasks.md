# Tasks — Delta 2026-09-api-f0-decisions

> Delta: `changes/2026-09-api-f0-decisions/delta-spec.md` · Generated: 2026-09-11
> Approved 2026-09-11. The code already behaved this way; these tasks folded the decisions into `specs/`.

## Tasks

### [x] 2026-09-11 T-APIF0-01 · Fold the five clarifications into the API Spec
- **What:** apply the six MODIFIED entries of the delta to
  `specs/api/umbral-daemon-api-v1.md` (Metadata transport row, §2 handshake ×4, §3 `trace_id`),
  and bump its change history.
- **REQ:** REQ-SEC-003, REQ-SEC-007
- **Depends on:** approval of this delta
- **Done:** `python3 tools/sdd_check.py` exits 0; the spec states the fallback directory,
  the `trace_id` substitute, the meaning of a `capabilities` entry, that `protocol_version`
  is required, and what a repeated handshake does.

### [x] 2026-09-11 T-APIF0-02 · Confirm the code matches the folded spec
- **What:** re-read `internal/api` against the folded §2 and §3 and fix any drift.
- **REQ:** REQ-SEC-003, REQ-SEC-007
- **Depends on:** T-APIF0-01
- **Done:** `task ci` green; `TestHelloWithBadTokenAndBadClientKindStillCloses_REQ_SEC_003`,
  `TestSecondHelloIsRefusedAndCloses`, `TestHelloRequiresTheProtocolVersion`,
  `TestCapabilitiesFollowTheMethodTable` and
  `TestVerifyPrivateDirRejectsAForeignRuntimeDirectory` all pass.

## Traceability matrix

| REQ | Tasks | Verification |
|---|---|---|
| REQ-SEC-003 | T-APIF0-01, T-APIF0-02 | TestHelloWithBadTokenAndBadClientKindStillCloses_REQ_SEC_003 |
| REQ-SEC-007 | T-APIF0-01, T-APIF0-02 | TestSocketPermissions0600_REQ_SEC_007 |

## Execution log

| Date | Tasks | Result | Notes |
|---|---|---|---|
| 2026-09-11 | T-APIF0-01, T-APIF0-02 | folded into `specs/` | API Spec goes to v1.2; `protocol_version` stays at 1 because every change clarifies rather than alters the contract (Art. 8). §2 gains a Runtime directory table with the `$TMPDIR/umbral-<uid>` fallback and its ownership rule, two numbered handshake rules (token compared first, repeated handshake closes), a required `protocol_version` and the meaning of a `capabilities` entry. §3 states what `trace_id` carries before tracing exists. The code needed no change: `task ci` green, and the five tests named in T-APIF0-02 all pass. |
