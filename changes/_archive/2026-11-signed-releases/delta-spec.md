# Delta — pinned CI, dependency updates and signed releases

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-11-18 by the Tech Lead ("Si, ratifico el PR 85, ratifico el 84." — PR #85 carried decision 4's workflow — and, asked whether that covered the deltas themselves, "Si, cubre también los deltas.")`, as written; folded into PRD 1.24 (REQ-PKG-009), Tech 1.94 §9.3 and the hardening tasks (T-REL-03), and archived. Q1 to Q5 stay open with their recommendations |
| **Date** | 2026-11-17 |
| **Tasks** | T-REL-03 (the release workflow's first run); decisions 1 to 3 are infrastructure under Art. 1 and need no task |
| **REQ** | REQ-PKG-009, added by decision 4 |
| **Folded into** | PRD 1.24 §6.14, Tech 1.94 §9.3, `specs/tasks/umbral-hardening-tasks.md` |

## Evidence

- Every action in `ci.yml` and `perf.yml` was referenced by a mutable major tag (`@v5`, `@v2`),
  three of them third-party (`mlugg/setup-zig`, `golangci/golangci-lint-action`,
  `gitleaks/gitleaks-action`): whoever can move the tag runs code in the gate.
- `gofumpt`, `govulncheck` and `go-arch-lint` were installed with `@latest` in CI, and all four
  tools with `@latest` in `task tools:install`; `golangci-lint` alone was pinned, in CI only.
- No Dependabot or Renovate configuration existed. The last dependency change is Go 1.27.3.
- The `.deb` and the tar.gz archives are built and checked in CI and go nowhere: there is no
  release workflow, no tag, no published checksum, no signature and no SBOM. Delta
  `2026-10-service-units` decision 5 says the macOS archives are unsigned in 0.1.
- `SECURITY.md` named `main` as the only supported branch; `origin/main` is 172 commits behind
  `develop`.

## Decisions proposed

1. **Actions are pinned by commit SHA**, with the version in a trailing comment so Dependabot
   can read and bump it. Each SHA is the commit the previously used major tag pointed at on
   2026-11-17, so no action changes version in this delta.
2. **The gate's Go tools are pinned**: gofumpt v0.12.0, govulncheck v1.10.0, golangci-lint
   v2.15.0 and go-arch-lint v1.19.1 — in `ci.yml`'s env block and in `Taskfile.yml`. The first
   three are what `@latest` resolved to on 2026-11-17. go-arch-lint is one release behind on
   purpose: v1.19.2 was published that same day, and its deep scan reports
   `cmd -\-> activeai-service not allowed` for the two adapters `cmd/umbrald/activeai.go`
   injects into `activeai.New`, which v1.19.1 — the release the last green run on `develop`
   used — does not. Whether that is a finding or a regression of the tool is open (Q5). `scripts/tool_versions_check.sh` fails `task specs`
   and CI's specs job when the two differ, when a workflow step writes a version of its own,
   when an `@latest` or a `version: latest` comes back, or when any action in any workflow is
   not referenced by a 40-digit SHA; `scripts/tool_versions_selftest.sh` proves it on twelve
   mutated copies. govulncheck's database is
   fetched at run time, so pinning the binary does not age the findings.
3. **Dependabot** (`.github/dependabot.yml`) opens pull requests against `develop` for both Go
   modules, the actions, `tools/package.json` and the desktop builder's image. It does not
   propose `go.mitchellh.com/libghostty`, which moves only with `scripts/build_libghostty.sh`'s
   ghostty commit.
4. **A `v*` tag builds a signed draft release** (`.github/workflows/release.yml`). Proposed
   requirement, REQ-PKG-009 if ratified: *WHEN a release is tagged, THE SYSTEM's pipeline SHALL
   publish each package with a SHA-256 checksum, a Sigstore build-provenance attestation bound
   to the workflow and the tag, and a CycloneDX SBOM of the module graph.* Keyless: there is no
   release-signing key to keep, unlike the rule-signing key of DD-016. The release is created as
   a draft; a person publishes it.
5. **`SECURITY.md`** says where fixes land (`develop`) and that the latest tagged release is
   supported from 0.1 on.

## What is done and what is not

- Decisions 1, 2, 3 and 5 are written in the working tree and were checked as far as they can be
  without the pipeline: the workflows and the Taskfile parse as YAML, every `uses:` carries a
  40-digit SHA, and `tool_versions_check.sh` passes and its selftest rejects twelve mutations.
- **Run on 2026-11-17:** each pinned SHA was compared with its tag through the GitHub API, and
  `task ci` on `chore/pin-ci-tools` passes every stage except `govulncheck`, which reports
  GO-2026-6865 (`klauspost/compress` v1.20.0). That finding is `develop`'s own and is fixed by
  `chore/govulncheck-go1.27.4`, which in turn fails CI's architecture job without this pin: the
  two land together. **Not run:** the workflows themselves on GitHub.
- **Decision 4's workflow has never run, and is not in the change that carries decisions 1, 2,
  3 and 5** (`chore/pin-ci-tools`): `.github/workflows/release.yml` arrives in a pull request
  of its own. Its first run is proposed task T-REL-03: push a
  pre-release tag (`v0.1.0-rc.1`) from `develop`, read the draft, and check one download with
  `sha256sum -c SHA256SUMS` and `gh attestation verify`.

## Open questions for the Tech Lead

- **Q1 — the macOS archive.** The workflow builds what CI builds: `linux-amd64` (`.deb` and
  tar.gz) and `darwin-arm64` (tar.gz). The attestation proves where the archive was built; it
  does not satisfy Gatekeeper. Notarisation needs an Apple Developer ID and stays out of 0.1,
  as `2026-10-service-units` already decided.
- **Q2 — attestations on a private repository** need a paid plan; this assumes the repository
  is public when 0.1 is tagged.
- **Q3 — the desktop client and Windows** are not in the release: neither has a package yet.
- **Q4 — branch protection.** Requiring a review and green checks on `develop` and `main`, and
  restricting who can push `v*` tags, are repository settings, not files; the review's finding
  3 (an agent allowed to approve and merge its own pull requests) is closed for good only
  there. The agent's permission file on the Tech Lead's machine, `.claude/settings.local.json`, no
  longer allows `gh pr merge` or `gh pr review`; it is local and unversioned, so nothing in the
  repository shows or enforces it.
- **Q5 — go-arch-lint v1.19.2.** Its notice is about the composition root handing its own
  types to a service through the service's ports, which Tech Design §5.2 intends
  (`.go-arch-lint.yml`: "The model and the block reader are its own ports, which `cmd`
  implements"). Before bumping the pin: read the release's notes, and either move
  `localSuggester` and `blockOutput` into an adapters package or record why the notice is
  wrong. An unpinned `@latest` would have turned the gate red on every branch that day.
