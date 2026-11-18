# CHECKPOINT — repository review

> 2026-11-17 · scope: branches, commits, SDD artifacts, phase status and a security review ·
> verified against the local clone (`develop` at `bf9092f`, refs as last fetched), not against notes.

## How this was checked

- Read-only, on the local clone; no `git fetch`, so anything pushed after the last fetch is not here.
- Phase status comes from the `[x]` / `[ ]` marks in `specs/tasks/*.md`, the plan and the PRDs.
- The security part is a targeted static review, not a line-by-line audit of the ~134 000 lines
  of Go (669 files, 309 of them tests).
- **Not run:** `task ci`, `govulncheck`, `gitleaks` and the tests. The review environment had no
  Go toolchain. Every statement about CI being green is the tasks files', not this review's.

## Repository and branches

- **History:** 419 commits by one author, 2026-09-11 to 2026-10-31; 414 on `develop`; pull
  requests up to #77. No tags. No commit since 2026-10-31.
- **`develop`** holds everything. All 89 local topic branches (`feat/T-*`, `fix/*`,
  `docs/ratify-*`, `chore/*`) are merged into it and can be deleted.
- **`main`:** `origin/main` is at PR #30 (`b921a1b`, 2026-10-22, F1's close), 172 commits behind
  `develop`. The local `main` is still at the initial commit.
- **Local leftovers:**
  - a worktree marked prunable at `../umbral-usf2` (`feat/T-F2-23-usf2-002`);
  - five `docs/ratify-2026-10-*` branches with no remote;
  - `docs/integration-prompts.md` and `.claude/settings.local.json` untracked.

## SDD artifacts

The package is complete: constitution, the MVP and F2 PRDs, API, Technical Design, Data Model,
plan, four tasks files, four Analyze reports, two ADRs and 26 checkpoints before this one.
`changes/_archive/` holds 87 deltas; 16 are open in `changes/`.

Drift to fix:

- `specs/README.md` says thirty-nine deltas are archived and none is pending, and names Plan 1.10.
  The plan is 1.15 and its status is still `DRAFT`; its metadata does not list
  `specs/tasks/umbral-f2-tasks.md`.
- `README.md` still says F0 is done, F1 is next and F2 is a horizon.
- `CHANGELOG.md` was last changed on 2026-09-21.

## Phases implemented

| Phase | Content | Tasks | Status |
|---|---|---|---|
| Phase 0 | folding the deltas and the visual identity | 7/7 | closed 2026-09-11 |
| F0 Core | daemon, PTY, libghostty, blocks, search, workspaces, restore, TUI, `umb` | 27/27 | closed 2026-09-26 |
| F1 Agentic | permissioned agent, model gateway, MCP, skills, redaction, waits, OTel, signed rules | 38/38 | closed 2026-10-22 (US-003: 16/20 offline, 0 egress) |
| F2a Orchestration | worktrees, parallel turns, ACP client, router, `full-auto` | 13/13 | closed 2026-10-24 |
| F2b Agent in the terminal | `terminal_interact`, embedded yzma provider, Active AI | 4/4 | code done; exit criterion not met |
| F2c Desktop | Wails v3 client, per-hunk review, icons | 3/4 | T-PKG-05 and the QA walk open |
| F2d Durability and remote | live PTY handoff, reconnect, remote daemons over SSH | 3/3 | tasks done 2026-10-31 |
| F2e Windows | ConPTY, named pipe, Credential Manager, PowerShell | 1/1 | done 2026-10-31; no handoff or remote hosts on Windows |
| F2f Extensibility | declarative plugins by digest, pane graphics | 2/2 | done 2026-10-31 |
| Hardening | `.deb`, tar.gz, systemd, launchd, T-HRD-02 … 12 | 16/19 | open |

No checkpoint or delta closes slices F2b … F2f the way `2026-10-f2a-close` closed F2a.

## What is missing

