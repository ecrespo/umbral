# Delta — a hosted thread's terminals run inside the sandbox under `full-auto`

| Field | Value |
|---|---|
| **Status** | `PROPOSED`. The Tech Lead chose the way to close it on 2026-11-20 ("de acuerdo con la opción A": confine the terminal's shell); this text, written with the code, has not been reviewed |
| **Date** | 2026-11-20 |
| **Task** | T-HRD-21 |
| **REQ** | adds REQ-SBX-007; REQ-ORC-004 and REQ-ACP-003 are not reworded |
| **Raised by** | The open question of delta `2026-11-full-auto-sandbox`, found while closing T-HRD-17 |
| **Depends on** | T-HRD-15, T-HRD-17 |

## Evidence

- A hosted thread can be in `full-auto`: REQ-ORC-004 "applies to an ACP thread's permission
  requests too", `thread.update` lets it in on an active worktree, and since T-F2-08 a
  `terminal/create` whose directory is inside the worktree is then allowed unasked
  (`TestFullAutoDecidesACPPermissions_REQ_ORC_004`).
- A hosted agent's terminal is a session the daemon starts (`sessions.StartTerminal`), outside
  the sandbox that confines the agent's own process. It was started with no confinement in
  every mode.
- So a hosted agent in `full-auto`, confined as a process, could write anywhere the user can
  through a terminal, without a question, while its thread read `confined` and its notice
  said it "can write this thread's directory only, so it cannot commit".

## Decisions

1. **What is confined.** Each terminal a hosted agent creates while the sandbox enforces and
   its thread is in `full-auto`, read as REQ-SBX-006 reads it: from the calling turn's own
   mode, carried with the request, or from the thread's record — never from the directory
   the terminal starts in. The turn's mode is the agent runtime's mark on the request
   (`agents/ports.WithFullAuto`); `cmd/umbrald` turns it into the sessions module's, since
   neither module knows the other.
2. **Its profile** is the one a `full-auto` thread's own shell gets (delta
   `2026-11-full-auto-sandbox`, decision 3), built by the same function: the worktree, a
   directory of its own, the device files and `[sandbox] write` to write; everything but
   Umbral's data and runtime directories to read, its start-up files excepted; no local
   socket. Nothing of the repository's `.git`: a terminal that commits fails.
3. **A directory per terminal.** A thread's own shell is one, and its directory is emptied
   at each launch. A hosted agent holds up to `[acp] max_terminals` at once, so each gets
   `shells/<thread>/term-<random>`, and the thread's directory is not emptied: starting one
   terminal does not remove another's temporary files, and one terminal's profile does not
   open another's directory. A terminal's directory is removed when its session ends: an
   agent in `full-auto` opens terminals unasked, with no limit on their total, and the
   runtime directory — usually memory, with a quota — is where the socket and the locks
   live. The confiner's port hands back a cleanup for that, called with the shell's own.
4. **A terminal that cannot be confined is refused.** The agent's `terminal/create` fails
   with the reason; nothing of its command runs.
5. **Every `full-auto` terminal, asked or not.** One whose directory is outside the worktree
   asks (REQ-ACP-005) and, approved, is confined all the same, as an approved command of a
   `full-auto` thread of Umbral's own is. It can read where it starts and cannot write there.
6. **Other modes are unchanged.** In `ask`, `normal` and `auto-edit` every terminal is a
   decision of the policy or of the user, and it runs unconfined.
7. **Where the sandbox does not enforce**, nothing is confined and the thread reads
   `unconfined`, as before (S2, S4 of delta `2026-11-open-questions`).
8. **The disclosure needs no new text.** A confined hosted thread's notice says the agent
   "can write this thread's directory only, so it cannot commit"; under `full-auto` that is
   now true of its terminals as well.

## Requirements (PRD F2 0.20 §6.13)

- **REQ-SBX-007** · MUST · state — WHILE the sandbox enforces and a thread hosted by an ACP agent is in `full-auto` with its worktree `active`, THE SYSTEM SHALL start each terminal the agent creates in a shell confined before any of the shell's code runs, so that the kernel refuses it what REQ-SBX-006 refuses a `full-auto` thread's own shell, with a directory for temporary files that no other terminal of the thread can write; and IF the shell cannot be confined THEN THE SYSTEM SHALL refuse the terminal rather than start it unconfined. A hosted thread's terminal in any other mode is not confined: each of its commands is a decision (REQ-ACP-003).

## What it does not close

- **The network**, and reading the user's files, as for every confined process (decision 15
  of `2026-11-sandbox-linux`).
- **A daemon that is killed leaves its live terminals' directories behind**, under
  `shells/<thread>`, until the runtime directory is cleared. Nothing sweeps them at start.
- **The daemon's own file writes for a hosted agent.** `fs/write_text_file`, and Umbral's own
  `write_file`, are performed by the daemon, unconfined, on a path the policy resolved a
  moment earlier. Found by the review of this delta, by reading and not reproduced: a
  confined process that swaps a directory of the worktree for a link between the decision
  and the write would get a write outside the worktree. It is T-HRD-22.
- **A terminal started before the thread entered `full-auto`** keeps running as it was
  started. A terminal's command is typed once, so nothing later runs in it unasked.
- **Windows**: no sandbox, and every command line asks there (decision 14 of
  `2026-10-windows-port`).

## Spec changes

- PRD F2 0.20 §6.13: REQ-SBX-007.
- Tech 1.102 §5.3: a hosted agent's terminals under `full-auto`; §5.3f: the terminal's
  directory and the mark's path.
- No change to the API: a hosted `Thread`'s `sandbox` already says `confined`.

## Verification

- `cmd/umbrald`, a real daemon and the test agent against the kernel
  (`TestAHostedTerminalIsBoundedByTheKernelUnderFullAuto_REQ_SBX_007`): in `full-auto` a
  terminal that writes outside the worktree runs unasked and is refused, and its write
  inside lands; in `normal` the same line asks and, approved, writes.
- `internal/sessions/integration`, real shells and a stand-in confiner
  (`TestAHostedTerminalIsConfinedUnderFullAuto_REQ_SBX_007`): a terminal is launched through
  the confiner on the turn's mark or the record, plainly otherwise; a refusal starts nothing
  and runs nothing; with no sandbox enforcing nothing is confined.
- `internal/agents` (`TestAHostedTerminalCarriesItsTurnsFullAuto_REQ_SBX_007`): the request
  is marked under `full-auto` and only then; `cmd/umbrald`
  (`TestATerminalIsStartedOnItsTurnsReading_REQ_SBX_007`) carries the mark across.
- `cmd/umbrald` (`TestAHostedThreadsTerminalsEachGetADirectoryOfTheirOwn_REQ_SBX_007`): two
  terminals, two directories, the first one's files kept; a thread's own shell gets no
  cleanup.
- `internal/acp/adapters/sdk` (`TestATerminalIsStartedOnTheContextItsTurnGave_REQ_SBX_007`):
  the host starts the terminal on the context the turn gave it, the one that carries the
  mark, and not on the agent's request's.
- The cleanup: called when a terminal is released (the sessions test), and it removes that
  terminal's directory and no other's (the `cmd/umbrald` test).
- Each of the ten pieces was removed in turn and a test went red.
- Not verified: macOS, by its runner only; several terminals alive at once against the
  kernel (the directories are checked, not the kernel's refusal between them).
