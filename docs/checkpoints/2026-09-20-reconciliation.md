# CHECKPOINT — reconciliation of the two spec lineages

> 2026-09-20 · verified against the filesystem, not against the previous checkpoint

## What this fixes

`umbral-repo-en.zip` (2026-09-20 08:48) was unpacked over the working copy. Its baseline is
dated 2026-09-11 09:20-09:31, which is **before** the F0 implementation commits of the same
day. The result was a working tree that added genuinely new work and silently removed
committed work:

| Lost | Restored from |
|---|---|
| `Exec=umbral-tui` / `Terminal=true` in the `.desktop` file, its checksum and the `build.py` template — the whole of T-PKG-02 | HEAD |
| `CLAUDE.md`: the architecture guide, the boundary rules and the note that the project skills, hooks and `.mcp.json` do not exist | HEAD |
| `[x]` states in the archived delta task files (`2026-09-analyze-fixes`, `2026-09-visual-identity`) | HEAD |
| `session.unsubscribed` (API §6, §8) | `changes/_archive/2026-09-slow-client-notification` |
| `$TMPDIR/umbral-<uid>` runtime directory and its ownership rule | `changes/_archive/2026-09-api-f0-decisions` |
| `trace_id` substitute until T-F1-18; `capabilities` derived from the method table; required `protocol_version`; repeated handshake closes the connection | same |
| `abandoned` widened, plus its two state-machine edges | `changes/_archive/2026-09-block-lifecycle-decisions` |
| REQ-BLK-003 late-marker promotion and the one-way rule | same |
| `klauspost/compress/zstd` as a `sessions` dependency (Tech §3.2) | same |
| `output_truncated` covering both caps; §2.3 sequences the chunks do not keep | same |
| `block.search` ordering by insertion position; FTS5 syntax note | `changes/_archive/2026-09-block-query-performance` |
| `idx_blocks_started`, its migration `0002`, and the `VACUUM` rebuild rule | same |
| `[x]` states and result notes for T-F0-01 … T-F0-10, T-PKG-01, T-PKG-02 | HEAD |
| The 11-row execution log of F0 | HEAD |
| CHANGELOG entries for every implemented task | HEAD |
| `tools/sdd_check.py`: the `[x] YYYY-MM-DD` state parser, the constitution-article exception, the FTS `MATCH` assertion | HEAD |

Kept from the 2026-09-20 lineage: the orchestration surface (ADR-0002), the two Art. 5
amendments, signing and the trust store, wait monitoring, the hardening tasks file, the
Analyze reports of 2026-09-20 and 20b, and the structure-table check in `sdd_check.py`
(rewritten to test migration 0003 rather than 0001).

## Versions after the merge

| Artifact | Before (HEAD) | Before (working tree) | After |
|---|---|---|---|
| Constitution | 1.0 | 1.1 | **1.1** |
| PRD | 1.3 | 1.2 | **1.5** |
| API Spec | 1.4 | 1.2 | **1.6** |
| Technical Design | 1.3 | 1.2 | **1.5** |
| Data Model | 1.3 | 1.2 | **1.5** |
| Plan | 1.1 | 1.2 | **1.4** |

Each history table now carries both lineages in date order: the 2026-09-11 delta rows and the
two 2026-09-20 rows, renumbered above them.

## Migrations

The orchestration surface put `workspaces`, `tabs`, `panes` and `pane_aliases` in migration
`0001`, which is already applied and which Art. 6 forbids editing. Resolved in
`changes/2026-09-structure-migration/` (pending ratification):

| Migration | Phase | State |
|---|---|---|
| `0001_terminal.sql` | F0 | written, applied |
| `0002_block_index.sql` | F0 | written, applied |
| `0003_structure.sql` | F0 | not written — T-F0-14 |
| `0004_agent.sql` | F1 | not written — T-F1-01 |

## Implementation, verified on disk

