# Delta — Per-hunk review and the worktree's file tree

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-26` by the implementing agent under the Tech Lead's standing F2 instruction (extended to F2b … F2f on 2026-10-26), pending the Tech Lead's review |
| **Date** | 2026-10-26 |
| **Task** | T-F2-31 |
| **REQ** | REQ-DSK-002, REQ-DSK-003 |

## What the task left open

REQ-DSK-002 asks the desktop client to show a diff per hunk — an approval's or a worktree's — and
let the user accept or reject single hunks, with only the accepted ones applied. REQ-DSK-003 asks
for the thread's worktree as a file tree with the changed files marked, a file opened read-only or,
inside a worktree, editable. The protocol had no way to do either: `worktree.diff` gives hunks ids
(REQ-WT-003) but nothing takes them, an `Approval`'s `diff` is one string approved whole, and no
method reads or writes a worktree's files. The desktop client is thin (DD-001), so the daemon must
do the applying.

## Decisions

1. **One parser, one id.** `internal/hunks`, a leaf with the standard library only, holds the
   unified-diff parser and the hunk id of REQ-WT-003 (moved from `worktrees/domain`, which now
   uses it), and the two ways a subset is applied: a patch of chosen hunks, and a file's content
   with only chosen hunks applied. The worktree's diff and an approval's diff give the same id to
   the same hunk when both name the file by the same path string — the tool's path as the model
   wrote it, git's repository-relative one — and split it the same way; no code relies on it.
2. **`worktree.reject_hunks`** `{worktree_id, revision, hunk_ids}` reverts the named hunks in the
   worktree's working copy and answers the new diff, as `worktree.diff` does. Accepting is keeping:
   the hunks left are what a `merge` with the new `revision` lands, so "apply only the accepted
   hunks" is a reject followed by a merge. `revision` is required — the ids are a diff's — and a
   worktree whose content is no longer that revision is `CONFLICT` `changed`. An id the diff does
   not hold is `VALIDATION_ERROR` `unknown_hunk` (§5.41's `data.reason`); an empty list is
   `VALIDATION_ERROR`. The revert
   is `git apply -R` of a patch holding only those hunks, run through the main repository's record
   of the worktree as every worktree git call is, with the repository's configuration not read;
   it applies whole or not at all, and one that does not apply is `CONFLICT` `changed`. A binary
   file and a mode change have no hunk to name; rejecting them is `discard`. It is refused under a
   running turn (`CONFLICT` `running`), to an agent's terminal (`PERMISSION_DENIED`, §2) — a
   review is the user's — and to `umb`: the interactive clients only.
3. **An approval's hunks.** `Approval` gains `hunks`: the `diff` split as `worktree.diff`'s
   `files` are, or `null` without a diff, and `hunks_selectable`. `approval.respond` takes
   `hunks?`, the ids to accept; absent, everything is approved. It needs `decision: "approve"` and
   `scope: "once"` — a remembered rule approves whole calls to come, never part of one — and at
   least one id, each in the approval's diff (`VALIDATION_ERROR` otherwise, with no `data.reason`:
   §5.25 names none; deny to accept none).
4. **The call runs with the accepted hunks.** `write_file` and `edit_file` compute their diff again
   from the file as it is when they run; when every accepted id is in it, they write the old
   content with only those hunks applied, and the result tells the model which hunks the user
   rejected and shows the diff written. When one is not — the file changed since it was approved —
   nothing is written and the call is an error saying so. The grant carries the ids; the registry
   runs a tool that cannot apply part of itself only whole, and refuses a grant with ids for it.
   Which hunks were applied is in the tool call's stored result; no column is added.
5. **A hosted agent's write is approved whole.** An ACP `fs/write_text_file` approval carries its
   diff with `hunks_selectable: false`, and `hunks` for it is `VALIDATION_ERROR`, its message
   saying the approval is decided whole; should ids reach the hosted write anyway, it is denied. The
   request is answered with no content, so an agent whose write was cut down would go on believing
   the file holds what it sent; the user sees the hunks and approves or denies the write.
6. **`worktree.files`** `{worktree_id}` answers `{worktree_id, revision, items: [{path, status}],
   truncated}`: every path of the worktree's content — what a merge would read, so ignored files
   are not listed — with `status` `added`, `modified` or `deleted` from the same snapshot's diff,
   `null` for an unchanged file, and a deleted file listed with its status. At most 20 000 items,
   by path, `truncated` saying whether more exist.
7. **`worktree.read_file`** `{worktree_id, path}` answers `{path, size_bytes, sha256, binary,
   content, editable}`. `path` is relative, clean, without `..` and outside `.git`
   (`VALIDATION_ERROR` otherwise); the file is opened inside the worktree's directory without
   following a link out of it. `content` is the text, or `null` for a file over 1 MiB, one that is
   not UTF-8 or holds a NUL (`binary`), or one that is not a regular file. `editable` is whether
   `write_file` would take it now: a regular text file within the limit, in an active worktree
   whose thread has no turn running. Every file of the tree is inside a worktree; "read-only" in
   REQ-DSK-003 is the file `editable` is false for. A path not in the worktree is `NOT_FOUND`, a
   worktree that is not active `CONFLICT` `not_active`, and a path with a `.git` component at any
   depth or in any case, or a link among its directories, `VALIDATION_ERROR` `bad_path`.
8. **`worktree.write_file`** `{worktree_id, path, content, expected_sha256}` replaces an existing
   regular file's content with the user's text (at most 1 MiB, UTF-8) and answers `{path,
   size_bytes, sha256}`. `expected_sha256` is `read_file`'s: a file that changed since is
   `CONFLICT` `changed`. The file keeps its mode, is written through a temporary file renamed over
   it inside the worktree's directory, and is never created or reached through a link. It is the
   user's own edit, typed in the client like keys in a pane, so it is not a tool call and does not
   go through the policy engine; for the same reason it is refused to an agent's terminal and to
   `umb`, and under a running turn (`CONFLICT` `running`), which keeps the directory it started
   with.
9. **The desktop client.** The worktree view shows each hunk with a checkbox, all checked; "Reject
   unchecked" calls `reject_hunks` with the diff's revision and shows the new diff, and "Merge"
   still needs the diff read and passes its revision. An approval with hunks shows them the same
   way, and approving with some unchecked sends the checked ids. The files view lists the active
   worktree's tree with changed files marked; a file opens in a text area, read-only unless
   `editable`, and "Save" sends what was read's `sha256`.

## Tests

- `TestHunkIDsAreTheSameFromEitherDiff_REQ_DSK_002`, `TestApplyingChosenHunks_REQ_DSK_002`
  (`internal/hunks`): ids by path and lines; a content with every hunk, none, or some applied;
  a patch of chosen hunks.
- `TestRejectedHunksLeaveTheWorktree_REQ_DSK_002` (`internal/worktrees/integration`): a real
  repository; a rejected hunk reverted, the rest kept and merged; a stale revision, an unknown id
  and an added untracked file.
- `TestAnApprovalAcceptsSomeHunks_REQ_DSK_002` (`internal/agents`), `TestAWriteRunsWithTheAcceptedHunks_REQ_DSK_002`
  (`internal/tools/adapters/builtin`): only the accepted hunks written; a file changed since
  writes nothing; the validation of `hunks`.
- `TestTheWorktreesFileTree_REQ_DSK_003` (`internal/worktrees/integration`): the tree, its marks,
  a read, an edit with and without the right digest, a link and `.git` refused.
- `TestTheReviewMethodsAreTheUsers_REQ_DSK_002`, `TestAnApprovalsHunksReachTheWire_REQ_DSK_002`
  (`internal/api`): the kinds and `userOnly`; an approval's `hunks` on the wire.
- `TestTheDesktopReviewsHunksAgainstARealDaemon_REQ_DSK_002` (`cmd/umbrald`): the bridge against a
  real daemon and repository.

## Residuals

- **The page is checked by hand.** The desktop page's own behaviour — a checkbox per hunk, all
  checked; "Approve for thread" refused with a hunk unchecked; the tree's marks; the read-only
  text area — has no automated test: the bridge test drives the calls it makes against a real
  daemon, and `task desktop:smoke` only proves the window draws a pane. It is verified by hand,
  as REQ-PKG-007's icon is, until the smoke drives the views.
- **An interrupted save.** A daemon killed between writing `.umbral-edit-<nonce>` and renaming it
  leaves that file in the worktree, where a merge would land it; the diff shows it first.
- **Twice on the wire.** An `Approval` carries its diff as `diff` and again as `hunks`, so a write
  near 1 MiB with a large diff approaches the 4 MiB frame limit, as a large `diff` alone could.

## Folded into

- API 1.56: §2 (kinds), §4 `Approval`, §5.25, §5.41.
- Tech 1.79: §5.2 (`hunks` in the module and dependency tables, `*/domain` and `worktrees` rows),
  §5.3h (the desktop's review and files).
- `specs/tasks/umbral-f2-tasks.md`: T-F2-31 done, its traceability rows.

## Review

spec-guardian, 2026-10-30: FIX FIRST — `task arch` and `task lint` red, decision 5 without a test
that bites, a directory link answered `INTERNAL_ERROR`, `.git` checked at the top only, and the
residuals above. Fixed before the commit: the `hunks` rule for `worktrees-service`, one comment
per alias, `acceptable` refusing a hosted write's hunks under test and the hosted write denied
should ids reach it, `bad_path` for a link among the directories and for `.git` at any depth and
case (`TestTheWorktreesFileTree_REQ_DSK_003`), and the wording of decisions 1, 3, 5 and 7. Art. 3
reading: `hunks` is a stdlib-only leaf with no I/O, taken as a domain package, for the Tech
Lead's review with this delta.