| Item | Kind | Detail |
|---|---|---|
| T-HRD-01 | security, the Tech Lead's | create the rule-signing key; `trustseed/seed.json` is empty |
| T-REL-02 | human walk | walk `docs/qa/f1-tui.md`; goes before T-REL-01 |
| T-REL-01 | human walk | a week of the TUI as the main terminal |
| T-PKG-05 | code, needs a Mac | macOS `.icon` and `Assets.car` |
| F2b exit criterion | the Tech Lead's decision | US-F2-002 measured 0/10 (psql 10/10, `git rebase -i` 0/10, every failure the model's use of vim); the threshold is 70 % |
| F2c QA walk | human walk | `docs/qa/f2-desktop.md` records only the app-id step of section 1; the icon and screenshot steps are not walked |
| 16 open deltas | the Tech Lead's review | 15 approved by the implementing agent, 12 of them marked "pending the Tech Lead's review"; one (`2026-10-wails-icons`) approved by the Tech Lead; none archived |
| `code-audit` gate | release | the plan requires it (SAST, SCA, secrets); no record of a run |
| Installation guide | docs | `docs/user/` holds providers and data retention only |
| Promotion to `main`, tag 0.1 | release | there is no release workflow |
| F3 | no PRD | background agents, MCP/ACP server, WASM plugins, workflows, semantic index |

## Security

### Controls read in the code

- The socket is `0600`, the token is compared in constant time (`internal/api/conn.go`) and the
  peer is read from the kernel (`internal/api/peer_*.go`).
- `fetch_url` refuses private, loopback, link-local and metadata addresses at dial time, and
  checks redirects (`internal/tools/adapters/builtin/fetch.go`).
- `[otel] endpoint` accepts loopback only (`internal/config/settings.go`).
- `ssh` is run without a shell, with `BatchMode=yes` and `--` before the destination.
- SQL is parameterised; the concatenations found join fixed column lists.
- Skills and plugins are installed by digest with normalised modes.
- The desktop page has a strict Content-Security-Policy and no `innerHTML` or `eval`.
- CI runs gosec, govulncheck and gitleaks over the history; `install_llama.sh` checks a pinned
  SHA-256 and `build_libghostty.sh` fetches a pinned commit.

No exploitable flaw was found in the code that was read.

### Findings

| # | Severity | Finding |
|---|---|---|
| 1 | High | The trust seed is empty (T-HRD-01): there is no key a signed rule bundle can be checked against. |
| 2 | High | 15 F2 deltas were approved by the implementing agent and have not had the Tech Lead's review, among them the widest surfaces: the Windows port, remote hosts, the PTY handoff, plugins and `terminal_interact`. |
| 3 | Medium | `.claude/settings.local.json` allows `gh pr merge` and `gh pr review`: an agent can approve and merge its own pull requests. It compounds finding 2. |
| 4 | Medium | Hosted ACP agents run without a sandbox and `full-auto` does not confine: a command can `cd` or write outside the worktree. Documented as a residual (Q-F2-08, Tech Design §5.3), and still the product's largest real risk. |
| 5 | Medium | GitHub Actions are pinned by mutable tag, third-party ones included (`mlugg/setup-zig`, `golangci-lint-action`, `gitleaks-action`); tools are installed with `@latest` in CI and in the Taskfile. |
| 6 | Medium | No Dependabot or Renovate; the last dependency change is Go 1.27.3. |
| 7 | Medium | The `.deb` and tar.gz archives carry no signature, published checksums or SBOM. |
| 8 | Low | `SECURITY.md` supports `main` only, which is 172 commits behind `develop`. |
| 9 | Low | A hosted agent's own traffic is not audited (`egress_visibility = unobserved`); OpenCode's provider package still comes from npm. Disclosed by REQ-ACP-007. |

## Proposed improvements and features

- **A sandbox for hosted agents and `full-auto`:** bubblewrap or landlock on Linux, seatbelt on
  macOS. The change that removes the most risk.
- **Editing without vim for the agent:** a non-interactive `GIT_SEQUENCE_EDITOR` or a structured
  edit tool would unblock US-F2-002 without changing the model.
- **A release workflow:** reproducible build, checksums, cosign signature and SBOM, started by a tag.
- **A harder CI:** actions pinned by SHA, tool versions pinned, Dependabot.
- **Fuzzing:** the shell parser in front of the destructive patterns, the JSON-RPC frames and the
  VT emulator.
- **Windows parity:** the live handoff and remote hosts.
- **A memory gate:** the 80 MiB idle budget was measured (33.8 MiB) and has no gate in CI.
- **The integrations in `docs/integration-prompts.md`:** OTel GenAI attributes, ACP/A2A with
  prismal, JSON-RPC to reflex-xtermjs and a shared MCP host.