| Task | State | Evidence |
|---|---|---|
| T-F0-01 … T-F0-10 | done | `internal/{store,api,bus,sessions}/**`, 11 rows in the F0 execution log |
| T-PKG-01, T-PKG-02 | done | `scripts/icons_check.sh`, `icons` job in `.github/workflows/ci.yml` |
| T-F0-11 | not started | `cmd/umb/main.go` is a 19-line stub that exits 69 |
| T-F0-12 | not started | `internal/tui/doc.go` is a 5-line package comment |
| T-F0-13 | not started | no `.github/workflows/perf.yml` |
| T-F0-14 … T-F0-18 | not started | no `internal/workspaces/` |
| T-F1-01 … T-F1-31 | not started | — |
| T-PKG-03 | not started | no `packaging/` |

## Gate

| Check | Result |
|---|---|
| `python3 tools/sdd_check.py` | **exit 0** · 112 REQs (106 MUST) · 106/106 with a task · DDL OK (26 objects) · migration 0001 in isolation OK (INSERT + FTS MATCH) · migration 0003 over 0001 OK |
| `node tools/mermaid_check.mjs` | 15/15 diagrams valid |
| `task ci` (specs, lint, arch, arch:selftest, test, build) | **exit 0** |
| `task test` (`go test -race ./...`) | green, 6 consecutive runs of `internal/api` |
| `task lint` | gofumpt, go vet, golangci-lint 0 issues, govulncheck clean, gitleaks no leaks |
| `task arch` | OK, no warnings |
| `bash scripts/icons_check.sh` | OK — checksums, byte-for-byte regeneration and `.desktop` validation |

## Fixed while here

**A real defect in T-F0-06, found because a test was flaky rather than wrong.**
`session.subscribe` registered the subscription, took the snapshot, and then called
`subscribe` a second time with the snapshot's sequence number. That second call closes the
existing subscription and creates a new one, which discards everything queued in between —
exactly the window the early registration exists to protect (REQ-TERM-004, API Spec §5.11).
Output produced during the snapshot survived only when the bus happened to deliver it after
the replacement, which is why `TestSubscribeSnapshotBeforeLive_REQ_TERM_004` failed about one
run in three under `-race` instead of always. The handler now calls `rebase` on the existing
subscription, which drops the prefix the snapshot already shows and keeps the rest; chunks
carry their sequence number into the queue so that prefix can be identified.
`TestRebaseKeepsWhatTheSnapshotDoesNotContain_REQ_TERM_004` pins it deterministically, and was
checked for teeth: making `rebase` drop the whole queue, the old behaviour, fails it. Six
consecutive `-race` runs of `internal/api` are green.

`TestNoChunkWaitsLongerThanTheBatchInterval` failed on roughly two runs in three under
`-race`, which left `task test` red and Art. 2 unmet. It timed one socket round-trip against
the 4 ms batch ceiling, and the race detector's overhead alone is the same order as the
quantity being measured, so a single sample could not separate signal from noise. It now takes
the median of 25 lone chunks. Teeth confirmed: adding `time.Sleep(BatchInterval)` to the
subscription loop makes it fail at a 4.48 ms median, and the unmodified daemon passes four
runs out of four.

## Raised by this work

- **C-05 (HIGH)**: REQ-WS-002's `w<n>`, `w<n>:t<m>`, `w<n>:p<m>` identifiers contradict Art. 6,
  which requires type-prefixed ULIDs, while API §3 and the Tech Design's Constitution check both
  still assert the rule. The DDL enforces `CHECK (id LIKE 'w%')` on `workspaces` and nothing on
  `tabs.id` or `panes.id`. It arrived with the orchestration surface rather than with this merge,
  but the merge is what put the two statements in one set of specs. **It needs a decision before
  T-F0-14** — amend Art. 6 with a written exception, or keep ULIDs internally and treat `w1:t1`
  as a display alias. Nothing else is blocked.

## Open, unchanged by this work

- **C-01** (MEDIUM): what the daemon does when SQLite cannot write under persist-first
  (DD-007). Blocks T-F1-13.
- **C-02** (MEDIUM): no runbook for custody of the rule-signing private key. Blocks T-F1-30.
- `umbral-repo-en.zip` is still in the working tree, untracked. It is the source of the
  regression and nothing references it.
