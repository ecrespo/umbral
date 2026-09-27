# Tasks — delta `2026-09-skills-cli`

### [ ] T-F1-34 · Skill store and `skill.*` methods
- **What:**
  - **Bundle checks:** front matter; entry kinds; paths; modes; size and entry limits enforced
    while streaming; the canonical-manifest digest.
  - **Store:** staging and rename into `$XDG_DATA_HOME/umbral/skills/<name>/`; the recovery
    sweep of Data Model §6.
  - **Methods:** `skill.inspect`, `install` (with `expected_sha256`), `list`, `get`,
    `set_enabled` and `remove`, plus `skill.changed`.
  - **Rules:** local sources only, classified before resolving; at most 64 enabled, and a 65th
    install is stored disabled.
  - **Arch:** the `api` rows. The `skills` table is written into the Data Model and T-F1-01 at
    ratification, not here.
  - **Ratification bookkeeping:** tasks, matrix and plan rows in the F1 files.
- **REQ:** REQ-SKL-001, REQ-SKL-002, REQ-SKL-006
- **Files:** `internal/context/**`, `internal/store/**`, `internal/api/**`, `.go-arch-lint.yml`,
  `specs/api/umbral-daemon-api-v1.md`, `specs/data-model/umbral-schema.md`,
  `specs/prd/umbral-mvp.md`, `specs/tasks/umbral-f1-tasks.md`, `specs/plans/umbral-mvp-plan.md`
- **Depends on:** T-F1-01, whose migration 0005 includes `skills` once this delta is ratified.
- **Done:** these are green and `task schema` and `task arch` are green:
  - `TestInstallCopiesRecordsAndRunsNothing_REQ_SKL_001`;
  - `TestABadBundleIsRefusedWhole_REQ_SKL_002`;
  - `TestACrashMidInstallLeavesNothingAfterRestart_REQ_SKL_002`;
  - `TestARemoteSourceIsRefused_REQ_SKL_006`.
- **Estimate:** 3d

### [ ] T-F1-35 · `umb skill`
- **What:** `umb skill install|list|show|enable|disable|remove`; inspect, confirm, then install
  with the shown digest; `--yes`, `--replace` and `--json`.
- **REQ:** REQ-SKL-001, REQ-SKL-003
- **Files:** `cmd/umb/**`, `specs/technical/umbral-architecture.md`
- **Depends on:** T-F1-34
- **Done:** `TestInstallAsksOrNeedsYes_REQ_SKL_001` and
  `TestSkillCommandsMirrorTheMethods_REQ_SKL_003` green.
- **Estimate:** 1d

### [ ] T-F1-36 · Umbral's agent sees skills, loads them on demand, and treats them as untrusted
- **What:**
  - **Catalog:** in every prompt, capped at 256 characters per description and 64 skills,
    inside the budget; rebuilt per turn, so changes reach the next turn.
  - **`skill_load`:** a `ReadOnly` tool behind a `tools/ports.SkillReader`; `file` confined to
    the bundle; its output taints the turn (REQ-SEC-006).
  - **Destructive patterns:** `umb skill install|enable|remove` and `umb mcp add` join the list.
- **REQ:** REQ-SKL-004, REQ-SKL-005, REQ-SKL-007
- **Files:** `internal/context/**`, `internal/tools/**`, `internal/security/**`, `cmd/umbrald/**`
- **Depends on:** T-F1-09, T-F1-11, T-F1-12, T-F1-14, T-F1-34
- **Done:** these are green:
  - `TestPromptCarriesDescriptionsNotBodies_REQ_SKL_004`;
  - `TestASkillCannotRunOrWidenAnything_REQ_SKL_005`;
  - `TestASkillChangeReachesTheNextTurn_REQ_SKL_007`.
- **Estimate:** 1.5d

## Traceability matrix (delta)

| REQ | Tasks | Verification |
|---|---|---|
| REQ-SKL-001 | T-F1-34, T-F1-35 | TestInstallCopiesRecordsAndRunsNothing_REQ_SKL_001, TestInstallAsksOrNeedsYes_REQ_SKL_001 |
| REQ-SKL-002 | T-F1-34 | TestABadBundleIsRefusedWhole_REQ_SKL_002, TestACrashMidInstallLeavesNothingAfterRestart_REQ_SKL_002 |
| REQ-SKL-003 | T-F1-35 | TestSkillCommandsMirrorTheMethods_REQ_SKL_003 |
| REQ-SKL-004 | T-F1-36 | TestPromptCarriesDescriptionsNotBodies_REQ_SKL_004 |
| REQ-SKL-005 | T-F1-36 | TestASkillCannotRunOrWidenAnything_REQ_SKL_005 |
| REQ-SKL-006 | T-F1-34 | TestARemoteSourceIsRefused_REQ_SKL_006 |
| REQ-SKL-007 | T-F1-36 | TestASkillChangeReachesTheNextTurn_REQ_SKL_007 |
