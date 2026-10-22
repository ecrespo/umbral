# Tasks — delta `2026-10-attention-focus`

### [x] 2026-10-17 T-F1-24 · Attention state and thread resume
- **What:** decisions 1–11 in the following places:
  - `internal/agents/attention.go`, `runtime.go`, `turn.go` and `approvals.go`;
  - the thread store's `FinishTurn` and `MarkSeen`;
  - `internal/api` (`thread.focus`, `thread.attention_changed`, the snapshot);
  - `internal/waits`;
  - the TUI's agent panel.

  API §5.3, §5.23, §5.29 and §6 are written on the task branch.
- **REQ:** REQ-AGT-016, REQ-AGT-017

### [x] 2026-10-17 Ratification
- The Tech Lead ratifies decisions 1–11.
- Fold: Data Model §2.5 (`seen_at`), Tech Design §5.1, the API version and history, and the F1
  tasks file.
- Then move the delta to `changes/_archive/`.
