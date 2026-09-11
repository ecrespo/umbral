# Changelog

Format based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). The project uses
[SemVer](https://semver.org/) from 0.1.0 onward.

## [Unreleased]

### Added
- **T-F0-10**: `block.list`, `block.get` and `block.search`, with cursor pagination and FTS5 snippets. `block.get` accepts the reserved id `last`, which is what `umb block last` asks for. Over 100,000 blocks a search takes 0.93 ms and a list page 0.22 ms at p95, against the 200 ms REQ-BLK-006 budgets.
- **T-F0-09**: the block lifecycle. The daemon now turns a shell's OSC 133, 633 and 7 sequences into blocks: a command, its working directory, its exit code and its duration, with the output stored twice, as a plain-text transcript for the agent and FTS5 and as zstd-compressed chunks for faithful re-rendering. Alternate-screen content is excluded from both. `block.started`, `block.updated`, `block.closed` and `session.integration` are published, and a session with no shell integration is marked `none` after five seconds and keeps delivering output.
- **T-F0-07**: the VT conformance suite, 22 cases from Tech Design §8.1 with their fixtures under `testdata/vt/`. A missing fixture or a deleted MUST case fails the run rather than shrinking the suite.
- **T-F0-06**: `session.subscribe` and `session.unsubscribe`, with the screen delivered before the first live chunk, per-subscription batching and an 8 MiB budget past which the subscription is dropped and announced. The daemon adds 12 µs at p95 between a PTY chunk and the notification.
- **T-F0-05**: the sessions module. PTY sessions with their own libghostty emulator, the `session.create`, `list`, `input`, `resize` and `close` methods, the per-session input lock, and the `session.exited`, `session.resized` and `session.input_owner` notifications. `umbrald` now owns real terminals.
- **T-F0-08**: shell-integration bootstrap scripts for bash, zsh and fish under `shell/`, injected by `internal/sessions/adapters/shellinteg`. They emit the OSC 133, 633 and 7 sequences the block lifecycle derives from, without dropping the user's own configuration or breaking a custom prompt.
- **T-F0-04**: `internal/sessions/adapters/ghostty`, the replayable VT snapshot behind `session.subscribe`, with eight golden round-trip cases. `scripts/build_libghostty.sh` and `task deps:ghostty` build the cgo dependency with Zig. The Q-01 decision is recorded in `docs/spikes/q01-snapshot.md`.
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

### Fixed
- `block.list` was a full table scan and a sort of the whole history, 141 ms at 100,000 blocks, because the three indexes on `blocks` all begin with a column an unfiltered page does not name. It now has an index on the ordering it is listed by.
- `block.search` sorted its entire match set to return one page, 1.23 seconds at 100,000 blocks against a 200 ms budget. It now orders by insertion position, which lets SQLite walk the full-text index backwards and stop at the limit.
- The bash and zsh integration reported exit code 0 for every command on any machine with a prompt framework installed. Both read `$?` from a hook registered last, and each element of `PROMPT_COMMAND` and of zsh's `precmd_functions` leaves `$?` set to its own result. The capture is now a separate hook registered first.
- The daemon never answered a program's query to the terminal, because libghostty's write-pty effect was never wired. Every fish session stalled for two seconds waiting for a Primary Device Attributes reply and then permanently disabled features.

### Changed
- API Spec v1.4, Data Model v1.3, PRD v1.3 and Tech Design v1.3 fold the three deltas raised during F0. `session.unsubscribed` tells a client its subscription was dropped, `abandoned` now covers a block superseded without its end marker, `output_truncated` covers the plain-text cap as well as the raw one, a shell that announces itself after the five-second window is promoted rather than left marked as having no integration, and the zstd dependency is recorded. Data Model §2.4 now warns that `VACUUM` renumbers the rowids the full-text index is keyed on, and must be followed by a rebuild.
- API Spec v1.2: the runtime-directory fallback and its ownership rule, `trace_id` before tracing exists, `capabilities` derived from the method table, a required `protocol_version`, and a repeated handshake closing the connection.
- The visual identity delta is folded: PRD §6.10 keeps the four requirements release 0.1 can satisfy, and the four that describe the F2 desktop client moved to `specs/prd/umbral-f2-desktop.md` (Analyze finding A-12).
- Migration 0001 now creates `threads`, so `sessions` and `blocks` accept inserts with `foreign_keys=ON` (finding A-01).
- The three `blocks_fts` synchronisation triggers are specified instead of described in a comment (A-07).
- API: `Session` gains `owner_thread_id`; `mcp.server.add` takes `env_refs` instead of `env_keyring_refs`.
- `tools/sdd_check.py` simulates migration 0001 with `threads`, asserts an FTS `MATCH`, and accepts a constitution article as the citation of an infrastructure task.
- The delta fixing Analyze findings A-01…A-07 is folded and archived in `changes/_archive/`.
- Repository setup: spec CI, issue/PR templates, Claude Code configuration (skills, subagents, hooks), GitHub bootstrap script.
