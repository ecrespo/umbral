# Tasks — delta `2026-10-wait-engine`

### [x] 2026-10-03 T-F1-23 · Wait engine
- **What:** decisions 1–11 in `internal/waits/**`, `internal/api/waits.go`, `internal/api/conn.go`
  (deferred answers), `internal/api/threads.go` (`thread.send`'s `wait`), the runtime's `Status`
  and `RejectBlocked`, `sessions.Service.ScreenText` and `cmd/umbrald/waits.go`, with API 1.26
  written.
- **REQ:** REQ-AUT-001, REQ-AUT-002, REQ-AUT-003, REQ-AUT-004

### [ ] Ratification
- The Tech Lead ratifies decisions 3, 7 and 8 (each has an alternative), folds the delta into API
  §2, Tech §3/§4/§5.2 and the F1 tasks file, and moves it to `changes/_archive/`.
