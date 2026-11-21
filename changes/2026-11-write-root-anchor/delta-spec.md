# Delta — the daemon's file writes and reads are reached from the write root

| Field | Value |
|---|---|
| **Status** | `PROPOSED`, written with its code on 2026-11-20 at the Tech Lead's order ("T-HRD-22, hazla ahora"); the Tech Lead's to ratify |
| **Date** | 2026-11-20 |
| **Task** | T-HRD-22 |
| **REQ** | none reworded: REQ-AGT-013 and REQ-ACP-004 as they stand; it goes around what REQ-SBX-001, 006 and 007 bound |
| **Raised by** | spec-guardian's review of T-HRD-21, which read the race and did not reproduce it |
| **Depends on** | — |

## Evidence

- `write_file`, `edit_file` and a hosted agent's `fs/write_text_file` and `fs/read_text_file`
  are performed by the daemon, which no sandbox confines. The policy decides on the target
  with its links resolved; the write then opened the target's nearest existing directory as an
  `os.Root`. That holds the last directory and the file, and nothing above them.
- Reproduced by `TestAWriteStaysUnderItsWriteRoot_REQ_AGT_013` and
  `TestAHostedWriteAndReadStayUnderTheWriteRoot_REQ_ACP_004` before the fix: with the decision
  made on `worktree/a/b/f`, `a` renamed away and replaced by a link to a directory outside
  that holds `b/f`, `write_file` and `edit_file` rewrote the file outside, a new file was
  created outside, and the hosted read returned the outside file's content. No window had to
  be widened: the write takes the decided target and resolves nothing again, so the state
  after the swap is the whole race.
- Who can do it: a process confined to the worktree — a `full-auto` thread's shell, a hosted
  agent, one of its terminals — or any command a turn runs. Under `auto-edit` and `full-auto`
  the write is unasked, and a hosted agent's read inside the write root is unasked in every
  mode. Tech §5.3 said a write "fails if a component or the target became a link since the
  check", which the code did not do for a component above the last directory.

## Decisions

1. **A target inside the write root is reached from the write root.** The daemon opens the
   thread's write root as an `os.Root` and writes or reads the target's path below it. A link
   met on the way that leads out of it is refused, whenever it appeared; one that stays
   inside is followed, as before.
2. **Inside is the policy's own test**: lexical, on the target with its links resolved at the
   decision, against the write root with its links resolved too, as the policy takes it. What
   the policy let run unasked and what the write is bounded by are the same reading. A
   thread's write root is held as the user spelled it, which may go through a link — a
   temporary directory on macOS does — and spec-guardian's review found the first version of
   this change comparing against that spelling, so that such a root never contained its own
   files and the write fell back to its nearest directory. Reproduced, then fixed.
3. **A target outside the write root** was approved by a person, or is a read by Umbral's own
   agent, which needs no approval anywhere. It is reached from its nearest existing
   directory, as every write was before. Its directories above that one can still be
   replaced by whoever can write them; a process confined to the worktree cannot.
4. **Reads too.** `read_file` and `edit_file` read the target the grant was checked against,
   resolving nothing again, through the same root. Before, `read_file` resolved its path a
   second time when it ran.
5. **The write root travels with a hosted request.** The runtime hands the performer the
   write root it decided against beside the target (`ExternalSink.Request`'s `perform`, the
   ACP `Files` port), since the code that opens the file is not the code that decided.

## What it does not close

- `grep`, `glob` and `list_dir` walk a directory by path. They are `ReadOnly` for Umbral's
  own agent, which may read anywhere unasked, so a link gains it nothing; no hosted request
  maps to them.
- A hard link inside the write root to a file outside it: a hosted read returns the file's
  content, which no directory root can refuse. A write is safe: the new content is renamed
  over the name, which breaks the link instead of writing through it. Making the link needs
  a file the process may already read.
- A directory replaced between the temporary file's creation and its rename leaves a
  `.name.umbral-*` file behind, inside the write root.
- Context assembly reads the rules files by path after resolving them. It is a read for
  Umbral's own agent, as the searches are.
- The write root itself replaced by a link: its parent is outside what a confined process
  can write, and without a sandbox a command can already write anywhere the user can.
- Windows: `os.Root` is used the same way; not measured there beyond the suite's own run.

## Spec changes

- Tech 1.104 §5.3 ("Targets follow symlinks") and §5.3f (files of a hosted agent).

## Verification

- The two tests above, red before and green after; `TestAHostedFileRequestIsReachedFromItsWriteRoot_REQ_ACP_004`
  (the daemon's performer), `TestACPWriteOutsideTheWorktreeAsks_REQ_ACP_004` (the runtime
  hands the worktree as the root) and `TestACPFileRequestsArePerformedOnTheDecidedTarget_REQ_ACP_004`
  (the ACP host passes it on).
- Mutations: reaching from the nearest directory again, inverting the inside test, comparing
  against the root as spelled, and dropping the root at each of the runtime, the ACP host and
  the daemon's performer are each killed. Dropping it at `write_file`'s or `edit_file`'s own write is not: the read of the
  current content, made through the same root just before, refuses first.
