# Tasks — delta `2026-10-display-metadata`

### [ ] T-F1-26 · Display metadata and tokens
- **What:** decisions 1–9 in the following places:
  - `internal/integrations` (`domain/metadata.go`, `metadata.go`, `adapters/reportstore/metadata.go`);
  - `internal/workspaces` (`domain/metadata.go`, the tree store's read);
  - `internal/api/panes.go`.

  API §4, §5.32 and §6 are written on the task branch.
- **REQ:** REQ-INT-004

### [ ] Ratification
- The Tech Lead ratifies decisions 1–9.
- Fold:
  - Data Model §2.4c;
  - Tech Design §5.2;
  - the API version and history;
  - the F1 tasks file, with the **Files** widened to the workspaces read and the API handler.
- Then move the delta to `changes/_archive/`.
