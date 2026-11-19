# Delta — the sandbox's requirements and its Linux adapter

| Field | Value |
|---|---|
| **Status** | `PROPOSED`. T-HRD-15 asks for this delta to be approved before any code; the Tech Lead asked on 2026-11-19 for T-HRD-15 to be implemented in the same request, so the code is written against this text and both await his review. Written into PRD F2 0.17, API 1.63 and Tech 1.98 as proposed |
| **Date** | 2026-11-19 |
| **Task** | T-HRD-15 |
| **REQ** | adds REQ-SBX-001 … REQ-SBX-005; amends REQ-ACP-007 and REQ-ACP-009 |
| **Raised by** | Delta `2026-11-agent-sandbox` (decisions 1, 2, 5 and 7), delta `2026-11-open-questions` (S1, S3, S4) and the spike `docs/spikes/2026-11-landlock.md` (T-HRD-14) |
| **Depends on** | T-HRD-14 |

## Evidence

`docs/spikes/2026-11-landlock.md` measured decision 5's profile on Ubuntu 26.04 and found what
it has to add and what it cannot say:

- Every redirect to `/dev/null` failed, and a commit in a worktree failed, because it writes
  into the main copy's `.git`.
- Landlock has no deny rule. "Everything minus two directories" is a generated allow list.
- Umbral keeps worktrees inside its data directory and a hosted agent's home inside its
  runtime directory, so both directories are denied and the thread's own two allowed back.

## Decisions

1. **The requirements** are REQ-SBX-001 … 005, below. They state what the kernel refuses, not
   how; Landlock is the Linux adapter's business.
2. **The write list** of a hosted agent is: its thread's directory (the worktree, or the
   thread's directory without one); its isolation root, which holds its home, its XDG
   directories and a temporary directory of its own; the device files `/dev/null`, `/dev/zero`,
   `/dev/full` and `/dev/tty` — not the pseudo-terminals, through which a process could open
   the user's other terminals, Umbral's panes included; for a worktree, the main copy's
   `.git/worktrees/<name>`, `.git/objects`, `.git/refs` and `.git/logs`; and `[sandbox] write`.
   - Never the main copy's `.git` whole: a hook written there runs as the user later.
   - `.git/refs` holds every branch. A confined agent can move another branch's ref, the
     sibling threads' included. Narrowing it needs a directory per thread under
     `refs/heads/`, a change to the worktree engine, and is an open question.
3. **The read list** is everything the user can read except Umbral's data directory and its
   runtime directory. That covers the database, the trust store, the token, every other
   thread's worktree and every other agent's home. The repository's main copy stays readable.
   - A file created after the agent starts, directly in an ancestor of a denied directory
     (`$HOME`, `~/.local`, `~/.local/share`, `/run/user/<uid>`), is not readable by it.
   - Those ancestors cannot be listed by it. A listing right there would reach the denied
     directories beneath.
4. **The agent gets `TMPDIR`**, a directory inside its isolation root. REQ-ACP-009's
   environment allowlist gains it. It is set whether or not the sandbox enforces, so an agent
   behaves the same either way.
5. **A helper applies the profile.** `umbrald --sandbox-exec <profile> -- <command>` confines
   itself and then becomes the command. A profile it cannot read or apply runs nothing: an
   agent is never launched unconfined because confining it failed.
6. **The profile is made at each launch**, from the thread's directory then.
7. **Degraded is said.** `system.status` gains `sandbox: {state, mechanism?, reason?}` with
   `state` `enforcing`, `unavailable` or `off`. A hosted `Thread` gains `sandbox`: `confined`
   or `unconfined`, `null` for Umbral's own agent. `umb status`, `umb thread create --agent`,
   the agent panel and the desktop's thread view say it.
8. **`[sandbox]`** in `config.toml`: `mode = "enforce"` (the default) or `"off"`; `write`, a
   list of clean absolute paths. An entry that is, holds or lies inside the data directory,
   the runtime directory, or the cache directory the daemon loads libffi from (T-HRD-18) stops
   the daemon at start, with the sandbox on or off.
9. **The network is not confined** in this slice (S1). The disclosure says the agent can
   reach the network.
