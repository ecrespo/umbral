# Delta — a `full-auto` thread's shell runs inside the sandbox

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-11-20 by the Tech Lead ("Ratifico los deltas que mencionas")`, as written; its open question, a hosted thread in `full-auto`, is not answered by this ratification and stays T-HRD-21. Written with its code on 2026-11-19, reviewed after it; folded into the specs and archived |
| **Date** | 2026-11-19 |
| **Task** | T-HRD-17 |
| **REQ** | adds REQ-SBX-006; REQ-ORC-004 is not reworded (see decision 7) |
| **Raised by** | Delta `2026-11-agent-sandbox` decisions 6 and 8; delta `2026-11-open-questions` S2 and S4 |
| **Depends on** | T-HRD-15, T-HRD-16 |

## Evidence

- Tech Design §5.3: in `full-auto` "any line can `cd` out or write outside through a path of
  its own. The policy cannot see either". REQ-ORC-004 lets an `Exec` whose cwd is inside the
  worktree run unasked; where its writes land is not read at all.
- A process takes Landlock's restriction, and macOS's sandbox, for good and at its start.
  A shell that is already running cannot be confined when its thread enters `full-auto`, nor
  released when it leaves.
- The spike of T-HRD-14: interactive bash, fish and zsh start under the profile; zsh makes
  its temporary files from `TMPPREFIX`, not `TMPDIR`.
- A thread's shell reads its start-up files from the daemon's runtime directory
  (REQ-TERM-012), which the profile denies whole.

## Decisions

1. **What is confined.** The shell Umbral's own thread runs its commands in, while the
   sandbox enforces and either of two readings says `full-auto`: the calling turn's own mode,
   read once as the turn starts and carried to the terminal with the call, or the thread's
   record — the flag set and its worktree active, as `TurnPlace` reads it. **Never where the
   shell starts**: the thread's directory is one a confined command can replace by a link
   out of the worktree, and a shell released on that account is the escape (see Review). A
   turn that decided under `full-auto` on a record that has changed since has no worktree to
   be confined to, and its command fails rather than run unconfined. A thread in `ask`,
   `normal` or `auto-edit` is not confined: every side effect there is a decision.
2. **A shell of the wrong kind is replaced.** The thread's shell is already replaced when its
   directory changes (REQ-WT-002). Whether it is confined joins that key: the first command
   after the thread enters or leaves `full-auto` gets a new shell. What the old one held —
   its variables, its directory — is lost, as on a change of directory. A shell the user has
   taken over is never closed under them (REQ-FTU-002): the command is refused until they
   hand it back.
3. **Its profile** is the hosted agent's, built by the same function: it writes the thread's
   worktree, a directory of its own under the runtime directory for temporary files
   (`shells/<thread>`, emptied at each launch), the device files and `[sandbox] write`; it
   reads everything but Umbral's data and runtime directories; it reaches no local socket.
   So nothing of the repository's `.git`: a command that commits fails, and what lands is
   the worktree's files. The shell's own directory is checked, at each launch, to be the one
   the daemon just made and no link left in its place. **Its environment is the daemon's**,
   as an unconfined shell's is: confinement bounds what the shell writes and reads, not what
   it was told, so a variable the daemon's own environment holds reaches the confined shell.
4. **A read entry.** The profile gains `read`: directories inside a denied one that are read
   and run from, never written. The only kind accepted is a shell's own start-up directory,
   `shellinteg-*` directly inside the runtime directory; a sibling's worktree, an agent's
   home or the token as a read entry is refused when the profile is built.
5. **`TMPDIR` and `TMPPREFIX`** point into the shell's own directory.
6. **A shell that cannot be confined is not started.** The command fails with the reason;
   it is not run unconfined in a thread that reads as confined. Where the sandbox does not
   enforce at all — another system, an old kernel, `[sandbox] mode = "off"` — `full-auto`
   runs as before, degraded and disclosed (S2, S4).
7. **REQ-ORC-004 stands as written.** It says what runs without approval; this delta adds
   what bounds it. Art. 5's "a sandbox or a disposable worktree" is now both where the
   sandbox enforces.
8. **Disclosure.** A thread's `sandbox` is `confined` or `unconfined` while it is in
   `full-auto`, as it is for a hosted thread, and null in any other mode. The agent panel
   shows it on the thread's row.
9. **`egress_visibility = confined` is not added here.** Delta `2026-11-agent-sandbox`
   decision 8 gives it to "a hosted thread whose network profile the kernel enforces", and
   no thread has a network profile until T-HRD-20: the value would be in the schema, in a
   migration that cannot be undone (Art. 6), with nothing to set it. T-HRD-20 adds it with
   the profile that makes it true.

## Requirements (PRD F2 0.19 §6.13)

- **REQ-SBX-006** · MUST · state — WHILE the sandbox enforces and a thread of Umbral's own
  agent is in `full-auto` with its worktree `active`, THE SYSTEM SHALL run that thread's
  commands in a shell confined before any of the shell's code runs, so that the kernel
  refuses it a write outside the worktree, a directory of its own for temporary files, the
  device files a shell needs and `[sandbox] write`, and a read of Umbral's data and runtime
  directories other than its own start-up files; SHALL replace a shell that is not so
  confined before the thread's next command, and one that is before the next command of a
  thread that has left `full-auto`; and IF the shell cannot be confined THEN THE SYSTEM
  SHALL fail the command rather than run it unconfined.

## What it does not close

- **`terminal_interact` under `full-auto`** goes through the same shell and is confined with
  it. A program the thread started before entering `full-auto` and left running in a shell
  the user took over stays as it was.
- **A hosted agent's terminals** are Umbral's own sessions, started by the daemon outside
  the agent's sandbox, and are not confined by this delta. Each of their commands is a
  decision (REQ-ACP-003) — but a hosted thread **can** be in `full-auto`: `thread.update`
  lets it in on an active worktree, and since T-F2-08 a `terminal/create` whose cwd is inside
  the worktree is then allowed unasked (`TestFullAutoDecidesACPPermissions_REQ_ORC_004`). So
  a confined hosted agent in `full-auto` is handed an unconfined shell without a question.
  The first version of this delta said such a thread could not exist; it can. See the open
  question below.
- Everything decision 15 of `2026-11-sandbox-linux` and decisions 9 and 10 of
  `2026-11-sandbox-macos` list: the network above all. A `full-auto` command can still send
  anything anywhere; `Network` *tools* ask, a command's own traffic does not.
- The shell's history file and a framework's caches are refused; the shell runs without
  them (spike).

## Review

spec-guardian's review of the first commit reproduced an escape by running it: whether the
shell was confined was decided from where it started, and a `full-auto` command could swap
the thread's directory for a link out of the worktree; the next command then got a plain
shell and wrote outside, unasked. Decision 1 is the fix (`68256aa`). The same review asked
for the environment to be stated (decision 3), for the desktop to say what the agent panel
says, and for a page in `docs/user/`; all three are in.

It also asked for a test that `thread.update` refuses `full-auto` to a hosted thread. No
such refusal exists and none is added: the behaviour is REQ-ORC-004's as T-F2-08 built it
and the Tech Lead ratified it. The test was not written; the question is raised instead.

## Open, for the Tech Lead

1. **A hosted thread in `full-auto`.** Its agent is confined (T-HRD-15); its terminals are
   not, and under `full-auto` those inside the worktree run unasked. Three ways to close it:
   confine an ACP terminal's shell with the same profile while the thread is in `full-auto`
   (the launcher refuses a confined session with a command today, so the command would be
   typed into a confined shell, as `run_command`'s is); make every `terminal/create` ask
   while the sandbox enforces; or refuse `full-auto` to a hosted thread, which undoes part
   of T-F2-08. The first keeps what was ratified. None is built here.

## Spec changes

- PRD F2 0.19 §6.13: REQ-SBX-006.
- API 1.65 §4: a `Thread`'s `sandbox` covers Umbral's own thread in `full-auto`.
- Tech 1.101 §5.3: `full-auto`'s boundary, now the kernel's where the sandbox enforces;
  §5.3f: the read entry, and what a shell's confinement is read from.

## Verification

- `cmd/umbrald`, a real daemon with a scripted model, against the kernel (Linux here, macOS
  on its runner): a `full-auto` command that writes outside its worktree is run unasked and
  refused by the kernel, its write inside lands and its `TMPDIR` is its own; back in
  `normal`, the same line, approved, writes outside. `thread.update` answers `confined` and
  then null.
- `internal/sessions/integration`, real shells and a stand-in confiner: the shell is kept
  while the answer holds and replaced when it changes, both ways; the confiner is given the
  shell's start-up directory; a refusal fails the command and runs nothing.
- The profile's read entry: the domain refuses every kind but a session's own start-up
  directory; under Landlock it is read and sourced, not written, and opens none of its
  neighbours; the macOS renderer reopens it for reading only.
- The review's escape, as a test against the kernel
  (`TestAFullAutoCommandCannotSwitchItsConfinementOff_REQ_SBX_006`): a `full-auto` command
  replaces the thread's directory by a link out of the worktree, and the next command is
  still confined; `run_command` and `terminal_interact` both carry the turn's mode to the
  terminal (`TestACommandCarriesItsTurnsFullAuto_REQ_SBX_006`).
- Each rule was removed in turn and a test went red, the one that keeps the kernel's
  refusal from being the policy's included: with the confiner switched off the file outside
  is written.
- Not verified: zsh with a framework and fish through the daemon (the spike ran them through
  its own helper); the hosted-agent branch of the confiner's decision, by reading only; a
  taken-over shell at the moment the thread enters `full-auto`.
