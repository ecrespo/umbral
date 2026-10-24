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

### [ ] T-PKG-06 · `umbrald` as a `systemd --user` service
- **What:** a `systemd --user` unit for `umbrald`, shipped in the `.deb` under
  `/usr/lib/systemd/user/` and not enabled by default; `umb` keeps autostarting the daemon
  when the unit is not in use (Tech §9, delta `2026-10-deb-package`).
- **REQ:** Tech §9 (0.1 distribution); no REQ
- **Files:** `packaging/deb/**`, `packaging/systemd/`
- **Depends on:** T-PKG-03
- **Done:** in the Ubuntu 26.04 smoke container with a user manager, `systemctl --user start
  umbrald` serves `umb status`, a stop leaves no process, and `dpkg -c` lists the unit.

### [ ] T-PKG-07 · tar.gz archives and the `launchd` plist
- **What:** the Linux and macOS tar.gz archives of the three binaries, the Linux one with the
  kit's `share/` tree, and a `launchd` agent plist for `umbrald` in the macOS one (Tech §9,
  Plan's Hardening phase, delta `2026-10-deb-package`).
- **REQ:** Tech §9 (0.1 distribution); REQ-PKG-001, REQ-PKG-002 for the Linux archive's `share/`
- **Files:** `packaging/tar/**`, `packaging/launchd/`
- **Depends on:** T-PKG-03
- **Done:** each archive is built in CI on its OS and unpacks to binaries that print their
  version; the plist passes `plutil -lint`.

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

### [ ] T-HRD-01 · The project's rule-signing key
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

### [ ] T-HRD-07 · Destructive patterns for `umb rules` and `umb mcp remove`
- **What:** Q2 and Q6: built-in patterns `umb_rules_change` (`umb rules key add|rotate|remove`, `rollback`, `reset`) and `umb_mcp_remove`, defence in depth for the Tech §5.3 residual; the daemon's refusal stays the control, and an approved command still ends `PERMISSION_DENIED` when the peer check sees the caller. Tech §5.3's list gains both. (delta `2026-10-hrd02-open-questions`).
- **REQ:** REQ-SEC-005, REQ-CLI-008
- **Files:** `internal/security/domain/policy_destructive.go`, `policy_test.go`, Tech §5.3
- **Depends on:** T-HRD-06
- **Done:** each command matches its pattern and `umb rules status`/`umb mcp list` do not; `policy_test.go`'s `umb mcp remove x` moves to the destructive list.

### [ ] T-HRD-08 · `skill_load` checks the stored copy's digest
- **What:** Q3: `ReadSkill` reads the stored bundle once, hashes it against the installed digest and serves the entry from that same read; a mismatch refuses the load, names the skill and tells the user to reinstall it. (delta `2026-10-hrd02-open-questions`).
- **REQ:** REQ-SKL-005
- **Files:** `internal/context/adapters/skills/**`
- **Depends on:** —
- **Done:** a test that edits the stored copy after install is refused, red first.

### [ ] T-HRD-09 · Catalog descriptions as third-party text
- **What:** Q4: the `# Skills` section of the system prompt wraps the catalog in a delimited block whose preamble says the descriptions are third-party text, not instructions. Taint stays for content the user never saw. (delta `2026-10-hrd02-open-questions`).
- **REQ:** REQ-SKL-005
- **Files:** `internal/context/**` (the prompt builder)
- **Depends on:** —
- **Done:** a prompt test shows the block and preamble, and a description that tries to close it cannot.

### [ ] T-HRD-10 · No lost last line in an agent's zsh
- **What:** Q9: the daemon marks the sessions it owns for an agent, and the zsh bootstrap runs `unsetopt PROMPT_SP` after the user's rc in those sessions only; a `precmd` hook re-applies it only if a framework turns it back on at every prompt. (delta `2026-10-hrd02-open-questions`).
- **REQ:** REQ-AGT-007
- **Files:** `internal/sessions/adapters/shellinteg/**`, `shell/zsh/`
- **Depends on:** —
- **Done:** `run_command` of `printf x` in a zsh thread PTY returns `x`, red first; skipped where zsh is absent; a user's own pane keeps `PROMPT_SP`.

### [ ] T-HRD-11 · A hosted OpenCode starts without its own update traffic
- **What:** Q10: a manifest field of fixed variables the user's `env` refs cannot override; the OpenCode manifest sets its `OPENCODE_DISABLE_*` switches. Its delta amends PRD F2 REQ-ACP-009's environment allowlist with "the manifest's fixed variables". (delta `2026-10-hrd02-open-questions`).
- **REQ:** REQ-ACP-009
- **Files:** `internal/acp/**` (the manifest), `specs/prd/umbral-f2.md`
- **Depends on:** —
- **Done:** a test that the fixed variables reach the agent and a ref cannot override them; the disclosure stays true.

