# Changelog

Format based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). The project uses
[SemVer](https://semver.org/) from 0.1.0 onward.

## [Unreleased]

### Added
- **T-PKG-01 and T-PKG-02**: `scripts/icons_check.sh` and the `icons` CI job, which verify the branding kit's checksums, regenerate it from source and compare byte for byte, then validate the `.desktop` file. The launcher now targets `umbral-tui` with `Terminal=true`, since release 0.1 ships no desktop client.
- **T-F0-03**: `internal/bus`, a typed pub/sub that drops the oldest event under pressure instead of blocking a publisher, and `internal/api`, the JSON-RPC 2.0 server on a 0600 Unix socket with per-installation token, `system.hello`, `system.status` and the §5.4 error translation. Identifiers are type-prefixed ULIDs from `store.NewID`.
- **T-F0-02**: `internal/store`, with migration 0001 of the terminal subdomain, the Data Model §5 pragmas verified after connecting, embedded forward-only migrations and the restart recovery of §6. The daemon runs all of it at startup.
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
- API Spec v1.2: the runtime-directory fallback and its ownership rule, `trace_id` before tracing exists, `capabilities` derived from the method table, a required `protocol_version`, and a repeated handshake closing the connection.
- The visual identity delta is folded: PRD §6.10 keeps the four requirements release 0.1 can satisfy, and the four that describe the F2 desktop client moved to `specs/prd/umbral-f2-desktop.md` (Analyze finding A-12).
- Migration 0001 now creates `threads`, so `sessions` and `blocks` accept inserts with `foreign_keys=ON` (finding A-01).
- The three `blocks_fts` synchronisation triggers are specified instead of described in a comment (A-07).
- API: `Session` gains `owner_thread_id`; `mcp.server.add` takes `env_refs` instead of `env_keyring_refs`.
- `tools/sdd_check.py` simulates migration 0001 with `threads`, asserts an FTS `MATCH`, and accepts a constitution article as the citation of an infrastructure task.
- The delta fixing Analyze findings A-01…A-07 is folded and archived in `changes/_archive/`.
- Repository setup: spec CI, issue/PR templates, Claude Code configuration (skills, subagents, hooks), GitHub bootstrap script.
