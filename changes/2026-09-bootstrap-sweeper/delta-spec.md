# Delta — where a shell's bootstrap files live, and who removes the ones a crash leaves

| Field | Value |
|---|---|
| **Status** | `PROPOSED 2026-09-20 — pending ratification` |
| **Date** | 2026-09-20 |
| **Task** | T-F0-19 (new) |
| **Raised by** | The F0 final verification of 2026-09-20: 2515 orphaned bootstrap directories, 168 MB, found on the development machine. Nothing in the specification package says where those files live or who removes them. |

## Evidence

**The measurement.** `docs/checkpoints/2026-09-20-f0-closure.md` records it: 2515
`/tmp/umbral-shellinteg-*` directories, 168 MB, one per session, each holding the generated
rc file the shell was launched with. They accumulated over a few weeks of development on one
machine.

**The normal path already cleans up.** `Bootstrap.Close` removes the directory and
`sessions.Service` calls it when the session's shell exits
(`internal/sessions/lifecycle.go:276`). Driving the real daemon confirms it: a clean `SIGTERM`
with two live sessions leaves **0** directories behind, because closing the PTY makes the
shell exit and the drain goroutine's exit handler runs in time.

**The abnormal path cannot.** `kill -9` with two live sessions leaves **2**. Nothing runs on
`SIGKILL`, by definition, and the directory name is random and recorded nowhere, so the next
daemon has no way to learn that it existed. The residue is permanent, and it grows by one
directory per live session per crash, for the life of the machine.

**This is unspecified, not merely unimplemented.** No artifact says where a bootstrap's files
live. `Prepare` uses `os.MkdirTemp("", …)`, so they land in the process's temporary directory
— `/tmp` on a normal Linux system, shared with every other program and every other Umbral
installation on the machine. The PRD, the Tech Design and the Data Model are all silent, so
there is nothing for an implementation to be wrong about, and nothing a reviewer could have
caught.

**Why a sweeper over `/tmp` would be the wrong fix.** The obvious repair is to delete
`/tmp/umbral-shellinteg-*` at start. It races: those directories are not scoped to an
installation, so a daemon sweeping them can delete the directory of a session another daemon
is starting right now, under a different `XDG_RUNTIME_DIR`. A shell reads its startup file
once, so the window is small, but "small window" is what this project's reviews keep finding
at the bottom of intermittent failures. An age heuristic narrows the window without closing
it, and buys a tunable nobody can choose correctly.

## Decisions

**1. A bootstrap's files live in the runtime directory, beside the socket and the token.**
`$XDG_RUNTIME_DIR/umbral/shellinteg-<random>/` (macOS: under
`~/Library/Application Support/Umbral/`), not the shared temporary directory. That directory
is already the daemon's own, already `0700` (REQ-SEC-007), and already where everything else
belonging to one installation lives. On Linux it is usually `tmpfs` cleaned at logout, which
is a second safety net rather than the mechanism.

**2. The daemon removes every one it finds at start, unconditionally, while holding the
instance lock.** The lock is the whole argument and it already exists: `AcquireInstanceLock`
is `flock` on `umbrald.lock` in that same directory, the kernel drops it when the holder dies
however it dies, and a daemon that cannot take it exits 0 without serving. So at the moment a
daemon holds the lock it is the only daemon of this installation, every `shellinteg-*` under
its runtime directory belongs to a process that is gone, and deleting them needs no age
heuristic, no ownership check and no guessing. A race that cannot be constructed does not
need a window.

**3. The sweep runs after the lock and before the socket is bound.** Concretely: after
`AcquireInstanceLock` and before `Restore`, so the sessions the restore creates cannot have
their own directories swept from under them.

**4. A sweep that fails is logged and startup continues.** A directory that cannot be removed
— a permission the user changed, a filesystem error — is a few kilobytes of litter. Refusing
to serve over it would turn a cosmetic problem into an outage, and the daemon is what the
user is waiting for.

**5. No configuration.** No retention knob and no opt-out. There is nothing to tune: the
files are worthless the instant their shell is gone, and the lock makes "is it safe" a
question with one answer.

## Requirement

Added to PRD §6.1:

> **REQ-TERM-012** · MUST · event — WHEN the daemon starts, THE SYSTEM SHALL delete the shell
> bootstrap directories left in its runtime directory by previous runs, before it restores the
> workspace tree, so that a daemon killed without a chance to clean up does not leave files
> that nothing will ever remove. THE SYSTEM SHALL create those directories inside its own
> runtime directory rather than the shared temporary directory, and SHALL log and continue
> when one cannot be removed.

## Not modified

The normal path is unchanged: `Bootstrap.Close` still removes the directory when the shell
exits, and it remains the mechanism that keeps a long-running daemon tidy. The sweeper is for
what a crash leaves, and the two are not alternatives — without `Close`, a daemon serving for
a month would hold every directory it ever made until its next restart.

`Service.Shutdown` is also unchanged. It looks like it should run the bootstrap cleanup and it
does not; a change was written for it during the verification and reverted, because the
measurement shows a clean shutdown already leaks nothing and the change altered no observable
behaviour. That reasoning is recorded in the checkpoint so the next reader does not re-derive
it from the code and reach the opposite conclusion.

## Phase

**F0, as `T-F0-19`.** The code is F0's — the bootstrap is T-F0-08's and the sessions that own
it are T-F0-05's — and the phase is already open on two of its own exit criteria, so this adds
no sequencing cost. The alternative, the hardening phase, runs after F1: that would ship the
MVP with a path that grows without bound after every crash, for the sake of a change that is a
path constant and a loop over one directory.

## Verification

- `TestBootstrapDirectoriesLiveInTheRuntimeDirectory_REQ_TERM_012` — `Prepare` creates its
  directory under the runtime directory and not under `os.TempDir()`.
- `TestStartSweepsOrphanedBootstrapDirectories_REQ_TERM_012` — a directory planted in the
  runtime directory before the daemon starts is gone once it is serving.
- `TestSweepLeavesTheRunningSessionsAlone_REQ_TERM_012` — the directories of the sessions the
  restore creates survive the start that created them, which is what decision 3's ordering
  buys.
- `TestSweepFailureDoesNotStopTheDaemon_REQ_TERM_012` — a directory that cannot be removed is
  logged and the daemon serves anyway.
- Teeth, measured rather than assumed: `kill -9` on a daemon with live sessions leaves
  directories behind, and the next start removes them. Before this delta the same sequence
  left them forever, which is the residue the verification found.