- **The F3 PRD:** MCP/ACP server, background agents, WASM plugins, the semantic index and opening
  a pull request from a worktree.

## What is next

1. The Tech Lead's review of the 16 open deltas, then their archive (findings 2 and 3).
2. T-HRD-01, then T-REL-02 and T-REL-01.
3. The decision on US-F2-002 and the close of slices F2b … F2f.
4. Deltas for findings 5 and 7, and the `code-audit` gate, before promoting `develop` to `main`.

## Follow-up, same day — what was done about the nine findings

Written in the working tree, **uncommitted**, for the Tech Lead to run `task ci` on and commit.
None of it was run through the gate here: the review environment has no Go toolchain.

| # | Finding | State | What exists now |
|---|---|---|---|
| 1 | Empty trust seed | the Tech Lead's | The steps are in `docs/checkpoints/2026-11-17-delta-review-guide.md`; the key is created offline and no agent can stand in |
| 2 | 15 deltas without the Tech Lead's review | the Tech Lead's | The same guide: a reading order and the points to look at in each delta |
| 3 | An agent allowed to approve and merge | fixed locally | `.claude/settings.local.json` no longer allows `gh pr merge` or `gh pr review`. The lasting control is branch protection, a repository setting |
| 4 | No sandbox | proposed | Delta `2026-11-agent-sandbox` (`PROPOSED`), with a spike first. No code |
| 5 | Unpinned actions and tools | fixed | Every action pinned by SHA; the four Go tools pinned in `ci.yml` and `Taskfile.yml` (go-arch-lint at v1.19.1: v1.19.2, published the same day, fails `task arch` on `cmd/umbrald/activeai.go`); `scripts/tool_versions_check.sh` in `task specs` and CI's specs job |
| 6 | No dependency updates | fixed | `.github/dependabot.yml`, against `develop` |
| 7 | Unsigned packages | written, never run | `.github/workflows/release.yml`: a `v*` tag builds a draft release with SHA256SUMS, an SBOM and Sigstore attestations. Delta `2026-11-signed-releases` (`PROPOSED`) |
| 8 | `SECURITY.md` supports a stale `main` | fixed | It names `develop` and, from 0.1, the latest release |
| 9 | A hosted agent's unobserved traffic | proposed | Folded into `2026-11-agent-sandbox` (decision 8, Q1) |

Checked here: the three workflows, the Taskfile and `dependabot.yml` parse as YAML; every
`uses:` carries a 40-digit SHA resolved from the action's repository on 2026-11-17;
`tool_versions_check.sh` passes and rejects an injected mismatch; `python3 tools/sdd_check.py`
still reports 178/178 with the two proposed deltas present. `changes/` now holds 18 open deltas.

## Follow-up, 2026-11-18 — what landed

The working-tree changes above were committed, run through `task ci` and merged into `develop`.

| # | Finding | State on 2026-11-18 |
|---|---|---|
| 1 | Empty trust seed | The Tech Lead created `umbral-rules-2026a`; pull request #83 seeds the trust store with its public key. T-HRD-01 stays open until a bundle signed with it is accepted by a daemon |
| 2 | 15 deltas without the Tech Lead's review | Unchanged; `2026-11-17-delta-review-guide.md` is the reading order |
| 3 | An agent allowed to approve and merge | Unchanged: branch protection is a repository setting and is not set |
| 4, 9 | No sandbox; unobserved traffic | Delta `2026-11-agent-sandbox`, still `PROPOSED`; its tasks are now T-HRD-14 … 17 |
| 5, 6, 8 | Unpinned actions and tools; no dependency updates; `SECURITY.md` | Merged in pull request #79 |
| 7 | Unsigned packages | Delta `2026-11-signed-releases` merged as `PROPOSED` with #79; `release.yml` is in a pull request of its own and has never run |

The same day's pull requests also fixed three things this review did not see, because it ran
no gate: GO-2026-6865 in `klauspost/compress` (#78), a race in the handoff's refusal (#80) and
a daemon that panicked at start without `libffi.so.8`, which failed the Debian job on every
branch (#81, T-PKG-08). Eight tests that fail on a runner and pass on a rerun are T-HRD-13
(#82).
