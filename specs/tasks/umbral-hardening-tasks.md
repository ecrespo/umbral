# Tasks — Umbral hardening 0.1 (packaging and identity)

> Source specs: `specs/prd/umbral-mvp.md` §6.14 · `specs/plans/umbral-mvp-plan.md` (hardening phase)
> Folded from `changes/_archive/2026-09-visual-identity/`. REQ-PKG-004, REQ-PKG-005, REQ-PKG-007
> and REQ-PKG-008 live in `specs/prd/umbral-f2.md` §6.8 (finding A-12; the F2 PRD absorbed the
> holding file `umbral-f2-desktop.md` on 2026-10-22), with their tasks T-PKG-04 and T-PKG-05 in
> `umbral-f2-tasks.md`.

## Conventions for this file

Same as `umbral-f0-tasks.md`. Packaging is verified with commands rather than Go tests, which is why
its matrix lists commands.

## Tasks

### [x] 2026-09-11 T-PKG-01 · Incorporate the kit and pin checksums
- **What:**
  - the kit lives in `assets/branding/umbral-icons/`;
  - `CHECKSUMS.sha256` covers every binary of the kit;
  - CI job `icons` that rebuilds with `tools/build.py` and compares.
- **REQ:** REQ-PKG-001, REQ-PKG-006
- **Files:** `assets/branding/umbral-icons/**`, `scripts/icons_check.sh`, `.github/workflows/ci.yml`, `Taskfile.yml`
- **Depends on:** —
- **Done:** the `icons` job is green; changing one pixel of a PNG turns it red.
- **Result:** `scripts/icons_check.sh` does three things rather than one: verify the
  checksums, regenerate with `build.py` and compare, then validate the `.desktop` file. The
  regeneration step is what REQ-PKG-006 actually asks for, because checking the checksums
  alone would pass a source edit committed together with its new checksum. Verified by
  flipping one byte of the 48 px PNG: exit 1. The kit reproduces byte for byte from source,
  so 55 of the 56 artifacts were already correct; only the `.desktop` file changed, in
  T-PKG-02. The regeneration step degrades to a warning when `cairosvg` and `Pillow` are
  missing, so a contributor without them still gets the checksum and `.desktop` checks; CI
  installs both, so the full check always runs there.

### [x] 2026-09-11 T-PKG-02 · `.desktop` file for release 0.1
- **What:** set `Exec=umbral-tui` and `Terminal=true`, validate it in CI, and regenerate
  `CHECKSUMS.sha256` in the same commit because the `.desktop` file is covered by it (finding A-15).
- **REQ:** REQ-PKG-002, REQ-PKG-003
- **Files:** `assets/branding/umbral-icons/linux/share/applications/**`, `assets/branding/umbral-icons/CHECKSUMS.sha256`
- **Depends on:** T-PKG-01
- **Done:** `desktop-file-validate` without errors and the `icons` job still green after the change.
- **Result:** the change went into the `DESKTOP` template in `build.py`, not into the
  generated file, so the next regeneration keeps it. `desktop-file-validate` exits 0; it
  emits one *hint* about `Categories` listing more than one main category, which is
  pre-existing, is not an error, and is left for whoever revisits the kit's categories.
  `scripts/icons_check.sh` additionally asserts the four lines REQ-PKG-002 and REQ-PKG-003
  name, because the validator does not know which binary release 0.1 ships. Checksums
  regenerated, closing A-15.

### [x] 2026-10-24 T-PKG-03 · Installation in the `.deb`
- **What:** ship the hicolor tree and the `.desktop` file in the package; postinst triggers refresh
  `gtk-update-icon-cache` and `update-desktop-database`.
- **REQ:** REQ-PKG-001, REQ-PKG-002
- **Files:** `packaging/deb/**`. Written as `packaging/deb/build.sh`, `postinst`, `postrm`,
  `check.sh`, `check_selftest.sh` and `smoke.sh`; `task deb` and `task deb:smoke` in
  `Taskfile.yml`; the `deb` job in `.github/workflows/ci.yml`
- **Depends on:** T-PKG-02
- **Done:** `dpkg -c umbral_*.deb` lists the 11 icon files and the `.desktop` file; smoke test in an
  Ubuntu 26.04 container.

### [x] 2026-10-31 T-PKG-06 · `umbrald` as a `systemd --user` service
- **What:** a `systemd --user` unit for `umbrald`, shipped in the `.deb` under
  `/usr/lib/systemd/user/` and not enabled by default; `umb` keeps autostarting the daemon
  when the unit is not in use (Tech §9, delta `2026-10-deb-package`).
- **REQ:** Art. 9 — a 0.1 distribution item the Plan and Tech §9 set; REQ-HND-002 for the handoff a unit refuses
- **Files:** `packaging/deb/**`, `packaging/systemd/`
- **Depends on:** T-PKG-03
- **Done:** in the Ubuntu 26.04 smoke container with a user manager, `systemctl --user start
  umbrald` serves `umb status`, a stop leaves no process, and `dpkg -c` lists the unit. Written as
  `packaging/systemd/umbrald.service`, check 5/5 of `packaging/deb/check.sh` with three selftest
  mutants, and `packaging/deb/smoke_systemd.sh`, run by `task deb:smoke` and CI's `deb` job.

### [x] 2026-10-31 T-PKG-07 · tar.gz archives and the `launchd` plist
- **What:** the Linux and macOS tar.gz archives of the three binaries, the Linux one with the
  kit's `share/` tree, and a `launchd` agent plist for `umbrald` in the macOS one (Tech §9,
  Plan's Hardening phase, delta `2026-10-deb-package`).
- **REQ:** Tech §9 (0.1 distribution); REQ-PKG-001, REQ-PKG-002 for the Linux archive's `share/`
- **Files:** `packaging/tar/**`, `packaging/launchd/`
- **Depends on:** T-PKG-03
- **Done:** each archive is built in CI on its OS and unpacks to binaries that print their
  version; the plist passes `plutil -lint`. Written as `packaging/tar/build.sh`, `check.sh`,
  `check_selftest.sh` and `packaging/launchd/io.github.ecrespo.umbral.umbrald.plist`, run by
  `task tar` and CI's `archives` job on `ubuntu-latest` and `macos-latest`.

### [x] 2026-11-18 T-PKG-08 · the package names libffi, which the daemon opens at start
- **What:** `umbrald` panics at start without `libffi.so.8`, which yzma's purego bindings open
  and no binary links, so `Depends` derived from the binaries never named it (delta
  `2026-11-deb-libffi`).
- **REQ:** Tech §9.3 (0.1 distribution); Art. 1 (the gate's smoke installs what a user installs)
- **Files:** `packaging/deb/**`, `packaging/tar/build.sh`
- **Depends on:** T-PKG-03, T-PKG-06, T-PKG-07
- **Done:** the `.deb` declares `libffi8` and `check.sh` refuses one that does not; installed
  alone in a clean Ubuntu 26.04 container, `umb` autostarts the daemon, and the unit starts it
  in a container booted with systemd; the Linux archive's `README` names the library.

### [ ] T-REL-01 · A week of the TUI as the main terminal, before release 0.1
- **What:** the TUI used for one week as the main terminal without blocking regressions —
  F0's third exit criterion, moved here by delta `2026-09-defer-tui-week` so the week is spent
  on the TUI that ships, agent panel included. A human's to perform; no code.
- **REQ:** Art. 9 — a release gate set by the plan (delta `2026-09-defer-tui-week`); no REQ.
- **Files:** `docs/qa/` (the record of the week, beside `docs/qa/f0-tui.md`)
- **Depends on:** the F1 TUI work (T-F1-20)
- **Done:** seven days of the TUI as the daily driver with no blocking regression, recorded
  where `docs/qa/f0-tui.md` was. Release 0.1 does not ship without it.

### [ ] T-REL-02 · Walk `docs/qa/f1-tui.md`, before release 0.1
- **What:** the human walk of the agent panel that closes `T-F1-20`'s Done line, moved here by
  delta `2026-10-f1-close` when F1 closed. It includes the extensions view T-F1-37 added: the
  `mcp:` and `skills:` rows, a server going `unavailable`, and a turn's row of MCP tools and
  loaded skills. A human's to perform; no code unless a step fails.
- **REQ:** REQ-TUI-001, REQ-TUI-002, REQ-TUI-003, REQ-TUI-004
- **Files:** `docs/qa/f1-tui.md`
- **Depends on:** T-F1-20, T-F1-37. **Goes before T-REL-01.**
- **Done:** every step of `docs/qa/f1-tui.md` walked on a real daemon with no step failing, and
  the walk recorded in the file. Release 0.1 does not ship without it.

### [~] T-REL-03 · The release workflow's first run
- **What:** `.github/workflows/release.yml` has never run (delta `2026-11-signed-releases`,
  decision 4). Push a pre-release tag, `v0.1.0-rc.1`, from `develop`; read the draft release
  it leaves; check one download. What the run breaks is fixed here. The workflow publishes
  and never checks what it made, so this task also adds a step, before the release is
  created, that runs `sha256sum -c SHA256SUMS` and verifies each package's two attestations;
  without it nothing reddens if an attestation step is removed later.
  Whether this task gates release 0.1, as T-REL-01 and T-REL-02 do, is the Tech Lead's to
  decide; delta `2026-11-open-questions` proposes that it does.
- **REQ:** REQ-PKG-009
- **Files:** `.github/workflows/release.yml`
- **Depends on:** T-PKG-03, T-PKG-07, T-PKG-08. The repository is public, which the
  attestations need (R2).
- **Done:** the draft release of `v0.1.0-rc.1` holds the `.deb`, both tar.gz archives,
  `SHA256SUMS` and the SBOM; `sha256sum -c SHA256SUMS` passes on a download;
  `gh attestation verify <file> --repo ecrespo/umbral` accepts its build provenance, bound to
  the workflow and the tag, and its SBOM attestation; the workflow's own verification step
  fails a run whose attestation step was removed; a tag on a commit that is on neither
  `develop` nor `main` is refused. The tag is pushed by the Tech Lead.

### [~] T-HRD-01 · The project's rule-signing key
- **What:** the Tech Lead creates the project's signing key under
  `docs/runbooks/rule-signing.md` §4.1, and its fingerprint becomes the trust seed T-F1-30
  ships empty (delta `2026-10-f1-close`).
- **REQ:** REQ-SEC-011, REQ-SEC-013, Art. 5 (signed rule material)
- **Files:** `internal/security/adapters/rules/trustseed/`, `docs/runbooks/rule-signing.md`
- **Depends on:** T-F1-30
- **Done:** a bundle signed with the key and served locally is accepted by a daemon built with the
  seed, and one signed with any other key is refused and recorded in `egress_log`.

### [x] 2026-10-24 T-HRD-02 · The questions F1's ratified deltas left open
- **What:** answer each of the seven by a delta, or accept it in writing in the delta that raised
  it (delta `2026-10-f1-close`):
  - `2026-10-skill-catalog`: forms of a command the normaliser does not see (`env -S`, a word
    built by substitution, a raw JSON-RPC client); `umb rules key add|rotate`, `rollback` and
    `reset` not being destructive; `skill_load` serving the stored copy without checking its
    digest;
  - `2026-10-umb-mcp-panel`: the catalog's descriptions reaching the system prompt without
    tainting the turn; `umb skill`/`umb mcp` from the agent's own PTY guarded only by patterns,
    not refused by the daemon; `umb mcp remove` not being destructive; which `env_refs` a `cli`
    client may set;
  - `2026-10-acp-mediation` (added 2026-10-24): the command normaliser reads no quotes, so a
    `run_command` line the model writes — `env 'A=x y' rm -rf /` — hides its program from the
    destructive floor (an ACP terminal is decided on its words as well); `run_command` in a zsh
    PTY loses a last line printed without a newline (`PROMPT_SP`).
  - `2026-10-usf2-live` (added 2026-10-24): a hosted OpenCode is not offline — its empty isolated
    cache makes it install its provider package from npm and fetch a model catalogue at start —
    and whether the manifest should switch that off; a hosted agent learns the main copy and its
    sibling threads' worktrees from git and may read them, unmediated by design. (Its third
    question, what "US-F2-001 passes" means, belongs to the F2a close rather than here.)
