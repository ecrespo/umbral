# Tasks — delta `2026-09-context-budget`

### [x] 2026-09-27 T-F1-12 · Token budget and compaction
- **What:** decisions 1, 2, 4, 5 (the replacement) and 7 in `internal/context/domain/budget.go`, each pinned by a test.
- **REQ:** REQ-CTX-004

### [ ] T-F1-13 · Agent runtime
- **What:** decisions 3, 5 (persisting the summary), 6 and 8: calls `Compact` before every model
  call against the smallest known window, with the `fast` class as summarizer, and persists the
  summary before publishing `context.compacted` (`TestCompactedEventPersistedThenPublished_REQ_CTX_004`).
- **REQ:** REQ-CTX-004

### [ ] Ratification
- The Tech Lead ratifies or amends the delta; on ratification it moves to `changes/_archive/`.
