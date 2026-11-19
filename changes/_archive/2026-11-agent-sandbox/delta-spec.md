# Delta — a sandbox around hosted agents and `full-auto`

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-11-18 by the Tech Lead ("Si, ratifico el PR 85, ratifico el 84." — PR #84 carried this delta — and, asked whether that covered the deltas themselves, "Si, cubre también los deltas.")`, as written, and archived. Nothing is implemented. Q1 to Q4 stay open with their recommendations; the spike (T-HRD-14) measures what Q1 and Q3 need, and the Tech Lead answers them before any product code. Answered since by delta `2026-11-open-questions` |
| **Date** | 2026-11-17 |
| **Tasks** | T-HRD-14 (spike), T-HRD-15, T-HRD-16, T-HRD-17 (renumbered on 2026-11-18: T-HRD-13 was taken that day by the tests that fail on a runner) |
| **REQ** | will amend REQ-ACP-007, REQ-ACP-009 and REQ-ORC-004 and add requirements under a new `SBX` prefix. None is written yet: the profile they state depends on what the spike measures, so T-HRD-15 writes them in the `**REQ-…** · MUST` form before its code (in a delta of its own: a step added when this one was archived) |
| **Folded into** | `specs/tasks/umbral-hardening-tasks.md` (T-HRD-14 … 17); no PRD, API or Tech Design text yet |

## Evidence

- **A hosted ACP agent runs unconfined.** `internal/acp/adapters/sdk/host.go` starts the
  `[[acp.agents]]` command with `exec.Command`, the manifest's environment and `Setsid`
  (`tree_unix.go`); nothing limits what the process reads, writes or connects to. Umbral
  decides what the agent *asks* for (REQ-ACP-003, REQ-ACP-009); an agent that does not ask —
  a bug, a prompt injection, a changed default — is not stopped.
- **`full-auto` is bounded by text, not by the kernel.** Tech Design §5.3: "any line can `cd`
  out or write outside through a path of its own. The policy cannot see either: the worktree is
  Art. 5's disposable boundary for what `full-auto` allows, not a sandbox." Art. 5 says
  `full-auto` "SHALL only run inside a sandbox or a disposable worktree"; today it is the
  second, and the worktree limits where work is *meant* to land, not where it can.
- **A hosted agent reads the main copy and every other worktree** (T-HRD-12 discloses it), and
  **its own traffic is unobserved** (`egress_visibility = unobserved`, the Art. 4 amendment of
  2026-10-22). T-HRD-11 switched off OpenCode's update traffic; its provider package still
  comes from npm.
- PRD F2 Q-F2-08 already names the answer and defers it: "A sandbox around the agent's process
  (bubblewrap, landlock, seatbelt) is a later hardening, not an F2 requirement."

## Decisions proposed

1. **A `sandbox` port, per-platform adapters, wired in `cmd/umbrald`.** The port takes a
   command and a profile — write roots, read roots, network — and returns the command to run
   instead. `acp` and `sessions` receive it; neither knows the mechanism (Art. 3).
2. **Linux: Landlock, applied by a re-exec helper.** `umbrald --sandbox-exec <profile> -- <cmd>`
   restricts itself and then execs the command, because the restriction must be taken by the
   process that becomes the agent. Landlock needs no setuid binary and no user namespace —
   Ubuntu restricts unprivileged user namespaces through AppArmor, which is what bubblewrap
   needs. File rules need kernel 5.13; TCP rules need 6.7.
3. **macOS: `sandbox-exec` with a generated profile.** Deprecated by Apple and still what
   other agent tools use; the adapter is small and replaceable.
4. **Windows: out of scope here.** Hosted agents already run in a Job Object
   (`2026-10-windows-port`); an AppContainer is a later delta.
5. **The profile.**
   - *Write:* the thread's worktree (or its directory when it has none), a private temporary
     directory, the agent's own state directory under the isolation root REQ-ACP-009 gives it,
     and what `[sandbox] write` adds — build caches are the expected entries.
   - *Read:* everything the user can read, **minus** the other threads' worktrees and Umbral's
     data directory (the database, the token, the trust store). That closes T-HRD-12's
     disclosure for sibling worktrees; the repository's main copy stays readable, since the
     worktree's own `.git` points into it.
   - *Network:* see Q1.
6. **What is confined.** (a) every hosted ACP agent, always; (b) an Umbral thread's own shell
   while the thread is in `full-auto`. A thread in `ask`, `normal` or `auto-edit` is not
   confined: every side effect there is already a decision.
7. **When the sandbox is unavailable** — an older kernel, Landlock disabled — the daemon reports
   a degraded mode in `system.status`, as Art. 5 already requires for the keyring fallback, and
   the thread's disclosure says the agent is unconfined. Whether `full-auto` is then refused is
   Q2.
8. **`egress_visibility` gains `confined`** for a hosted thread whose network profile the kernel
   enforces; `unobserved` stays for the rest. The Art. 4 amendment stands: Umbral still does not
   *record* the agent's own requests, it bounds where they can go.

## Open questions for the Tech Lead

- **Q1 — the network profile.** Landlock's TCP rules name ports, not hosts: "connect to 11434
  only" allows a local Ollama and also any host's port 11434. The options:
  (a) ports only, and say so in the disclosure; (b) no network at all and a daemon-side relay
  on a Unix socket for the model endpoint, which makes the agent's model calls Umbral's own and
  puts them in `egress_log`; (c) leave the network open in the first slice and confine files
  only. Recommended: (c) for T-HRD-15, (b) as its own task once the spike measures it.
- **Q2 — `full-auto` without a sandbox.** Refusing it would amend the ratified REQ-ORC-004 and
  break `full-auto` on macOS until T-HRD-16. Recommended: allow it, degraded and disclosed, and
  revisit once both adapters exist.
- **Q3 — the user's shell inside the sandbox.** A `full-auto` thread's shell reads the user's rc
  files; a framework that writes to `$HOME` at start would fail. The spike should measure bash,
  zsh and fish with oh-my-zsh and starship before the profile is fixed.
- **Q4 — an opt-out.** `[sandbox] mode = "off"` for a user whose toolchain the profile breaks:
  recommended, disclosed on every thread it affects, and never the default.

## Tasks proposed

- **T-HRD-14 · Spike: OpenCode and a `full-auto` shell under Landlock.** Measure what the
  profile of decision 5 breaks (the agent's start, `go build`, `npm install`, a `git commit`
  in the worktree), on Ubuntu 26.04. Output: `docs/spikes/2026-11-landlock.md` and the answers
  to Q1 and Q3. No product code.
- **T-HRD-15 · The `sandbox` port and the Linux adapter; hosted agents confined.**
- **T-HRD-16 · The macOS adapter.**
- **T-HRD-17 · `full-auto` shells confined; `egress_visibility = confined`; the disclosures.**

## Constitution check

- **Art. 3:** a new module needs its `.go-arch-lint.yml` rules before its code.
- **Art. 4:** unchanged; decision 8 narrows what the 2026-10-22 amendment leaves undisclosed.
- **Art. 5:** this is the "sandbox" its `full-auto` sentence names.
- **Art. 9:** the stack table gains nothing if Landlock is reached through `golang.org/x/sys`,
  already a dependency; a Landlock library would be a new entry and needs its own line.