- **REQ:** REQ-SEC-005, REQ-SKL-005, REQ-CLI-008
- **Files:** `changes/<YYYY-MM>-<slug>/` per decision; code only where a decision asks for it
- **Depends on:** —
- **Done:** each question names its answer and where it was decided; `task ci` green on any code
  that answer required.
- **Answered** by delta `2026-10-hrd02-open-questions`, ratified by the Tech Lead on 2026-10-24
  ("Ratifico el delta y el PR #50"); the code each answer needs is its own task below:
  - Q1 (`env -S`, substitution, raw JSON-RPC) and Q8 (`env 'A=x y' rm -rf /`, through
    `run_command` and ACP `execute`): a real shell parser, dynamic words ask, `setsid` and
    launchers read as wrappers — **T-HRD-06**;
  - Q2 (`umb rules key add|rotate`, `rollback`, `reset`) and Q6 (`umb mcp remove`): new
    `umb_rules_change` and `umb_mcp_remove` patterns, defence in depth for the Tech §5.3
    residual only — **T-HRD-07**;
  - Q3 (`skill_load` and the digest): hash on every load and serve from that read — **T-HRD-08**;
  - Q4 (catalog descriptions untainted): a delimited third-party block in `# Skills` —
    **T-HRD-09**;
  - Q5 (`umb skill`/`umb mcp` from the agent's PTY): closed by `2026-10-full-auto` decision 8,
    the Tech §5.3 residual accepted, once T-HRD-05 is done;
  - Q7 (which `env_refs` a `cli` client may set): accepted as is, the Tech §5.3 residual
    accepted, once T-HRD-05 is done;
  - Q9 (zsh `PROMPT_SP`): `unsetopt PROMPT_SP` after the user's rc in sessions the daemon flags
    as an agent's — **T-HRD-10**;
  - Q10 (a hosted OpenCode is not offline): the manifest's fixed `OPENCODE_DISABLE_*` variables,
    amending REQ-ACP-009 — **T-HRD-11**; a shared package cache is left to an F2 slice;
  - Q11 (sibling worktrees readable): disclosed now in PRD F2 0.9 REQ-ACP-007, shown by
    **T-HRD-12**; an OS sandbox is left to an F2 slice;
  - Q12 (new: a hosted agent's own processes are not "inside an agent's terminal"): its pids join
    `agentPeer` and it is started with `Setsid` — **T-HRD-05**, first.

### [x] 2026-10-24 T-HRD-05 · A hosted agent is refused what an agent's terminal is refused
- **What:** Q12: `cmd/umbrald`'s `agentPeer` also counts the processes of hosted ACP agents, which the ACP host lists (`Host.AgentPIDs`, wired by `cmd/umbrald`) when it launches them with `Setsid` (process-group signalling on cancel is unchanged). A hosted agent, or a command it runs itself, calling a `userOnly` method gets `PERMISSION_DENIED` like an agent's terminal (REQ-ORC-004). (delta `2026-10-hrd02-open-questions`).
- **REQ:** REQ-ORC-004, REQ-ACP-009
- **Files:** `internal/acp/**`, `cmd/umbrald/agentpeer.go`, `cmd/umbrald/acp.go`. Written as `internal/acp/adapters/sdk/host.go` (`AgentPIDs`, `Setsid`), `process.go` (the pid recorded from the start, the group killed at exit before reaping, stderr `/dev/null`), `reap_linux.go`/`reap_other.go`, `acptest/acptest.go` (the `dial` prompt), `cmd/umbrald/main.go` and `agentpeer.go`; tests `TestAHostedAgentIsRefusedTheUsersMethods_REQ_ORC_004`, `TestAHostedAgentIsKnownByItsSession_REQ_ORC_004`, `TestAHostedAgentsExitEndsWhatItLeft_REQ_ORC_004` and `TestAReusedPidStaysListedForItsNewerAgent_REQ_ORC_004`; delta `2026-10-hosted-agent-peer`
- **Depends on:** T-F2-08
- **Done:** a test with a fake hosted agent that calls `skill.install` over the socket is refused, seen red first; `task ci` green. Goes first.

### [x] 2026-10-24 T-HRD-06 · A shell parser in front of the destructive patterns
- **What:** Q1 and Q8: the command normaliser behind `MatchDestructive` parses the line with a real shell parser (a new dependency, checked under Art. 9); a word built at run time (`$(…)`, a variable, `env -S`) makes the line ask; `setsid`, `systemd-run`, `at` and `ssh localhost` are read as wrappers. Both paths into it are covered: `run_command` and the ACP `execute` permission's `raw_input.command`. (delta `2026-10-hrd02-open-questions`).
- **REQ:** REQ-SEC-005, REQ-ACP-003
- **Files:** `internal/security/domain/policy_destructive.go` and its tests, `go.mod`. Written as `policy_destructive.go` (the normaliser on `mvdan.cc/sh/v3/syntax`, `UnknownDynamicWord`/`UnknownStdinScript`/`UnknownUnparseable`), `.go-arch-lint.yml` (the `shell-syntax` vendor for `security-domain` only), `go.mod`; tests `TestDestructiveEvasionsAsk_REQ_SEC_005`, `TestARunCommandEvasionAsks_REQ_SEC_005` and `TestAnACPExecuteEvasionAsks_REQ_ACP_003`; delta `2026-10-shell-parser`
- **Depends on:** T-HRD-05
- **Done:** a table of evasions — `env -S`, `env 'A=x y' rm -rf /`, `$(…)`, `setsid -f umb skill install x` — asks on both paths, each red first; the existing pattern tests stay green.

### [x] 2026-10-24 T-HRD-07 · Destructive patterns for `umb rules` and `umb mcp remove`
- **What:** Q2 and Q6: built-in patterns `umb_rules_change` (`umb rules key add|rotate|remove`, `rollback`, `reset`) and `umb_mcp_remove`, defence in depth for the Tech §5.3 residual; the daemon's refusal stays the control, and an approved command still ends `PERMISSION_DENIED` when the peer check sees the caller. Tech §5.3's list gains both. (delta `2026-10-hrd02-open-questions`).
- **REQ:** REQ-SEC-005, REQ-CLI-008
- **Files:** `internal/security/domain/policy_destructive.go`, `policy_test.go`, Tech §5.3. Written as two entries of `DefaultDestructivePatterns` and their lines in `TestDestructiveAlwaysAsk_REQ_SEC_005`; delta `2026-10-rules-mcp-patterns`
- **Depends on:** T-HRD-06
- **Done:** each command matches its pattern and `umb rules status`/`umb mcp list` do not; `policy_test.go`'s `umb mcp remove x` moves to the destructive list.

### [x] 2026-10-24 T-HRD-08 · `skill_load` checks the stored copy's digest
- **What:** Q3: `ReadSkill` reads the stored bundle once, hashes it against the installed digest and serves the entry from that same read; a mismatch refuses the load, names the skill and tells the user to reinstall it. (delta `2026-10-hrd02-open-questions`).
- **REQ:** REQ-SKL-005
- **Files:** `internal/context/adapters/skills/**`
- **Depends on:** —
- **Done:** a test that edits the stored copy after install is refused, red first.

### [x] 2026-10-24 T-HRD-09 · Catalog descriptions as third-party text
- **What:** Q4: the `# Skills` section of the system prompt wraps the catalog in a delimited block whose preamble says the descriptions are third-party text, not instructions. Taint stays for content the user never saw. (delta `2026-10-hrd02-open-questions`).
- **REQ:** REQ-SKL-005
- **Files:** `internal/context/**` (the prompt builder)
- **Depends on:** —
- **Done:** a prompt test shows the block and preamble, and a description that tries to close it cannot.

### [x] 2026-10-24 T-HRD-10 · No lost last line in an agent's zsh
- **What:** Q9: the daemon marks the sessions it owns for an agent, and the zsh bootstrap runs `unsetopt PROMPT_SP` after the user's rc in those sessions only; a `precmd` hook re-applies it only if a framework turns it back on at every prompt. (delta `2026-10-hrd02-open-questions`).
- **REQ:** REQ-AGT-007
- **Files:** `internal/sessions/adapters/shellinteg/**`, `shell/zsh/`
- **Depends on:** —
- **Done:** `run_command` of `printf x` in a zsh thread PTY returns `x`, red first; skipped where zsh is absent; a user's own pane keeps `PROMPT_SP`. Written as `internal/sessions/lifecycle.go` (`umbralEnv`, `umbralOwned`) and `shell/zsh/.zshrc` (precmd and preexec); tests in `internal/sessions/integration/agent_test.go` and `env_test.go`; delta `2026-10-zsh-prompt-sp`.

### [x] 2026-10-31 T-HRD-11 · A hosted OpenCode starts without its own update traffic
- **What:** Q10: a manifest field of fixed variables the user's `env` refs cannot override; the OpenCode manifest sets its `OPENCODE_DISABLE_*` switches. Its delta amends PRD F2 REQ-ACP-009's environment allowlist with "the manifest's fixed variables". (delta `2026-10-hrd02-open-questions`).
- **REQ:** REQ-ACP-009
- **Files:** `internal/acp/**` (the manifest), `specs/prd/umbral-f2.md`
- **Depends on:** —
- **Done:** a test that the fixed variables reach the agent and a ref cannot override them; the disclosure stays true. Written as `Manifest.FixedEnv` in `internal/acp/domain/manifest.go`, set last by `Environment` and reserved by `ReservesEnv`; `TestTheManifestsFixedVariablesReachTheAgent_REQ_ACP_009`; delta `2026-10-opencode-fixed-env`.

### [x] 2026-10-31 T-HRD-12 · The disclosure names the readable worktrees
- **What:** Q11: what `umb thread create --agent` and the agent panel show for a hosted thread adds that the agent can read the repository's main copy and every other thread's worktree, as PRD F2 0.9 REQ-ACP-007 now says. (delta `2026-10-hrd02-open-questions`).
- **REQ:** REQ-ACP-007
- **Files:** `cmd/umb/thread.go`, `internal/tui/**` (the hosted-thread notice)
- **Depends on:** —
- **Done:** both texts carry the sentence, checked by their existing disclosure tests. Written in `cmd/umb/thread.go` and `internal/tui/agent.go` (`threadLines`); `TestAnACPThreadDisclosesItsTrafficAtCreation_REQ_ACP_007`, `TestAgentPanelSaysAHostedThreadIsUnobserved_REQ_ACP_007`.

### [x] 2026-10-24 T-HRD-03 · Three defects known when F1 closed
- **What** (delta `2026-10-f1-close`):
  - `fetch_url` follows four redirects where Tech §5.3 allows five: `CheckRedirect` refuses at
    `len(via) >= fetchMaxHops`, and `via` already holds the first request;
  - `TestAResponseOverTheLimitIsResultTooLarge_REQ_API_005` timed out on the macOS runner after
    five seconds reading the reply (PR #26, run 39779926649's first attempt, 2026-10-22), and
    passed on a rerun. The `REQ_AGT_007` cancel flake first listed here was fixed under this
    task by delta `2026-10-agent-pty-settle` (PR #27) before F1 closed;
  - API §5.37 numbers two sections (`api.schema` and `skill.*`);
  - added by the Tech Lead on 2026-10-23 and fixed under this task:
    `TestUmbAiStreamsALongAnswer_REQ_CLI_001` failed about once in 100 runs with two CPUs,
    `umb ai` ending with `ErrStreamOverflow` because the stream's reader ran 1024 notifications
    ahead of its relay. `umb ai` now reads through `client.NewUnboundedStream`, which queues
    without bound rather than overflow, so its reader never stops reading the socket
    (`TestAnUnboundedStreamKeepsEveryNotification_REQ_CLI_001`).
- **REQ:** REQ-AGT-018, REQ-API-005, REQ-API-004, REQ-CLI-001
- **Files:** `internal/tools/adapters/builtin/fetch.go`, its test, the macOS flake's test file,
  `specs/api/umbral-daemon-api-v1.md`; for the `umb ai` item `internal/client/stream.go`, its test
  and `cmd/umb/ai.go`. Written as `internal/tools/adapters/builtin/fetch.go` and `fetch_test.go`,
  `internal/api/blocks.go` and `blocks_fit_test.go`, the API and Tech specs, and comments citing
  §5.42 in `cmd/umb/skill.go`, `skill_test.go`, `internal/api/skills.go`, `skills_wire_test.go`
  and `internal/tui/adapters/daemon/daemon.go`
- **Depends on:** —
- **Done:** a test that follows exactly five redirects and refuses the sixth, seen red first; the
  flake's cause found and fixed, with the test green over repeated runs on macOS; one §5.37, with
  every reference to the renumbered section updated and `task schema` green;
  `TestUmbAiStreamsALongAnswer_REQ_CLI_001` green over repeated runs at `-cpu 1,2,4`.
- **Result** (2026-10-24):
  - **`fetch_url`:** `CheckRedirect` now refuses at `len(via) > 5`.
    `TestFetchUrlFollowsFiveRedirects_REQ_AGT_018` was red first, refusing the fifth redirect, and
    it reddens again when a sixth is allowed. The rules fetcher already counted right.
  - **The macOS timeout:** the failing subtest was `block.get plain … (shift 1)`. The test client
    gives a reply 5 s, and the reply was not ready in time. The likely cause, measured on Linux and
    not on macOS, was `plainPrefix`, which cut
    a transcript by bisection over `json.Marshal`. That is about twenty encodings of a megabyte
    per response, 1.2 s per subtest under `-race` on an idle Linux machine.
    - **The fix:** one pass adds up each rune's escaped width, and the encoder confirms the cut
      twice, falling back to the bisection if they ever disagree. This brings the subtest to
      0.5 s.
    - **Tests:** `TestAPlainCutMatchesTheEncoder_REQ_API_005` checks every character JSON escapes
      at every room. `TestAPlainCutEncodesAFewTimes_REQ_API_005` was red at 20 encodings.
    - **Mutations:** a wrong width for `<`, and dropping the encoder's check, each redden a test.
    - **macOS:** the `macos-latest` race job was green on four attempts of PR #49's CI run
      40115764015 (jobs 120936592057, 120939053485, 120940287137, 120942499138).
  - **API §5.37:** `skill.*` is now §5.42, after `worktree.*`, in API 1.51. §4 `Skill`, §6
    `skill.changed`, Tech §9.4 and the code comments point there, and `task schema` is green. No
    other section moved.

### [x] 2026-10-23 T-HRD-04 · A client that stops reading holds up no other
- **What** (delta `2026-10-slow-client-queue`, approved by the Tech Lead on 2026-10-23):
  - `Server.broadcast` queues each control notification in the connection's own outbox, written
    by that connection's goroutine, instead of writing every socket on the dispatcher;
  - past 8 MiB unwritten the connection is closed, never trimmed, and the client reconnects with
    a snapshot;
  - a connection is sent broadcasts once its handshake has succeeded, read through an atomic.
- **REQ:** REQ-API-006
- **Files:** `internal/api/outbox.go`, `internal/api/slowclient_test.go`, `internal/api/conn.go`,
  `internal/api/notify.go`, `internal/api/system.go`, `specs/prd/umbral-mvp.md`,
  `specs/api/umbral-daemon-api-v1.md`
- **Depends on:** —
- **Done:** `TestASlowClientHoldsUpNoOtherClient_REQ_API_006` red before the change and green
  after, over 20 runs at `-cpu 1,2,4`; removing the close on overflow reddens it; `task ci` green.

### [x] 2026-11-19 T-HRD-13 · Tests that fail on a CI runner and pass on a rerun
- **What:** eight tests failed on the runners on 2026-11-18, on code that did not touch them,
  and a cause is suspected for two of them only (`REQ_AUT_004`, `REQ_RMT_001`). Each is reproduced before it is changed, and a fix
  that only raises a timeout or adds a retry is not one.
  - `TestAnACPAgentCrashEndsTheTurn_REQ_ACP_006` (`ubuntu-latest`, run 44210237570): after the
    agent's crash the next prompt answers `acp: the agent failed: its process exited` instead
    of finding the agent relaunched (`host_test.go:218`). Second sighting; the first was on
    T-F2-60's pull request.
  - `TestTheUsersGlobalAgentConfigIsNotRead_REQ_ACP_009` (`macos-latest`, the same run): the
    fake agent's process exits with status 2 (`host_test.go:579`). Also a second sighting.
  - `TestAProgramsUnenteredInputNeverJoinsTheNextLine_REQ_FTU_003` (`macos-latest`, the same
    run): `send_keys` is refused with "the terminal changed since the call was decided"
    (`interact_test.go:409`). Second sighting; the first was on T-F2-42's pull request.
  - `TestATimeoutCarriesTheLastState_REQ_AUT_004` (Windows, run 44213787204): a one-second wait
    was measured at 999.724 ms and the test asks for at least a second
    (`waits_test.go:264`). The first sighting. Whether the wait may end early or only the
    test's two clocks disagree is the question to answer first.
  - `TestAnAddedHostIsLinkedThroughItsForwardedSocket_REQ_RMT_001` (`ubuntu-latest`, on
    T-PKG-08's pull request, which changes packaging only): `Add` returned the host already
    `connected` and the test asks for `connecting` (`manager_test.go:166`). The first sighting.
    The fake tunnel answers at once, so the link can finish before `Add` reads the state;
    whether `Add` may answer `connected` is the question.
  - `TestADroppedRemoteLinkComesBackAndItsPanesResume_REQ_RMT_002` (`ubuntu-latest`, the rerun
    of the same job): "no nas disconnected" after 121 s (`remote_test.go:381`). Seen before on
    T-F2-60's pull request.
  - `TestACancelKillsTheACPTerminalsFirst_REQ_ACP_008` and
    `TestATerminalStartedDuringACancelIsKilled_REQ_ACP_008` (`macos-latest`, run 44408940798, on
    this task's own pull request, which changes this file only): the fake agent's process
    exits with status 2 (`host_test.go:896`, `:957`), as in `REQ_ACP_009` above. One cause
    for the three is the first thing to look for.
- **REQ:** REQ-ACP-006, REQ-ACP-008, REQ-ACP-009, REQ-FTU-003, REQ-AUT-004, REQ-RMT-001,
  REQ-RMT-002; Art. 2 (a test that fails without
  a defect hides the run that fails with one)
- **Files:** `internal/acp/adapters/sdk/**`, `internal/sessions/integration/interact_test.go`
  and what it drives, `internal/waits/**`, `internal/remote/**`, `cmd/umbrald/remote_test.go`
- **Depends on:** —
- **Done:** each failure reproduced, on the runner's OS or by narrowing the schedule locally;
  its cause named in the execution log; the test green over 50 runs at `-cpu 1,2,4` on Linux
  and in a green run of the pull request on each runner, macOS and Windows included (delta
  `2026-11-run-command-prompt`, decision 6; the line asked for the 50 runs on the OS that
  failed); and for each, whether the defect was in the product or in the test.

### [x] 2026-11-19 T-HRD-14 · Spike: OpenCode and a `full-auto` shell under Landlock
- **What:** measure what the profile of delta `2026-11-agent-sandbox` decision 5 breaks, on
  Ubuntu 26.04: the agent's start, `go build`, `npm install`, a `git commit` in the worktree,
  and the user's shell with bash, zsh and fish under oh-my-zsh and starship. No product code.
- **REQ:** Art. 5 (`full-auto` "SHALL only run inside a sandbox or a disposable worktree")
- **Files:** `docs/spikes/2026-11-landlock.md`
- **Depends on:** —
- **Done:** the spike's report says what each profile entry broke and what it took to run,
  and gives the measurements that fix the profile's write list (delta
  `2026-11-open-questions`, S3) and size the model relay of T-HRD-20 (S1).

### [x] 2026-11-19 T-HRD-15 · The `sandbox` port and the Linux adapter; hosted agents confined
- **What:** the delta's decisions 1, 2, 5 and 7: a `sandbox` port wired in `cmd/umbrald`, a
  Landlock adapter applied by a re-exec helper, every hosted ACP agent confined, and a
  degraded mode reported in `system.status` where Landlock is unavailable. It starts with a
  delta of its own, approved before any code: the `SBX` requirements, the amendments to
  REQ-ACP-007 and REQ-ACP-009, `system.status`'s degraded mode and `[sandbox] write`.
- **REQ:** REQ-SBX-001, REQ-SBX-002, REQ-SBX-003, REQ-SBX-004, REQ-SBX-005 (written by this task's delta `2026-11-sandbox-linux`); Art. 5, Art. 3 (the module's `.go-arch-lint.yml` rules before its code)
- **Files:** a new `internal/sandbox/**`, `.go-arch-lint.yml`, `internal/acp/**`,
  `internal/api/**`, `internal/config/**`, `cmd/umbrald/**`, `specs/prd/umbral-f2.md`,
  `specs/api/umbral-daemon-api-v1.md`, `specs/technical/umbral-architecture.md`
- **Depends on:** T-HRD-14. Delta `2026-11-open-questions` answers the questions: files
  only, the network left open and disclosed (S1); the write list fixed from the spike (S3);
  `[sandbox] mode = "off"` as a disclosed opt-out (S4). The profile leaves the libffi cache
  directory of T-HRD-18 out of every write list.
- **Done:** a hosted agent that writes outside its profile, or reads another thread's
  worktree or Umbral's data directory, is refused by the kernel, with a test per rule that
  reddens when the rule is removed; without Landlock the daemon says so and the thread's
  disclosure says the agent is unconfined.

### [x] 2026-11-19 T-HRD-16 · The macOS sandbox adapter
- **What:** the delta's decision 3: `sandbox-exec` with a generated profile behind the same
  port.
- **REQ:** Art. 5; the `SBX` requirements T-HRD-15 writes
- **Files:** `internal/sandbox/**`
- **Depends on:** T-HRD-15
- **Done:** T-HRD-15's tests pass on `macos-latest` through this adapter.

### [x] 2026-11-19 T-HRD-17 · `full-auto` shells confined; the disclosures
- **What:** decision 6 of delta `2026-11-agent-sandbox`: an Umbral thread's own shell is
  confined while the thread is in `full-auto`, and the disclosures say which threads are
  confined. REQ-ORC-004 stands as written; the task's own delta, `2026-11-full-auto-sandbox`,
  adds REQ-SBX-006 beside it. The `egress_visibility = confined` value of that delta's
  decision 8 and its migration are T-HRD-20's (this task's delta, decision 9): no thread has
  a network profile before the relay.
- **REQ:** REQ-SBX-006 (written by this task's delta `2026-11-full-auto-sandbox`); Art. 5, Art. 4; REQ-ORC-004
- **Files:** `internal/sessions/**`, `internal/agents/**`, `internal/tools/**`,
  `internal/sandbox/**`, `internal/api/**`, `internal/tui/**`, `cmd/umbrald/**`,
  `cmd/umbral-desktop/frontend/app.js`, `docs/user/full-auto.md`, `specs/prd/umbral-f2.md`,
  `specs/api/umbral-daemon-api-v1.md`, `specs/technical/umbral-architecture.md`
- **Depends on:** T-HRD-15. Delta `2026-11-open-questions`: `full-auto` without a sandbox is
  allowed, degraded and disclosed (S2), and the opt-out covers `full-auto` shells too (S4).
- **Done:** a `full-auto` command that writes outside its worktree is refused by the kernel
  and not by the policy's reading of the line; a thread in `ask`, `normal` or `auto-edit` is
  not confined.

### [x] 2026-11-19 T-HRD-18 · The daemon loads libffi from a path its user can write (macOS, Windows)
- **What:** on macOS and Windows x86_64, `github.com/jupiterrider/ffi`'s `init` writes its
  embedded libffi to the user's cache directory and loads it from there; a file already at
  that path is loaded as it is. A process of the same user that writes it runs code inside
  `umbrald` at its next start, and a `full-auto` command or a hosted agent is such a process
  (delta `2026-11-open-questions`, L2 and L3). Linux loads the system's `libffi.so.8`.
- **REQ:** Art. 5 (`full-auto` "SHALL only run inside a sandbox or a disposable worktree":
  this is a way out of the worktree that lasts past the thread)
- **Files:** `cmd/umbrald/**`, `internal/llmgw/adapters/yzma/**`, `Taskfile.yml`,
  `packaging/tar/build.sh`, `.github/workflows/*.yml`
- **Depends on:** —
- **Done:** on both systems the daemon loads a libffi whose bytes it has checked, or one from
  a path only an administrator can write; a test plants another file, a symbolic link and a
  truncated file at the cache path and the daemon loads none of them; and it never loads the
  library by its bare name, with the cache directory unwritable or `FFI_NO_EMBED=1` set.
  Whether this task gates release 0.1 is the Tech Lead's to decide.

### [x] 2026-11-19 T-HRD-19 · go-arch-lint v1.19.2's notice on `cmd/umbrald/activeai.go`
- **What:** v1.19.2 reports `cmd` handing its own `localSuggester` and `blockOutput` to
  `activeai.New`, which v1.19.1, the pinned version, does not (delta `2026-11-signed-releases`
  Q5). Read the release's notes; either move the two types into an adapters package or record
  why the notice is wrong; then move the pin.
- **REQ:** Art. 3
- **Files:** `cmd/umbrald/activeai.go`, `internal/activeai/**`, `.go-arch-lint.yml`,
  `.github/workflows/ci.yml`, `Taskfile.yml`
- **Depends on:** —
- **Done:** `task arch` and `task arch:selftest` pass on the newest go-arch-lint, pinned.

### [ ] T-HRD-20 · A hosted agent's model traffic through a daemon-side relay
- **What:** option (b) of delta `2026-11-agent-sandbox` Q1: a confined agent has no network,
  and reaches its model endpoint through a relay the daemon serves, so the agent's model
  calls are recorded in `egress_log`. Its delta, `2026-11-model-relay`, was approved by the
  Tech Lead on 2026-11-20 with its five questions answered as recommended: the relay is an
  HTTP listener on a loopback port, since the sandbox refuses Unix sockets and the agent
  takes a base URL; the port rule's reach on Linux, every host, is accepted and disclosed;
  the relay records and does not redact; the provider's key stays with the daemon; a hosted
  thread whose network profile the kernel enforces reads `egress_visibility = confined`,
  computed from a new `network_profile` column rather than a rebuilt `threads` (Art. 6); and
  the task does not gate release 0.1. Three pieces, each mergeable on its own:
  - **a · the network profile (done 2026-11-20):** `Profile.Net` (ports), Landlock's port
    rules, the socket filter, the macOS rules, kernel tests for each, no caller yet;
  - **b · the relay:** a port in `internal/acp` and an adapter in `cmd/umbrald` over
    `llmgw`'s provider configuration — the listener, the thread's token, the streaming
    forwarder, the `egress_log` row;
  - **c · wiring and disclosure:** the manifest's base URL and port flag, two ports per
    thread, the profile's use, the migration, `egress_visibility`, the clients' text, Art.
    4's amendment, and the Done line against a real OpenCode.
- **REQ:** Art. 4; REQ-SBX-008 (piece a) and one more `SBX` requirement, written into the PRD
  by the piece that makes it true; REQ-ACP-007 and REQ-ACP-009, amended
- **Files:** `internal/sandbox/**`, `internal/acp/**`, `internal/llmgw/**`, `cmd/umbrald/**`,
  `internal/store/migrations/**`, `internal/api/**`, `specs/data-model/umbral-schema.md`,
  `specs/api/umbral-daemon-api-v1.md`
- **Depends on:** T-HRD-15
- **Done:** a hosted agent whose profile allows no network completes a turn through the
  relay; its requests to a non-loopback endpoint are `egress_log` rows and those to a loopback
  model are none, as Tech Design §4 has it for Umbral's own calls.

### [x] 2026-11-20 T-HRD-21 · A hosted thread in `full-auto`: its terminals are confined
- **What:** the open question of delta `2026-11-full-auto-sandbox`. A hosted thread can be in
  `full-auto` (REQ-ORC-004 covers an ACP thread's permission requests), and a
  `terminal/create` inside its worktree then runs unasked in a session the daemon starts
  outside the agent's sandbox. The Tech Lead chose, on 2026-11-20, to confine the terminal's
  shell with the thread's profile rather than make it ask or refuse `full-auto` to a hosted
  thread (delta `2026-11-hosted-terminal-sandbox`).
- **REQ:** REQ-SBX-007 (written by this task's delta); Art. 5; REQ-ORC-004, REQ-ACP-003
- **Files:** `internal/sessions/terminal.go`, `internal/sessions/lifecycle.go`,
  `internal/sessions/ports/ports.go`, `internal/agents/external.go`,
  `internal/agents/ports/ports.go`, `cmd/umbrald/acp.go`, `cmd/umbrald/shellsandbox.go`,
  `docs/user/full-auto.md`, `specs/prd/umbral-f2.md`, `specs/technical/umbral-architecture.md`
- **Depends on:** T-HRD-17
- **Done:** what a hosted thread in `full-auto` can write through a terminal is what its
  notice says it can, proved against the kernel.

### [ ] T-HRD-22 · The daemon's file writes follow a link swapped after the decision
- **What:** read by spec-guardian while reviewing T-HRD-21, not reproduced. `write_file` and
  a hosted agent's `fs/write_text_file` are performed by the daemon, which no sandbox
  confines. The policy resolves the target's links and decides; the write then opens the
  deepest existing directory of the target and checks only that its last component is a
  directory. A process confined to the worktree — a `full-auto` shell, a hosted agent or one
  of its terminals — that replaces a directory of the worktree by a link between the two
  gets a write outside the worktree, with content it chose, unasked under `full-auto` or
  `auto-edit`. `fs/read_text_file` and `read_file` have the same shape for reads. First
  reproduce it with a test that widens the window; then open the root at the write root and
  write relative to it, so a link met on the way is refused by the kernel.
- **REQ:** Art. 5; REQ-SBX-001, REQ-SBX-006, REQ-SBX-007 (what they bound, this goes around);
  REQ-ACP-004
- **Files:** `internal/tools/adapters/builtin/files.go`, `internal/agents/external.go`
- **Depends on:** —
- **Done:** a test that swaps a worktree directory for a link between the decision and the
  write goes red before the fix and green after; a write or a read whose path leaves the
  write root through a link is refused whenever the link appeared. Whether this task gates
  release 0.1 is the Tech Lead's to decide.

## Traceability matrix (hardening)

| REQ | Tasks | Verification |
|---|---|---|
| REQ-PKG-001 | T-PKG-01, T-PKG-03, T-PKG-07 | `icons` job; the `deb` job's `packaging/deb/check.sh` (`dpkg -c`, each file compared with the kit), `check_selftest.sh` and `smoke.sh`; the `archives` job's `packaging/tar/check.sh` (step 2) and `check_selftest.sh` |
| REQ-PKG-002 | T-PKG-02, T-PKG-03, T-PKG-07 | `desktop-file-validate`, on the kit's file and on the packaged one; the Linux archive's `.desktop` compared byte for byte with the kit's by `packaging/tar/check.sh` |
| REQ-PKG-003 | T-PKG-02, T-PKG-03 | `.desktop` review in CI; `packaging/deb/check.sh` step 3 on the packaged file |
| REQ-PKG-006 | T-PKG-01 | the `icons` job regenerates the kit with `build.py` and compares byte for byte, rendered images pixel for pixel within two levels (delta `2026-10-icons-render-tolerance`); checksums alone would pass a source edit committed with its new checksum |
| REQ-PKG-009 | T-REL-03 | the release workflow's own verification step (`sha256sum -c SHA256SUMS`, both attestations of each package), added by T-REL-03; and its first tagged run, checked by hand on a download |
| REQ-TUI-001, 002, 003, 004 | T-REL-02 (walk) | `docs/qa/f1-tui.md`; the harness tests are F1's |
| REQ-SEC-011, REQ-SEC-013 | T-HRD-01 (the key) | TestTheEmbeddedSeedCarriesTheProjectKey_REQ_SEC_011 (the seed, its `.pub` file and the README agree); at the close, a bundle signed with the project key accepted, any other refused |
| REQ-SEC-005, REQ-SKL-005, REQ-CLI-008 | T-HRD-02 (decisions) | per decision |
| REQ-ORC-004 (hosted agents) | T-HRD-05 | TestAHostedAgentIsRefusedTheUsersMethods_REQ_ORC_004, TestAHostedAgentIsKnownByItsSession_REQ_ORC_004, TestAHostedAgentsExitEndsWhatItLeft_REQ_ORC_004, TestAReusedPidStaysListedForItsNewerAgent_REQ_ORC_004 |
| REQ-SEC-005, REQ-ACP-003 | T-HRD-06, T-HRD-07 | TestDestructiveEvasionsAsk_REQ_SEC_005, TestARunCommandEvasionAsks_REQ_SEC_005, TestAnACPExecuteEvasionAsks_REQ_ACP_003; TestDestructiveAlwaysAsk_REQ_SEC_005 (the new patterns) |
| REQ-SKL-005 | T-HRD-08, T-HRD-09 | TestSkillLoadRefusesAnEditedStoredCopy_REQ_SKL_005, TestSkillLoadServesACopyInstalledFromATarball_REQ_SKL_005; TestTheCatalogIsADelimitedThirdPartyBlock_REQ_SKL_005 |
| REQ-AGT-007 (zsh), REQ-AGT-003 | T-HRD-10 | TestAZshThreadKeepsAnUnterminatedLastLine_REQ_AGT_007, TestAZshThreadKeepsPromptSPOffUnderAFramework_REQ_AGT_007 |
| REQ-ACP-009, REQ-ACP-007 | T-HRD-11, T-HRD-12 | `TestTheManifestsFixedVariablesReachTheAgent_REQ_ACP_009`, `TestTheACPEnvironmentHoldsNoSecrets_REQ_ACP_009`; `TestAnACPThreadDisclosesItsTrafficAtCreation_REQ_ACP_007`, `TestAgentPanelSaysAHostedThreadIsUnobserved_REQ_ACP_007` |
| REQ-AGT-018, REQ-API-005, REQ-API-004, REQ-CLI-001 | T-HRD-03 | TestFetchUrlFollowsFiveRedirects_REQ_AGT_018; TestAPlainCutMatchesTheEncoder_REQ_API_005, TestAPlainCutEncodesAFewTimes_REQ_API_005 and the macOS job over repeated runs; `task schema`; TestAnUnboundedStreamKeepsEveryNotification_REQ_CLI_001 |
| REQ-API-006 | T-HRD-04 | TestASlowClientHoldsUpNoOtherClient_REQ_API_006, TestASlowClientIsClosedNotTrimmed_REQ_API_006 |
| REQ-ACP-006, REQ-ACP-008, REQ-ACP-009, REQ-FTU-003, REQ-AUT-004, REQ-RMT-001, REQ-RMT-002 (the tests' own reliability) | T-HRD-13 | TestAnACPAgentCrashEndsTheTurn_REQ_ACP_006, TestTheUsersGlobalAgentConfigIsNotRead_REQ_ACP_009, TestAProgramsUnenteredInputNeverJoinsTheNextLine_REQ_FTU_003, TestATimeoutCarriesTheLastState_REQ_AUT_004, TestAnAddedHostIsLinkedThroughItsForwardedSocket_REQ_RMT_001, TestADroppedRemoteLinkComesBackAndItsPanesResume_REQ_RMT_002, TestACancelKillsTheACPTerminalsFirst_REQ_ACP_008, TestATerminalStartedDuringACancelIsKilled_REQ_ACP_008, each over 50 runs at `-cpu 1,2,4` on Linux and green on the runners; TestAddAnswersConnectingHoweverFastTheLinkIs_REQ_RMT_001 and TestRunCommandReturnsWithTheShellAtItsPrompt_REQ_FTU_003 for the two product fixes |
| REQ-SBX-001 | T-HRD-15 | TestAConfinedProcessMakesNoLocalSocket_REQ_SBX_001, TestAHostedAgentWritesNothingInTheRepositorysGitDirectory_REQ_SBX_001, TestAConfinedProcessWritesOnlyInsideItsProfile_REQ_SBX_001, TestAConfinedProcessWritesItsDeviceFiles_REQ_SBX_001, TestAConfinedProcessWritesNothingInTheRepositorysGitDirectory_REQ_SBX_001, TestAProfileThatCannotBeAppliedRunsNothing_REQ_SBX_001, TestAHostedAgentIsLaunchedThroughTheSandbox_REQ_SBX_001, TestAHostedAgentIsConfinedByTheKernel_REQ_SBX_001 |
| REQ-SBX-002 | T-HRD-15 | TestALinkBesideADeniedDirectoryOpensNothing_REQ_SBX_002, TestAThreadWhoseDirectoryReachesUmbralsCannotBeConfined_REQ_SBX_002, TestAThreadWhoseDirectoryHoldsUmbralsDataIsNotLaunched_REQ_SBX_002, TestAnAgentIsVouchedOnlyItsOwnDirectories_REQ_SBX_002, TestAConfinedProcessCannotReadUmbralsDirectories_REQ_SBX_002, TestAnAgentsProfileWritesItsOwnAndDeniesUmbrals_REQ_SBX_002, TestAHostedAgentCannotReadUmbralsData_REQ_SBX_002 |
| REQ-SBX-003 | T-HRD-15 | TestTheAdapterSaysWhatItEnforces_REQ_SBX_003, TestTheSandboxOptOutIsSaid_REQ_SBX_003, TestAHostedThreadSaysWhetherItIsConfined_REQ_SBX_003, TestAgentPanelSaysWhetherAHostedThreadIsConfined_REQ_SBX_003, TestStatusSaysWhetherTheSandboxEnforces_REQ_SBX_003 |
| REQ-SBX-004 | T-HRD-15 | TestSandboxWriteRefusesWhatReachesAProtectedDirectory_REQ_SBX_004, TestTheSandboxSectionIsRead_REQ_SBX_004, TestASandboxWriteEntryThatReachesUmbralStopsTheDaemon_REQ_SBX_004 |
| REQ-SBX-005 | T-HRD-15 | the confined wording of TestAHostedThreadSaysWhetherItIsConfined_REQ_SBX_003 and TestAgentPanelSaysWhetherAHostedThreadIsConfined_REQ_SBX_003, which carry REQ_SBX_005's sentence |
| REQ-SBX-006 | T-HRD-17 | TestAFullAutoCommandIsBoundedByTheKernel_REQ_SBX_006, TestAThreadsShellIsReplacedWhenItsConfinementChanges_REQ_SBX_006, TestAShellThatCannotBeConfinedDoesNotRun_REQ_SBX_006, TestAReadEntryOpensOnlyWhatLiesInsideADeniedDirectory_REQ_SBX_006, TestAReadEntryIsReadAndNotWritten_REQ_SBX_006, TestAReadEntryIsReopenedForReadingOnly_REQ_SBX_006, TestAgentPanelSaysWhetherAFullAutoThreadIsConfined_REQ_SBX_006, TestAFullAutoCommandCannotSwitchItsConfinementOff_REQ_SBX_006, TestACommandCarriesItsTurnsFullAuto_REQ_SBX_006 |
| REQ-SBX-007 | T-HRD-21 | TestAHostedTerminalIsBoundedByTheKernelUnderFullAuto_REQ_SBX_007, TestAHostedTerminalIsConfinedUnderFullAuto_REQ_SBX_007, TestAHostedTerminalCarriesItsTurnsFullAuto_REQ_SBX_007, TestATerminalIsStartedOnItsTurnsReading_REQ_SBX_007, TestATerminalIsStartedOnTheContextItsTurnGave_REQ_SBX_007, TestAHostedThreadsTerminalsEachGetADirectoryOfTheirOwn_REQ_SBX_007 |
| REQ-SBX-008 | T-HRD-20 (piece a) | TestAConfinedProcessReachesOnlyItsPorts_REQ_SBX_008 (Landlock, and `sandbox-exec` on the macOS runner), TestANetworkProfileIsRefusedWhereItCannotBeEnforced_REQ_SBX_008, TestANetworkProfileIsRefusedWhereNothingIsEnforced_REQ_SBX_008, TestANetworkProfileCannotNameEveryPort_REQ_SBX_008, TestANetworkProfileOpensOnlyItsLoopbackPorts_REQ_SBX_008, TestAProfilesNetworkIsCarriedToTheHelper_REQ_SBX_008 |

## Execution log

| Date | Tasks | Result | Notes |
|---|---|---|---|
| 2026-09-11 | T-PKG-01, T-PKG-02 | done | Folded from `changes/_archive/2026-09-visual-identity/`. The `icons` CI job verifies the checksums, regenerates the kit from source with `tools/build.py` and compares byte for byte, then validates the `.desktop` file. The launcher targets `umbral-tui` with `Terminal=true`, since release 0.1 ships no desktop client. Recorded in the F0 tasks file until the hardening file existed; moved here on 2026-09-20 with the rest of the packaging work. |
| 2026-10-18 | T-REL-02, T-HRD-01, T-HRD-02, T-HRD-03 | added | Delta `2026-10-f1-close`: F1 closed, and what it left moved here — `T-F1-20`'s QA walk as a release gate, the rule-signing key, the seven questions the nine ratified deltas left open, and three known defects. |
| 2026-10-22 | T-HRD-03 | revised | Its `REQ_AGT_007` item — a late interrupt eating the first byte of the agent's next command — fixed by delta `2026-10-agent-pty-settle` (PR #27, `TestALateInterruptDoesNotEatTheNextCommand_REQ_AGT_007`) and replaced by the `REQ_API_005` macOS read timeout seen on PR #26 (delta `2026-10-f1-close`, revised). The task stays open for the other three items. |
| 2026-10-23 | T-HRD-03 | revised | The Tech Lead added the `umb ai` stream overflow found during T-F2-04 (`TestUmbAiStreamsALongAnswer_REQ_CLI_001`, 3 failures in 300 runs at `-cpu 2`, none at `-cpu 1`); fixed by `client.NewUnboundedStream`, 900 runs green at `-cpu 1,2,4` afterwards. The task stays open for its other three items. |
| 2026-10-23 | T-HRD-04 | done | Added and approved by the Tech Lead (delta `2026-10-slow-client-queue`) after the T-HRD-03 review found that `Server.broadcast` wrote every client's socket on the dispatch goroutine: one client that stopped reading stopped every other's notifications. Each connection now has an outbox and its own writer; past 8 MiB it is closed. The new test blocked the dispatcher for 20 s before the change. A write deadline, also proposed, was dropped: with the outbox it would only disconnect a suspended TUI. |
| 2026-10-24 | T-HRD-03 | done | Its last three items are closed. `fetch_url` follows five redirects and refuses the sixth. The `REQ_API_005` macOS timeout was `plainPrefix` encoding a megabyte about twenty times per cut; it is now one pass checked by the encoder, 1.2 s to 0.5 s per subtest under `-race`. macOS was green on four attempts of PR #49's CI. `skill.*` moved from the second §5.37 to §5.42 (API 1.51). Each fix was red first, and each was mutated to red again. |
| 2026-10-24 | T-PKG-03 | done | Delta `2026-10-deb-package` (Tech 1.67 §9.3). `task deb` builds `umbral_<version>_<arch>.deb` with `dpkg-deb` from the built binaries and the kit's files as they are; libghostty-vt is static, so it depends on `libc6 (>= 2.36)` alone. `check.sh` was red on an empty package first; `check_selftest.sh` shows it rejects a missing icon, an icon not the kit's, a `.desktop` naming another icon and a postinst that refreshes nothing. In an `ubuntu:26.04` container the package installed, `umb` autostarted the installed `umbrald` and read its status, the icon cache listed the icon, and after removal it no longer did. The `systemd --user` unit, the tar.gz archives and the `launchd` plist became T-PKG-06 and T-PKG-07. |
| 2026-10-24 | T-PKG-06, T-PKG-07 | added | Delta `2026-10-deb-package`, ratified by the Tech Lead: the `systemd --user` unit, the tar.gz archives and the `launchd` plist Tech §9 and the Plan name for 0.1, which no task owned. |
| 2026-10-24 | T-HRD-02 | done | Delta `2026-10-hrd02-open-questions`, ratified by the Tech Lead: every question names its answer above. Q5 and Q7 are accepted once T-HRD-05 is done; Q11 is disclosed in PRD F2 0.9 REQ-ACP-007. |
| 2026-10-24 | T-HRD-05 … T-HRD-12 | added | The code those answers need, Q12 (T-HRD-05) first. Spec-guardian's review found that the socket is reachable from a hosted agent's `HOME`, so a hosted agent can call `userOnly` methods today. |
| 2026-10-24 | T-HRD-05 | done | Delta `2026-10-hosted-agent-peer` (API 1.52 §2, Tech 1.68). The hole was measured first: a fake hosted agent found the socket from its `HOME`, read the token and reached `skill.install`, which answered `VALIDATION_ERROR` for its empty params. The ACP host now lists its agents' pids from launch, the agents lead their own sessions, and when an agent exits its whole session is killed — on Linux before the zombie is reaped, so a reused pid is never signalled or forgotten; on macOS the pid stays listed until close; members are signalled through pidfds, a session not seen empty in 2 s keeps the pid listed, and pids are counted — with the agent's stderr `/dev/null`, since a discarding pipe held by a child hid the exit. The agent's call answers `PERMISSION_DENIED`. Each piece was mutated back and its test went red. |
| 2026-10-24 | T-HRD-06 | done | Delta `2026-10-shell-parser` (Tech 1.69 §5.2, §5.3). The normaliser parses the line with `mvdan.cc/sh/v3/syntax`, the one vendor a domain may use; 130 evasions — quoted separators, `env -S`, substitutions, globs, braces, launchers, shell options, interpreters, carriers, redirections on any statement, history, prompt variables, arithmetic — ask on both paths, each red first; nine mutations each redden the table. The vendor is a proposed Art. 3 amendment and REQ-SEC-005/REQ-ORC-004 gain a sentence, both for the Tech Lead. A word built at run time now asks, which costs `always` for lines with variables or globs. |
| 2026-10-24 | T-HRD-07 | done | Delta `2026-10-rules-mcp-patterns` (Tech 1.70 §5.3), on top of T-HRD-06's branch. `umb_rules_change` and `umb_mcp_remove` join the built-in list; ten command lines ask, `umb rules status`, `umb rules key list` and `echo umb rules reset` do not; `umb mcp remove x` moved from the harmless list. Removing `rollback` or `remove` from a pattern reddens the test. |
| 2026-10-24 | T-HRD-08 | done | Delta `2026-10-skill-load-digest` (Tech 1.71 §5.3b), stacked on T-HRD-07's branch. `ReadSkill` reads the stored copy once with the install reader, compares its digest with the row's and serves the entry from that read; an edited, added or removed file refuses every load of the skill with `VALIDATION_ERROR` naming it and saying to reinstall. Red first on all four edits; skipping the comparison reddens them. |
| 2026-10-24 | T-HRD-09 | done | Delta `2026-10-catalog-third-party` (Tech 1.72 §5.3b), stacked on T-HRD-08's branch. The `# Skills` section wraps the catalog in a `<skills>` block after a preamble naming the descriptions third-party text, not instructions; `&`, `<` and `>` in each entry are escaped when the prompt is rendered, so a description writing `</skills>` or a `<rules>` tag stays inside. Red first (no block); not escaping reddens it. |
| 2026-10-24 | T-HRD-10 | done | Delta `2026-10-zsh-prompt-sp` (Tech 1.73 §5.2b), stacked on T-HRD-09's branch. A thread-owned session gets `UMBRAL_AGENT_SESSION=1`; the zsh bootstrap turns `PROMPT_SP` off after the user's rc and again in `__umbral_precmd` and `__umbral_preexec`. `printf x` in a zsh thread came back `" "`, red, and now returns `x`, under a framework that sets the option every prompt too; a user's zsh pane keeps it. Not setting the variable, or dropping the precmd re-apply, reddens the tests. |
| 2026-10-31 | T-HRD-11 | done | Delta `2026-10-opencode-fixed-env` (PRD F2 0.15 REQ-ACP-009, Tech 1.88 §5.3f). `Manifest.FixedEnv` holds OpenCode's `OPENCODE_DISABLE_AUTOUPDATE`, `_DEFAULT_PLUGINS`, `_LSP_DOWNLOAD`, `_MODELS_FETCH` and `_SHARE`, read from the installed binary; `Environment` sets them after the refs and `ReservesEnv` refuses a ref naming one. Red first (no field); dropping the loop or the reservation reddens the test. The provider package from npm has no switch and stays the agent's own traffic. |
| 2026-10-31 | T-HRD-12 | done | No delta: PRD F2 0.9 REQ-ACP-007 already carries the sentence (delta `2026-10-hrd02-open-questions`); Tech 1.89 §5.3e, §5.3f and §9.4 follow it. `umb thread create --agent`'s stderr and the agent panel's hosted-thread line add that the agent can read the repository's main copy and every other thread's worktree. Red first in both disclosure tests; the panel's check reads the wrapped notice across lines. Both name the agent rather than "it", which could be read as Umbral; the desktop's thread view, which creates no hosted thread, carries the sentence too. |
| 2026-10-31 | T-PKG-06 | done | Tech 1.90 §9.3 and DD-014, delta `2026-10-service-units`. Spec-guardian found that a live handoff under the unit would kill the adopted shells when the unit stopped: a daemon systemd runs as its unit (`SYSTEMD_EXEC_PID` equal to its pid, not `INVOCATION_ID`, which a terminal under any unit passes on and which failed every handoff test on the first `task ci`) now refuses at `hello` (`TestAHandoffUnderAServiceManagerIsRefused_REQ_HND_002`, red without the check), the unit stops with `KillMode=mixed` and does not retry exits 64, 65, 75 and 78, and the smoke opens a pane, is refused a takeover and finds the unit's cgroup empty after the stop. Before that: `check.sh` was red first (the unit missing from the package); its selftest rejects a package without the unit, one running another binary and one whose postinst enables it. `smoke_systemd.sh` boots `ubuntu:26.04` with systemd as init in a privileged container sharing the host's cgroups; as a lingering user the unit is `disabled`, `systemctl --user start` serves `umb status --no-autostart`, one `umbrald` runs, and a stop leaves none. A package whose unit runs `sleep` instead fails it (no token). The package is mounted under `/srv`, since systemd mounts a tmpfs over `/tmp`. |
| 2026-10-31 | T-PKG-07 | done | Tech 1.91 §9.3, delta `2026-10-service-units`. After spec-guardian's review: the plist is never restarted by launchd, so an exit 75 or 78 cannot respawn every 10 s; `check.sh` fails a binary that does not print the archive's version and an archive with `._*` entries, with four more mutants (a failing `umbrald`, a silent `umb`, an `umbral-tui` of another version, an AppleDouble file); bsdtar writes without xattrs or AppleDouble entries, and BSD `touch -t` is given UTC. The `archives` job ran green on `ubuntu-latest` and `macos-latest`, `plutil -lint` included, on the commit before these fixes; the PR's run checks them. Before that: `build.sh` packs `umbral-<version>-<os>-<arch>.tar.gz`; `check.sh` runs each binary's version on its own OS and compares the Linux `share/` with the kit and the macOS plist with `packaging/launchd/`. The selftest builds the other OS's layout from the same binaries and rejects eight broken archives: a binary missing or not executable, an icon missing, a `.desktop` edited, a plist on Linux, none on macOS, a plist running another path, no `LICENSE`. Built and checked locally on Linux. |
| 2026-11-17 | T-HRD-01 | `[~]` seeded, Done check pending | The Tech Lead created key `umbral-rules-2026a` offline under `docs/runbooks/rule-signing.md` §4.1 and restored it once from each drive (sign and `rulesign verify` of a scratch bundle). `trustseed/seed.json` carries it as `key_01M7NYMYDJTK4FGC17YAJ1G6XG`, fingerprint `SHA256:KDVQHGXp79RqucL/1XN2bweYM/CsTnGyLrzNYgDDH9c`, also in the README; the fingerprint was recomputed from the public key's 32 bytes. `TestTheEmbeddedSeedCarriesTheProjectKey_REQ_SEC_011` holds the seed, the `.pub` file beside it and the README together: an emptied seed, another key or another fingerprint reddens it. `TestRuleRecoveryEndToEnd_REQ_SEC_016` asks for a trust store that starts with no key, so `startRulesWithSeed` takes the seed and that test passes an empty one. Tech 1.93 DD-016 no longer says the seed ships empty. `task ci` green on the seeded tree on 2026-11-18. Not done yet: the task's Done line — a bundle signed with the key and served locally accepted by a daemon built with the seed, one signed with another key refused and recorded in `egress_log`. |
| 2026-11-18 | T-PKG-08 | done | Tech 1.92 §9.3, delta `2026-11-deb-libffi`. CI's `deb` job failed on pull requests #78, #79 and #80: `umbrald` panicked at start with `libffi.so.8: cannot open shared object file`, because yzma's purego bindings open the library and no binary links it, so `Depends` derived from the binaries never named it and `smoke.sh` installed the package beside others that brought it in. `Depends` now names `libffi8`; `check.sh` refuses a package without it, with a mutant in its selftest; `smoke.sh` installs the package alone first, which rejected the old `.deb`; the Linux archive's `README` names the library. `task deb:smoke` (both container smokes) and `task tar` pass on the rebuilt package. |
| 2026-11-18 | T-HRD-13 | added | Eight tests failed on the runners while pull requests #79 to #82 were checked, none on code those requests changed: `REQ_RMT_001` and, on its rerun, `REQ_RMT_002` on Linux on #81, two `REQ_ACP_008` tests on macOS on #82, `REQ_ACP_006` on Linux, `REQ_ACP_009` and `REQ_FTU_003` on macOS (run 44210237570) and `REQ_AUT_004` on Windows (run 44213787204). Three had been seen once before and passed on a rerun, which is how they stayed open. No cause is known yet; the task asks for one before any change. |
| 2026-11-18 | T-REL-03, T-HRD-14, T-HRD-15, T-HRD-16, T-HRD-17 | added | The Tech Lead ratified deltas `2026-11-signed-releases` and `2026-11-agent-sandbox` as written. The first adds REQ-PKG-009 (PRD 1.24, Tech 1.94 §9.3) and T-REL-03, the release workflow's first run, which has not happened. The second adds the sandbox's four tasks; its `SBX` requirements are not written yet, because the profile depends on what the spike (T-HRD-14) measures, and T-HRD-15 writes them before its code. Both deltas' open questions stay open with their recommendations, and one more is open for the Tech Lead: neither delta says whether T-REL-03 or the sandbox tasks gate release 0.1, and the plan's hardening section has not been updated for them. |
| 2026-11-18 | T-REL-03, T-HRD-14, T-HRD-15, T-HRD-17, T-HRD-18, T-HRD-19, T-HRD-20 | answered, added | Delta `2026-11-open-questions`, approved in advance by the Tech Lead: an answer for each of the twelve questions deltas `2026-11-signed-releases`, `2026-11-agent-sandbox` and `2026-11-deb-libffi` left open. What gates release 0.1 was not among them and stays the Tech Lead's: the delta proposes that T-REL-03 does and the sandbox does not, and leaves T-HRD-18 open. The sandbox's first slice confines files only; `full-auto` without a sandbox is allowed and disclosed; `[sandbox] mode = "off"` is an opt-out. Branch protection waits for T-HRD-13. Reading the libffi binding for the Windows and macOS questions found that both systems load the library from the user's cache directory without checking it: T-HRD-18. T-HRD-19 is the go-arch-lint notice, T-HRD-20 the model relay. |
| 2026-11-19 | T-HRD-13 | done | Every failure was reproduced by widening its window — with the runner's own message, except `REQ_FTU_003`, reproduced as the state its refusal comes from — then fixed, and no timeout was raised. **Two were product defects.** `REQ_RMT_001`: `Manager.Add` read the host's state back after starting the link, so a fast link answered `connected` where API §5.44 says `connecting`; it now answers the state it created (`TestAddAnswersConnectingHoweverFastTheLinkIs_REQ_RMT_001`). `REQ_FTU_003`: `run_command` returned when its block closed, before the shell's prompt was ready, and a `terminal_interact` call decided in between was refused as decided on another terminal; it now returns once the prompt is drawn, at most 500 ms later, and an ACP terminal's command is not waited for (delta `2026-11-run-command-prompt`, proposed, Tech 1.96 §5.3b; `TestRunCommandReturnsWithTheShellAtItsPrompt_REQ_FTU_003`, red on every run with a 300 ms prompt). **The rest were test defects.** `REQ_AUT_004`: the wait's clock starts at registration and the test's started after it. `REQ_ACP_006`, `REQ_ACP_008` (both tests) and `REQ_ACP_009`, one cause: the fake agent started its connection before storing it, and a request handled in between found none and the process panicked, exit status 2; its stdin is now held until the connection is stored (90 data races in 12 parallel copies before, none in 2304 launches after). `REQ_RMT_002`: the test awaited `session.unsubscribed` and then `remote.state_changed`, which two observers of the drop send in no order API §5.44 fixes; when the state change came first it was dropped and the test waited 120 s for another. Delaying the unsubscription by 300 ms failed it every time with "no nas disconnected"; it now awaits both together, and API 1.62 §5.44 says there is no order. Each test ran 50 times at `-cpu 1,2,4` under the race detector on Linux; macOS and Windows are the runners' run of the pull request, which the Done line now says (the delta's decision 6, for the Tech Lead). The reproductions were temporary edits and are not in the tree. Under 12 parallel copies three other ACP tests time out (`TestAnUnresponsiveACPAgentIsKilledAfter5s`, `TestAnAgentWaitingOnUmbralIsNotSilent`, `TestASilentACPAgentEndsTheTurn`): overload, never seen on a runner, and left alone. |
| 2026-11-19 | T-HRD-13 | ratified | The Tech Lead ratified delta `2026-11-run-command-prompt`, its changed Done line included, and merged pull request #88, whose run was green on `ubuntu-latest`, `macos-latest` and Windows at the first attempt. `develop` and `main` are protected since the same day: changes land through pull requests only, administrators included, with no force push or deletion (answer R4 of delta `2026-11-open-questions`; required checks and the `v*` tag restriction are not switched on). |
| 2026-11-19 | T-HRD-14 | done | `docs/spikes/2026-11-landlock.md`, measured on Ubuntu 26.04.1 with kernel 7.0 (Landlock ABI 8) through a throwaway helper; no product code. Decision 5's profile as written broke every redirect to `/dev/null` and `git commit` in a worktree, which writes into the main copy's `.git` (`worktrees/<name>`, `objects`, `refs`, `logs`; with those four a hook or the main copy's config still cannot be written). `go build` and a local `npm install` ran without their caches; bash, fish and OpenCode started with nothing refused; zsh under oh-my-zsh started with its completion dump and `/tmp` files refused. The read list has to be generated, 146 rules and about 4 ms per command, and a file created later in an ancestor of a denied directory is unreadable. For T-HRD-20: OpenCode binds and connects to `127.0.0.1:4096` and does not start with TCP denied; with that port and the model's it completed a turn; Landlock cannot deny UDP. Not measured: a first start with an empty home and no TCP, two agents at once, a kernel without Landlock. |
| 2026-11-19 | T-REL-03 | `[~]` the verification step written, the first run pending | `release.yml` gained a step before the draft release: `sha256sum -c --strict`, three packages each listed in `SHA256SUMS`, and for each package `gh attestation verify` twice — build provenance and the CycloneDX SBOM — bound to this workflow, the tag's ref and its commit. The step's script was run locally against a stub `gh`: it passes with everything present and fails when either attestation is missing, a package is not in the sums, a package was changed after the sums, or an archive is missing. Not done: the run itself. The tag `v0.1.0-rc.1` is the Tech Lead's to push, from `develop` once this is merged; the flags and the SBOM's predicate type (`https://cyclonedx.org/bom`) are first exercised by that run, and so are the Done line's download check and the refusal of a tag on a stray commit. |
| 2026-11-19 | T-HRD-15 | done; its delta awaits review | Delta `2026-11-sandbox-linux` (PRD F2 0.17 §6.13, API 1.63, Tech 1.98), written first and **not approved before the code**, as the task asks: the Tech Lead asked for the implementation in the same request, so both are his to review. `internal/sandbox` has its arch rules, a domain (the profile, the four git paths, `[sandbox] write`'s check), one port and the Landlock adapter, which `umbrald --sandbox-exec` reaches. `acp` asks through its own `Confiner` port; `cmd/umbrald` builds each agent's profile. Against the kernel, and against a real daemon with a hosted agent that reads and writes without asking: writes outside the worktree and the agent's root are refused, the main copy and a hook included; the database, the token and a sibling thread's worktree are unreadable; a commit in the worktree lands. `system.status` and the thread say `enforcing`/`confined`, or `off`/`unconfined` under the opt-out, and `umb status`, `umb thread create --agent`, the panel and the desktop say it. Spec-guardian's review of the first version found two CRITICAL and two HIGH defects, each fixed with a test that was red on that version: an agent that rewrote its worktree's `.git` was given every sibling worktree at its next launch (the file is now believed only when the directory it names points back, and never for a path that reaches a protected directory); a thread whose directory held the data directory was given the database (it is no longer launched); a symbolic link beside a denied directory opened it; and through the user's D-Bus a process confined by Landlock alone ran `systemd-run --user` outside the sandbox, measured, which a seccomp filter refusing `socket(AF_UNIX)` and `io_uring_setup` closes. Also from the review: `enforcing` needs Landlock 3, the pseudo-terminals left the write list, every path is resolved, and CI's Linux job fails where its kernel cannot confine. The product's helper ran a real OpenCode turn, a commit and a build. Twenty-two mutations each reddened a test; the three symlink defences redden only together. What the sandbox does not close is listed in the delta's decision 15. Two existing tests changed: the peer check of `REQ_ORC_004` runs with the sandbox off, since a confined agent cannot read the token at all, and `FailOnce`'s marker moved into the thread's directory. Not verified here: a kernel without Landlock (the `unavailable` path is covered by the adapter's unit test and the opt-out only); macOS and Windows report `unsupported_os`, by reading. Two reviews of the first commit — an automated security review and spec-guardian's second pass — found ways out through the repository's `.git`, which the first version let a worktree's agent write so that it could commit: a rewritten `commondir` made the daemon's own git run a repository of the agent's, and a replace ref or a rewritten loose object landed content the user had not reviewed. The profile now holds nothing of `.git` — a confined agent cannot commit, and what lands is its files (decision 2) — and the worktree engine names the common directory, reads only the worktree's own record and reads no replacement object, for an agent that is not confined. Metadata operations are not mediated (decision 15). Three tests failed on the Windows runner for their POSIX paths and one for a one-second margin; fixed. |
| 2026-11-19 | T-HRD-19 | done | v1.19.2 checks the types a `cmd` package hands to a module's constructor as arguments, and reported `localSuggester` and `blockOutput` passed to `activeai.New`; every other module takes its ports inside its `Config`, which the linter does not follow. The notice was right about the shape and wrong about a violation: `cmd/umbrald` is where adapters are wired (Art. 3). `activeai.Config` now carries `Model` and `Blocks` like the rest, with no change of behaviour; the pin moved to v1.19.2 in `Taskfile.yml` and `ci.yml`, and `task arch` and `task arch:selftest` pass on it. Stacked on T-HRD-15's branch, whose `.go-arch-lint.yml` it needs. |
| 2026-11-19 | T-HRD-18 | done; its delta awaits review | Delta `2026-11-libffi-load` (Tech 1.100 §9.3). The binding loads libffi in its own `init`, before any of Umbral's code, so the change is in a copy of it, `third_party/ffi`, used through a `replace`: the file at the cache path is loaded only when it is a regular file with exactly the embedded bytes, anything else is written over, an unusable cache falls back to a private directory, and no bare name is loaded on macOS or Windows, `FFI_NO_EMBED` or not. `scripts/ffi_copy_check.sh` compares the rest of the copy, the four libraries included, with upstream v0.8.0. Not closed: the instant between the check and the load. The tests of the check run on every system; the one that reads what the process itself loaded runs on macOS and Windows in CI only. |
| 2026-11-19 | T-HRD-17 | done; its delta awaits review | Delta `2026-11-full-auto-sandbox` (PRD F2 0.19 REQ-SBX-006, API 1.65, Tech 1.101). A process takes a sandbox for good, so the thread's shell is replaced by a confined one at the first command after it enters `full-auto`, and by a plain one after it leaves, through the mechanism that already replaces it on a change of directory. Measured against a real daemon and the kernel: a `full-auto` command that writes outside its worktree runs unasked and is refused, and the same line approved in `normal` writes. `egress_visibility = confined` and its migration are **not** added: no thread has a network profile until T-HRD-20, which is where the delta moves them (decision 9) — a departure from this task's title, for the Tech Lead. The Done line's two halves are met on Linux; macOS runs the same test on its runner. |
| 2026-11-19 | T-HRD-16 | done; its delta awaits review | Delta `2026-11-sandbox-macos`. The same profile rendered for `/usr/bin/sandbox-exec`, behind the port T-HRD-15 wrote. The Done line is met: T-HRD-15's tests, and the kernel tests of this adapter, pass on `macos-latest` (pull request #95, merged 2026-11-20). The mark was left for T-HRD-17's branch, so the three branches above it did not each need a rebase. |
| 2026-11-20 | T-HRD-17 | closed | spec-guardian's review of the first commit reproduced an escape by running it: confinement was decided from where the shell started, and a `full-auto` command could replace the thread's directory by a link out of the worktree and get a plain shell for its next command. Fixed in `68256aa`: confinement is read from the turn's own mode or the thread's record, never the cwd, and `TestAFullAutoCommandCannotSwitchItsConfinementOff_REQ_SBX_006` holds it against the kernel. The delta, Tech Design §5.3 and §5.3f, this task's title, What and Files, and T-HRD-20's text now say what was built: `egress_visibility = confined` and its migration are T-HRD-20's. The desktop says what the agent panel says, and `docs/user/full-auto.md` is new. **Found while closing, not fixed, for the Tech Lead:** the delta said a hosted thread cannot be in `full-auto`; it can (T-F2-08), and its terminals inside the worktree then run unasked in a shell the sandbox does not confine — the delta's open question, with three ways to close it, now **T-HRD-21**. The review's request for a test that `thread.update` refuses `full-auto` to a hosted thread was not met: there is no such refusal. Two Windows failures of the branch were tests' own (a helper in a unix-only file, a POSIX profile rendered without its skip). |
| 2026-11-20 | T-HRD-15, T-HRD-16, T-HRD-17, T-HRD-18 | ratified | The Tech Lead ratified deltas `2026-11-sandbox-linux`, `2026-11-sandbox-macos`, `2026-11-libffi-load` and `2026-11-full-auto-sandbox` as written, the recommendations of their open questions included, and they are archived. Two things the ratification does not settle: whether a hosted agent's process survives a live handoff with its sandbox, which `2026-11-sandbox-linux` lists as not checked, and a hosted thread in `full-auto`, which is T-HRD-21. Pull requests #89 to #97 were merged the same day. |
| 2026-11-20 | T-HRD-21 | done; its delta awaits review | The Tech Lead chose option A of the three the question offered. Delta `2026-11-hosted-terminal-sandbox` (PRD F2 0.20 REQ-SBX-007, Tech 1.102), written with the code. Tests first: a real daemon and the test agent against Landlock — in `full-auto` a terminal that writes outside the worktree runs unasked and is refused by the kernel, its write inside lands, and in `normal` the same line asks and, approved, writes. The launcher needed no change: a terminal is a shell the command is typed into, so the session is created confined as the thread's own shell is. A hosted agent holds several terminals, so each gets a directory of its own instead of the thread's being emptied. spec-guardian's review found no escape and two gaps, both closed before the commit: the mark's passage through the ACP host had no test, and a terminal's directory was never removed — the confiner's port now hands back a cleanup. Ten mutations, each killed by a test. Not done: the kernel's refusal between two live terminals is not measured, and a killed daemon leaves its live terminals' directories. The review also read a race in the daemon's own file writes, which is T-HRD-22. |
| 2026-11-20 | T-HRD-22 | added | Read, not reproduced, by spec-guardian's review of T-HRD-21: the daemon's own file writes, performed unconfined on a path decided a moment earlier, follow a link a confined process puts in the worktree between the decision and the write. Older than the sandbox and outside T-HRD-21's diff; it goes around what REQ-SBX-001, 006 and 007 bound. |
| 2026-11-20 | T-HRD-20 | its delta approved | The Tech Lead answered the five questions of delta `2026-11-model-relay` as recommended ("Como recomiendas"): the port rule's reach accepted and disclosed, no redaction in the relay, a new column instead of a rebuilt `threads`, the key kept by the daemon, and no gate on release 0.1. The task is three pieces, a, b and c, and nothing of it is implemented. The delta stays in `changes/` until its last piece folds it into the specs. |
| 2026-11-20 | T-HRD-20 | piece a done | `Profile.Net` on Linux and macOS, with no caller (PRD F2 0.21 REQ-SBX-008, Tech 1.103 §5.3f). Tests first, against Landlock: a probe that tries each kind of socket, a connect and a bind on an allowed and a refused port, a bind on port 0, each right on the other's port, a listen without a bind and a fast-open send. Stricter than the delta in three places — the socket filter is an allow list, since a stream need not be TCP; macOS opens a port to TCP only; macOS leaves out the Mach services that use the network for their caller. **One new limit, for the Tech Lead:** a process that may bind keeps `listen`, and a socket that listens without a bind gets a port of the kernel's choosing; Landlock has no right for it. Three of these were found by the commit's automated security review; spec-guardian's then found the IF branch untested and a profile with a network passed through unconfined where nothing is enforced, both fixed. The delta's new section amends an approved delta and awaits the Tech Lead. macOS is verified by its runner only. Pieces b and c are not started. |
