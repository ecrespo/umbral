# Tasks — Delta 2026-09-slow-client-notification

> Delta: `changes/2026-09-slow-client-notification/delta-spec.md` · Generated: 2026-09-11
> The daemon already emits the notification; these tasks fold it into `specs/`.

## Tasks

### [x] 2026-09-11 T-SUB-01 · Fold `session.unsubscribed` into the API Spec
- **What:** add the row to §6, add the pointer to the §8 queue row, and bump the change history.
- **REQ:** REQ-TERM-004
- **Depends on:** approval of this delta
- **Done:** §6 lists `session.unsubscribed`; `python3 tools/sdd_check.py` exits 0.
- **Result:** folded into API Spec v1.3. §6 carries the row and the two sentences on what a
  client does with it, and the §8 queue row points at it.

### [x] 2026-09-11 T-SUB-02 · Confirm the daemon matches the folded spec
- **What:** check `internal/api/fanout.go` against the folded §6 and §8.
- **REQ:** REQ-TERM-004
- **Depends on:** T-SUB-01
- **Done:** `task ci` green; `TestSlowClientIsDropped` passes.
- **Result:** `internal/api/fanout.go:125` emits the notification before the subscription
  goroutine returns, which is the "before it stops delivering" the folded §6 requires. No
  code change was needed.

## Traceability matrix

| REQ | Tasks | Verification |
|---|---|---|
| REQ-TERM-004 | T-SUB-01, T-SUB-02 | TestSubscribeSnapshotBeforeLive_REQ_TERM_004, TestSlowClientIsDropped |
