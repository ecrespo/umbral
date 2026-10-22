# Tasks — delta `2026-10-integration-surface`

### [x] 2026-10-17 T-F1-25 · Integration surface
- **What:** decisions 1–15 in the following places:
  - `internal/workspaces` (the domain rule, the tree store's read, the move);
  - `internal/sessions` (the environment);
  - `internal/integrations/**`;
  - `internal/api/panes.go`;
  - `cmd/umbrald` (the wiring and the subscription);
  - `cmd/umb/pane.go`;
  - `.go-arch-lint.yml`.

  API §4, §5.31 and §6 are written on the task branch.
- **REQ:** REQ-INT-001, REQ-INT-002, REQ-INT-003, REQ-INT-005, REQ-INT-006

### [x] 2026-10-17 Ratification
- The Tech Lead ratifies decisions 1–15.
- Fold:
  - Tech Design §5.1, §5.2, §5.2b and §9.4;
  - Data Model §4;
  - the API version and history;
  - API §2's sentence on methods advertised outside their prefix;
  - the F1 tasks file: T-F1-25's **Files** widened to what the task touched (`internal/sessions`,
    `internal/workspaces`, `cmd/umbrald/integrations.go`), and decision 15's answer in T-F1-27.
- Then move the delta to `changes/_archive/`.