10. **The module** is `internal/sandbox`: `domain` (the profile), `ports` (one interface) and
    `adapters/landlock`. `acp` does not import it: it declares a port of its own, `Confiner`,
    which `cmd/umbrald` adapts. No new dependency: `golang.org/x/sys/unix`.
11. **The profile believes nothing the agent can write.**
    - A worktree's `.git` file names the administrative directory a commit writes, and the
      agent can rewrite it. Its content is believed only when that directory's own `gitdir`
      file points back at this worktree's `.git`, and never for a path that reaches a
      protected directory. Otherwise the agent gets no git path. Found in review: without the
      check, an agent that rewrote `.git` to name the directory of its sibling worktrees was
      given all of them at its next launch.
    - Every path is resolved before it is compared or allowed, and a rule is never placed
      through a symbolic link.
12. **A thread that cannot be confined is not launched** (REQ-SBX-001's IF). A rule on a
    directory covers everything beneath it, so a thread whose directory holds the data or the
    runtime directory — the user's home, `/` — would be given the database. A thread working
    inside one is refused too, unless the directory is the worktree the daemon made for it,
    `<data>/worktrees/<repository>/<thread>`. The thread is kept and `agent_unavailable` says
    it cannot be confined; the user picks a narrower directory or switches the sandbox off.
    The same holds when the daemon's own executable lies inside what the agent may write: the
    next launch would run the agent's file before confining it.
13. **No Unix-domain socket.** Landlock does not mediate a connection to one. Measured: a
    process confined by Landlock alone listed the user's D-Bus and ran `systemd-run --user
    touch <file outside the profile>`, which created the file. The same road leads to the
    keyring, ssh-agent, gpg-agent and a container daemon. A seccomp filter refuses the
    process `socket(AF_UNIX)` and `io_uring_setup`, which can make a socket without the
    system call. With it the same commands fail, and OpenCode still completes a turn; a
    socketpair and Internet sockets are untouched. On Landlock 6 and later the process also
    cannot signal a process outside the sandbox or connect to an abstract socket made there.
14. **A floor for "enforcing".** Landlock before version 3 (kernel 6.2) does not mediate
    truncate, so the adapter reports `unavailable`, `landlock_too_old`, below it; and
    `unsupported_arch` where the socket filter is not written for the CPU (amd64 and arm64
    are).
15. **What the sandbox does not close**, said here because the disclosure cannot list it all:
    - the network, loopback TCP services included, and UDP;
    - a thread without a worktree, in a repository: its `.git` is inside its own directory,
      hooks and configuration with it. The four-path rule is a worktree's;
    - `stat` of a denied path, so a sibling thread's id can be probed; changing the mode,
      owner or times of a file the user owns anywhere;
    - a denied file the user hard-linked elsewhere, and a denied directory bind-mounted
      under a readable one;
    - Umbral's configuration directory, which stays readable on purpose: `models.toml` holds
      references, not secrets, and nothing there is writable.
16. **Out of this delta:** macOS (T-HRD-16); `full-auto` shells and `egress_visibility =
    confined` (T-HRD-17); the model relay (T-HRD-20); Windows.

## Requirements (PRD F2 0.17, a new §6.13)

- **REQ-SBX-001** · MUST · state — WHILE the sandbox enforces, THE SYSTEM SHALL launch every
  hosted ACP agent confined before any of the agent's code runs, so that the kernel refuses
  it a write outside its thread's directory, its isolation root, the device files a shell
  needs, the paths of the repository's main copy a commit in its worktree writes — never its hooks or its configuration — and `[sandbox] write`, and refuses it a Unix-domain socket. IF the agent cannot be confined — its directory holds Umbral's data or runtime directory, or lies inside one and is not its own worktree — THEN THE SYSTEM SHALL not launch it.
- **REQ-SBX-002** · MUST · state — WHILE the sandbox enforces, THE SYSTEM SHALL have the
  kernel refuse a hosted ACP agent reading or listing Umbral's data directory and runtime
  directory — the database, the token, the trust store, every other thread's worktree and
  every other agent's home — and SHALL leave it able to read the repository's main copy.
- **REQ-SBX-003** · MUST · unwanted — IF the system cannot confine a process or `[sandbox]
  mode = "off"`, THEN THE SYSTEM SHALL launch hosted agents unconfined, report the sandbox's
  state and the reason in `system.status`, and say on each hosted thread that its agent is
  unconfined.
- **REQ-SBX-004** · MUST · unwanted — IF a `[sandbox] write` entry is not a clean absolute
  path, or is, holds or lies inside Umbral's data directory, its runtime directory or a
  directory the daemon loads code from, THEN THE SYSTEM SHALL refuse to start.
- **REQ-SBX-005** · SHOULD · ubiquitous — THE SYSTEM SHOULD say on each confined thread what
  the sandbox leaves open: the repository's main copy to read, and the network.

## Amendments

- **REQ-ACP-007**, last item: the user is shown that the agent can read the repository's main
  copy and every other thread's worktree **unless the thread is confined**, in which case
  they are shown what the sandbox keeps from the agent and what it leaves open (REQ-SBX-003,
  REQ-SBX-005).
- **REQ-ACP-009**, the environment: the allowlist gains `TMPDIR`, a directory of the agent's
  own under its isolation root.

## Spec changes

- PRD F2 0.17: §6 names the `SBX` prefix; a new §6.13 with the five requirements; the two
  amendments.
- API 1.63: §5.2 `system.status`'s `sandbox`; §4 `Thread`'s `sandbox`.
- Tech 1.98: §5.1 `[sandbox]`; §5.2 the `sandbox` module's row; §5.3f the profile, the helper
  and the disclosure.

## Open questions

- **Narrowing `.git/refs`** to the thread's own branch (decision 2). Recommended: a task of
  its own, after T-HRD-17.
- **Whether the two unreadable-ancestor limits of decision 3 are acceptable.** Recommended:
  yes; the alternative is a listing right that shows sibling worktrees' file names.
- **Whether refusing every Unix-domain socket is too blunt** (decision 13). It also stops an
  agent's `git` over ssh with an agent, and a tool that talks to a local daemon. Recommended:
  keep it; `[sandbox] mode = "off"` is the way out, and a relay is T-HRD-20's shape.
- **Whether a thread that cannot be confined should be refused or run unconfined and say so**
  (decision 12). Recommended: refused, as written — the thread reads `confined` or it does
  not run.
- **Whether a hosted agent's process survives a live handoff** with the sandbox state of the
  daemon that launched it. Not checked.
- **Whether a daemon without Landlock should refuse to host agents** rather than run them
  unconfined. Recommended: no, as S2 answers for `full-auto`; it is disclosed.

## Verification

- `internal/sandbox/adapters/landlock`, against the kernel: writes outside the profile are
  refused and inside it land; the device files are writable; the database, the token, a
  sibling's worktree and another agent's home are refused, and the main copy is read; a commit
  in a worktree lands and a hook, the main copy's configuration, its `HEAD` and its files are
  refused; a profile that does not parse runs nothing.
- `cmd/umbrald`, against a real daemon with a hosted agent that reads and writes without
  asking: the same refusals, `system.status` and the thread's `sandbox`, and the opt-out.
- Each rule was removed in turn and its test went red: the deny list, the write rights, the
  device files, each git path, the profile check, the protected directories, the confiner,
  the socket filter and its `io_uring` line, the back pointer, the two directory checks and
  the helper's. The three defences against a link beside a denied directory — skipping it,
  not following it, and checking what was opened — hold one another up: the test goes red
  only with all three removed.
- Spec-guardian's review found two CRITICAL and two HIGH defects in the first version: the
  rewritten `.git`, a thread directory holding Umbral's data, links beside a denied
  directory, and local sockets. Each has a test that was red on that version.
- The product's helper ran a real OpenCode turn against `gpt-oss:20b`, a commit in a
  worktree and a redirect to `/dev/null` under the profile the daemon makes.
- Not verified: a kernel without Landlock or older than 6.2, by reading and a stub only;
  arm64; CI's Linux job now fails if its kernel cannot confine.
