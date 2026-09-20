# Tasks — delta `2026-09-tui-renderer`

### [x] 2026-09-20 T-VT-01 · Give the TUI's renderer a place in the boundary rules
- **What:** Tech Design §5.1 and §5.2, the DD-001 consequence, and the `tui-ports` and
  `tui-adapters` components in `.go-arch-lint.yml`.
- **REQ:** Art. 3
- **Files:** `specs/technical/umbral-architecture.md`, `.go-arch-lint.yml`
- **Done:** `task arch` green with the new packages present; an import from `internal/tui`
  into `internal/sessions/adapters/ghostty` is rejected.
