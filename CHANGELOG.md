# Changelog

Format based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). The project uses
[SemVer](https://semver.org/) from 0.1.0 onward.

## [Unreleased]

### Added
- **T-F0-01**: Go module `github.com/ecrespo/umbral`, the Tech Design §5.1 package skeleton and the three `cmd/` binaries.
- Quality gate: `Taskfile.yml` (`task lint`, `task arch`, `task test`, `task specs`, `task ci`), `.golangci.yml` with gosec, `.pre-commit-config.yaml` and the GitHub Actions pipeline.
- `.go-arch-lint.yml` encoding the Tech Design §5.2 dependency rules, plus `scripts/arch_selftest.sh`, which proves the rules reject a `sessions` → `agents` import instead of merely being present.
- Research and conceptual architecture (`docs/ARCHITECTURE.md`), ADR-0001.
- Architecture infographic (`docs/diagrams/umbral-architecture.excalidraw` + PNG/SVG render).
- SDD artifacts: constitution, PRD with EARS, JSON-RPC API v1, technical design, data model, plan, F0/F1 tasks and Analyze.
- Visual identity Delta Spec and icon kit (Linux, Windows, macOS).
- REQ-SEC-008 (degraded start when the OS keyring is unavailable) and REQ-AGT-015 (`thread.send` idempotency through `client_msg_id`).
- Tech Design appendix §8.1 with the closed VT conformance case list (VT-01…VT-22).
- Data Model §5.1: what each migration creates.

### Changed
- Migration 0001 now creates `threads`, so `sessions` and `blocks` accept inserts with `foreign_keys=ON` (finding A-01).
- The three `blocks_fts` synchronisation triggers are specified instead of described in a comment (A-07).
- API: `Session` gains `owner_thread_id`; `mcp.server.add` takes `env_refs` instead of `env_keyring_refs`.
- `tools/sdd_check.py` simulates migration 0001 with `threads`, asserts an FTS `MATCH`, and accepts a constitution article as the citation of an infrastructure task.
- The delta fixing Analyze findings A-01…A-07 is folded and archived in `changes/_archive/`.
- Repository setup: spec CI, issue/PR templates, Claude Code configuration (skills, subagents, hooks), GitHub bootstrap script.
