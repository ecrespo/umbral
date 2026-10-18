# Tasks — delta `2026-10-notifications`

### [x] T-F1-27 · Notifications
- **What:** decisions 1–11 in `internal/notify/**`, `internal/api/notifications.go` (plus `conn.go`, `system.go`, `server.go`, `errors.go`, `schema.go`), `internal/integrations` (the `Notifier` port and the blocked transition), `internal/config` (`[notifications] enabled`), `cmd/umbrald` (wiring), `cmd/umb/notify.go`, `internal/tui` (status line), `docs/qa/f1-tui.md` step 13 and `.go-arch-lint.yml`. API §2, §5.35 and §6 are written on the task branch.
- **REQ:** REQ-NTF-001, REQ-NTF-002

### [x] Approval
- Approved by the implementing agent under the Tech Lead's standing F1 instruction; the Tech Lead reviews it afterwards.
- Fold: API version and history, Tech Design §5.2's `notify` row, the F1 tasks file with **Files** widened. Then archive.