### [ ] T-HRD-12 · The disclosure names the readable worktrees
- **What:** Q11: what `umb thread create --agent` and the agent panel show for a hosted thread adds that the agent can read the repository's main copy and every other thread's worktree, as PRD F2 0.9 REQ-ACP-007 now says. (delta `2026-10-hrd02-open-questions`).
- **REQ:** REQ-ACP-007
- **Files:** `cmd/umb/thread.go`, `internal/tui/**` (the hosted-thread notice)
- **Depends on:** —
- **Done:** both texts carry the sentence, checked by their existing disclosure tests.

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

## Traceability matrix (hardening)

| REQ | Tasks | Verification |
|---|---|---|
| REQ-PKG-001 | T-PKG-01, T-PKG-03 | `icons` job; the `deb` job's `packaging/deb/check.sh` (`dpkg -c`, each file compared with the kit), `check_selftest.sh` and `smoke.sh` |
| REQ-PKG-002 | T-PKG-02, T-PKG-03 | `desktop-file-validate`, on the kit's file and on the packaged one |
| REQ-PKG-003 | T-PKG-02, T-PKG-03 | `.desktop` review in CI; `packaging/deb/check.sh` step 3 on the packaged file |
| REQ-PKG-006 | T-PKG-01 | the `icons` job regenerates the kit with `build.py` and compares byte for byte; checksums alone would pass a source edit committed with its new checksum |
| REQ-TUI-001, 002, 003, 004 | T-REL-02 (walk) | `docs/qa/f1-tui.md`; the harness tests are F1's |
| REQ-SEC-011, REQ-SEC-013 | T-HRD-01 (the key) | a bundle signed with the project key accepted, any other refused |
| REQ-SEC-005, REQ-SKL-005, REQ-CLI-008 | T-HRD-02 (decisions) | per decision |
| REQ-ORC-004 (hosted agents) | T-HRD-05 | TestAHostedAgentIsRefusedTheUsersMethods_REQ_ORC_004, TestAHostedAgentIsKnownByItsSession_REQ_ORC_004, TestAHostedAgentsExitEndsWhatItLeft_REQ_ORC_004, TestAReusedPidStaysListedForItsNewerAgent_REQ_ORC_004 |
| REQ-SEC-005, REQ-ACP-003 | T-HRD-06, T-HRD-07 | TestDestructiveEvasionsAsk_REQ_SEC_005, TestARunCommandEvasionAsks_REQ_SEC_005, TestAnACPExecuteEvasionAsks_REQ_ACP_003; the new patterns |
| REQ-SKL-005 | T-HRD-08, T-HRD-09 | an edited stored copy refused; the delimited catalog block |
| REQ-AGT-007 (zsh) | T-HRD-10 | `printf x` returned whole in a zsh thread PTY |
| REQ-ACP-009, REQ-ACP-007 | T-HRD-11, T-HRD-12 | fixed manifest variables; the disclosure's worktree sentence |
| REQ-AGT-018, REQ-API-005, REQ-API-004, REQ-CLI-001 | T-HRD-03 | TestFetchUrlFollowsFiveRedirects_REQ_AGT_018; TestAPlainCutMatchesTheEncoder_REQ_API_005, TestAPlainCutEncodesAFewTimes_REQ_API_005 and the macOS job over repeated runs; `task schema`; TestAnUnboundedStreamKeepsEveryNotification_REQ_CLI_001 |
| REQ-API-006 | T-HRD-04 | TestASlowClientHoldsUpNoOtherClient_REQ_API_006, TestASlowClientIsClosedNotTrimmed_REQ_API_006 |

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
| 2026-10-24 | T-HRD-06 | done | Delta `2026-10-shell-parser` (Tech 1.69 §5.2, §5.3). The normaliser parses the line with `mvdan.cc/sh/v3/syntax`, the one vendor a domain may use; 95 evasions — quoted separators, `env -S`, substitutions, globs, braces, launchers, shell options, interpreters, carriers, redirections on any statement, history, prompt variables, arithmetic — ask on both paths, each red first; nine mutations each redden the table. The vendor is a proposed Art. 3 amendment and REQ-SEC-005/REQ-ORC-004 gain a sentence, both for the Tech Lead. A word built at run time now asks, which costs `always` for lines with variables or globs. |
