# Tasks — delta `2026-09-bootstrap-sweeper`

### [ ] T-BS-01 · Settle where a shell's bootstrap files live and who removes the orphans
- **What:** PRD §6.1 gains REQ-TERM-012; Tech Design §9.2 says the bootstrap directories live
  in the runtime directory and that a daemon sweeps the previous run's after taking the
  instance lock and before restoring the tree.
- **REQ:** REQ-TERM-012
- **Files:** `specs/prd/umbral-mvp.md`, `specs/technical/umbral-architecture.md`,
  `specs/tasks/umbral-f0-tasks.md`, `specs/plans/umbral-mvp-plan.md`
- **Done:** `python3 tools/sdd_check.py` green with REQ-TERM-012 carrying a task;
  `T-F0-19` present in the F0 task list, the traceability matrix and the plan's F0 table.

### [ ] T-F0-19 · Bootstrap files in the runtime directory, and a sweep at start
> The implementation. Lives in `specs/tasks/umbral-f0-tasks.md`; repeated here so the delta
> carries its own record of what it asked for.
- **What:** `shellinteg.Prepare` creates its directory under the daemon's runtime directory
  instead of `os.TempDir()`; `cmd/umbrald` deletes every `shellinteg-*` it finds there after
  `AcquireInstanceLock` and before `Restore`; a removal that fails is logged and startup
  continues.
- **REQ:** REQ-TERM-012
- **Files:** `internal/sessions/adapters/shellinteg/bootstrap.go`, `cmd/umbrald/main.go`,
  `internal/config/paths.go`
- **Depends on:** T-F0-08, T-F0-18
- **Done:** the four tests of the delta's Verification section green, and the measured teeth
  check — `kill -9` a daemon with live sessions, confirm the directories are there, restart,
  confirm they are gone.
