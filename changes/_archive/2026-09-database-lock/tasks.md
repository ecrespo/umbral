# Tasks — delta `2026-09-database-lock`

### [x] 2026-09-26 T-F0-24 · A daemon locks the database it recovers
- **What:** `config.AcquireDatabaseLock` takes `<database>.lock` beside the database after the
  instance lock and before `store.Open`; held elsewhere, `umbrald` exits 75. Tech §9.4 says so
  (1.12).
- **REQ:** none — Data Model §6's premise, as the instance lock (infrastructure).
- **Files:** `internal/config/instancelock.go`, `internal/config/instancelock_test.go`,
  `cmd/umbrald/main.go`, `cmd/umbrald/dblock_test.go`,
  `specs/technical/umbral-architecture.md`
- **Done:** the three tests of the delta's Verification green.
