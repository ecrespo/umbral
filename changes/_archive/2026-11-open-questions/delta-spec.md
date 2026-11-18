# Delta — answers to the open questions of three November deltas

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-11-18 by the Tech Lead, in advance` ("apruebo lo que propongas de los deltas Q1-Q5, Q1-Q4. Apruebo y resuelve el 2026-11-deb-libffi con sus Q1-Q3."); the answers R1 to R5, S1 to S4 and L1 to L3 are the implementing agent's proposals and the text is pending his review. What gates release 0.1 is **not** among the questions he approved and stays proposed (below). Folded into the hardening tasks and Tech 1.95 §9.3, and archived |
| **Date** | 2026-11-18 |
| **Tasks** | T-REL-03, T-HRD-14, T-HRD-15, T-HRD-17 (their open dependencies answered); T-HRD-18, T-HRD-19, T-HRD-20 (added) |
| **REQ** | none changed. The `SBX` requirements stay T-HRD-15's to write |
| **Raised by** | The questions deltas `2026-11-signed-releases`, `2026-11-agent-sandbox` and `2026-11-deb-libffi` left for the Tech Lead |

## Why one delta

Three deltas ratified or approved in November left twelve questions open. The Tech Lead
approved the implementing agent's proposal for each in advance, as T-HRD-02's eleven were
answered by `2026-10-hrd02-open-questions`. One answer rests on something read on 2026-11-18
and not known when its question was written (L2, L3).

## `2026-11-signed-releases`

- **R1 — the macOS archive (Q1).** Attested, not notarised, in 0.1. Nothing changes:
  `2026-10-service-units` decision 5 already says so, and notarisation needs an Apple
  Developer ID the project does not have.
- **R2 — attestations on a private repository (Q2).** The repository is public
  (`gh repo view`, 2026-11-18), so the assumption holds. If it is made private before a tag,
  the release job fails at its attestation step and says why; nothing is published.
- **R3 — the desktop client and Windows (Q3).** Not in release 0.1. Each joins the release
  workflow in the task that gives it a package.
- **R4 — branch protection (Q4).** `develop` and `main` should require a pull request and
  green checks, and `v*` tags should be restricted to the Tech Lead. It is a repository
  setting and the Tech Lead's to switch on, **after T-HRD-13**: on 2026-11-18 five of the
  day's pull requests had a test job red on a test their change did not touch — #79, #81, #82
  and #83, and #80 on its first attempt (run 44213787204, the Windows job) — so required
  checks would have blocked each of them.
- **R5 — go-arch-lint v1.19.2 (Q5).** The pin stays at v1.19.1. Moving it is T-HRD-19, which
  first decides whether the notice is right.

**Proposed, and not approved: what gates release 0.1.** Neither delta said, and it is none
of the twelve questions the Tech Lead approved. The proposal is that T-REL-03 gates 0.1 — a
release is what the workflow makes, so 0.1 would not be tagged before a release candidate has
gone through it — and that the sandbox tasks do not: PRD F2 Q-F2-08 calls the sandbox "a
later hardening", and the disclosures of REQ-ACP-007 stand until it exists. Whether T-HRD-18
gates 0.1, which ships a macOS archive, is open too. All three are the Tech Lead's.

## `2026-11-agent-sandbox`

- **S1 — the network profile (Q1).** Option (c): T-HRD-15 confines files only and leaves the
  network open, and the thread's disclosure says so. Option (b), no network and a daemon-side
  relay for the model endpoint, is T-HRD-20, sized from what the spike measures.
- **S2 — `full-auto` without a sandbox (Q2).** Allowed, degraded and disclosed: REQ-ORC-004
  is not amended to refuse it. Revisited once both adapters exist.
- **S3 — the user's shell inside the sandbox (Q3).** The profile's write list is fixed from
  what T-HRD-14 measures for bash, zsh and fish under oh-my-zsh and starship; a framework
  that needs a path gets it through `[sandbox] write`, never through a wider default.
- **S4 — an opt-out (Q4).** `[sandbox] mode = "off"`, never the default, disclosed on every
  thread it affects, for hosted agents and `full-auto` shells alike.

## `2026-11-deb-libffi`

The delta is ratified as written and archived with this one.

- **L1 — should a daemon with no embedded provider need libffi (Q1)?** Not changed now. The
  load is in the `init` of `github.com/jupiterrider/ffi`, a dependency of yzma; avoiding it
  needs a build without yzma, which would make two daemons to test and ship, or a change
  upstream. Decision 1 of the delta stands.
- **L2 — Windows (Q2).** No panic on `windows/amd64`, the port's only target (REQ-WIN-001):
  the module embeds `libffi-8.dll` there (`embed_windows_amd64.go`) and its `init` writes it
  to `os.UserCacheDir()/github.com/jupiterrider/ffi/libffi/3.8.0/` and loads it from that
  path. Read in the module's source at v0.8.0; CI's `windows` job starts `umbrald.exe` in
  `scripts/windows_smoke.sh` and passes. on `windows/arm64` the copy is embedded and never extracted; it is not a target.
- **L3 — macOS (Q3).** The same: `libffi.8.dylib` is embedded for `darwin/amd64` and
  `darwin/arm64` and extracted to the user's cache directory. Not run on a clean machine; the
  `archives` job runs the archive's `umbrald` on `macos-latest`.
- **What L2 and L3 found, and neither question asked.** On macOS and Windows the daemon loads
  a library from a directory its user can write. When the file already exists the module
  loads it as it is, without comparing it with the embedded bytes (`embed.go`). A process of
  the same user that writes that file runs code inside `umbrald` at its next start — and a
  `full-auto` command or a hosted agent is such a process. Linux is not affected: it loads
  the system's `libffi.so.8`. It is one more way for such a process to persist, not a
  boundary that holds today: the same process can already write the user's rc files. Two more
  paths in the same code: when the cache directory cannot be written, or `FFI_NO_EMBED=1` is
  set, the library is loaded by its bare name through the system's search order; and the
  write is not atomic, so an interrupted first start can leave a truncated file that is then
  taken as it is. This is **T-HRD-18**, and the sandbox profile of T-HRD-15 and T-HRD-16 must
  leave that directory out of every write list.

The status lines of the two archived deltas point here; their text is otherwise as ratified.

## Tasks

- **T-HRD-18 · The daemon loads libffi from a path its user can write (macOS, Windows).**
- **T-HRD-19 · go-arch-lint v1.19.2's notice on `cmd/umbrald/activeai.go`.**
- **T-HRD-20 · A hosted agent's model traffic through a daemon-side relay.**

## Constitution check

- **Art. 5:** `full-auto` "SHALL only run inside a sandbox or a disposable worktree"; T-HRD-18
  is a way out of the worktree that lasts past the thread. It is recorded as a task and not
  fixed here.
- **Art. 9:** no requirement changes; Tech §9.3 says where each OS's libffi comes from.
