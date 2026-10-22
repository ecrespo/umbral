# Delta — the worktree engine

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-22 by the implementing agent under the Tech Lead's standing F2 instruction; folded into Tech 1.56 (§3.2, §5.1, §5.2)` |
| **Date** | 2026-10-22 |
| **Task** | T-F2-02 |
| **REQ** | REQ-WT-001, REQ-WT-003, REQ-WT-004, REQ-WT-005, REQ-WT-006, REQ-WT-008 |

## Decisions the PRD and the task file left open

1. **How a squash lands without checking out a branch.**
   - The engine writes the worktree's whole working tree as a tree object. It uses an index file
     of its own, so the worktree's index is never touched. The tree holds what was committed in
     the worktree, what was modified and what is untracked, minus `.gitignore`.
   - It wraps that tree in a commit whose parent is the base. It is never signed and never
     referenced, so git's garbage collection drops it.
   - In the main copy, which is on the origin branch and clean, it runs
     `cherry-pick --no-commit`, then `commit -m <message>`.
   - A conflict, or a failed commit, is undone with `reset --hard HEAD`. That restores the copy
     exactly, because it was clean before.
   - git 2.34 has no `merge-tree --write-tree`, so a three-way merge without a working copy was
     not an option.
2. **"Uncommitted changes" means tracked changes, staged or not.**
   - Untracked files are the user's, and the merge leaves them alone.
   - A squash that adds a path where the main copy already has a file is refused as `conflict`,
     naming the paths, before anything is applied. The engine checks this itself: git refuses to
     overwrite an untracked file, but overwrites one `.gitignore` excludes (the user's `.env`),
     and the rollback's `reset --hard` would then delete it. The spec-guardian review
     reproduced that loss; `TestAMergeNeverOverwritesAnIgnoredFile_REQ_WT_004` is its test.
3. **A successful merge removes the worktree's directory and branch, and marks the row
   `merged`.**
   - The work is on the origin branch now. Keeping the directory would leak one copy per merge.
   - The row is marked first: a directory left behind is harmless, an `active` row over landed
     work is not.
4. **A worktree that changed nothing merges without a commit.** The result's `commit` is empty and
   the row is `merged`. The same holds when the origin branch already carries exactly the
   worktree's change, so nothing is staged after the cherry-pick.
5. **Five refusal reasons the PRD does not name.** T-F2-03 writes them into API §8:
   - `no_commit` (`VALIDATION_ERROR`): the repository has no commit, so there is no `HEAD` to
     start from;
   - `exists` (`CONFLICT`): the thread already has an active worktree;
   - `not_active` (`CONFLICT`): the worktree is merged, discarded or missing;
   - `bad_thread` (`VALIDATION_ERROR`): the thread id is not `thr_` followed by letters and
     digits, so it cannot name a branch and a directory;
   - `changed` (`CONFLICT`): the merge named a reviewed revision the worktree no longer holds
     (decision 19).
6. **A hunk's id does not depend on its position.**
   - It is the first 16 hex digits of the SHA-256 of the file's path, the hunk's lines and its
     occurrence among identical hunks of the file. Its line numbers are left out.
   - So a hunk keeps its id when an earlier hunk grows. That is what REQ-DSK-002's per-hunk
     decisions need.
7. **Which git commands are sanitised.**
   - **Showing a worktree** runs with fsmonitor, filter drivers, external diff drivers and
     textconv off, under REQ-CTX-003's rule. This covers its status, its diff and the snapshot
     the diff reads.
   - **Landing it** runs as the user's own git would: the merge's snapshot with their filters
     (an LFS pointer stays a pointer), the cherry-pick, and the commit with their hooks and
     signing.
   - **Creating it** runs `post-checkout` as `git worktree add` always does.
8. **"No terminal".**
   - Every git runs with standard input on `/dev/null`, `GIT_TERMINAL_PROMPT=0` and
     `GIT_EDITOR=false`.
   - Every git runs in a session of its own (`setsid`), so nothing it starts can open `/dev/tty`.
   - At the timeout the whole process group is killed, and `Wait` gives up on pipes a stray
     child keeps, after 2 s.
9. **The engine serialises create, merge and discard.** Two threads landing into one working copy
   never interleave a cherry-pick and a reset.
10. **A worktree created on a detached `HEAD` records an empty origin branch.** Its merge is refused
    `detached` or `other_branch` until the user lands it by hand.
11. **`GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE`, `GIT_COMMON_DIR` and `GIT_OBJECT_DIRECTORY` are
    dropped from the daemon's environment** before any git runs. One inherited from the shell
    that started `umbrald` would point every command at another repository.
    `GIT_ALTERNATE_OBJECT_DIRECTORIES`, `GIT_CONFIG_PARAMETERS` and `GIT_CONFIG_COUNT`,
    `GIT_CONFIG_KEY_<n>` and `GIT_CONFIG_VALUE_<n>` are dropped too: they would carry
    configuration past the read path's overrides. `LC_ALL=C` is set for every git, hooks
    included, because the engine reads git's messages.
12. **Missing.** An active worktree is marked `missing` when any of these is gone: its directory,
    its branch, or the repository itself. That is checked on every access and by `CheckAll` at
    start (REQ-WT-006). T-F2-03 wires `CheckAll` into `cmd/umbrald`'s start and
    `[worktrees] commit_timeout` into the engine; neither is called before then.
13. **The worktree's `.git` file is never read.** The agent writes inside its worktree, so that
    file could name a git directory of the agent's choosing, whose configuration runs a filter,
    an fsmonitor or a hook outside the policy engine (Art. 5).
    - Every command that reads the worktree runs with `GIT_DIR` and `GIT_WORK_TREE` pinned to
      the main repository's own record of it, `<common dir>/worktrees/<name>/gitdir`.
    - The diff of the snapshot runs in the main copy.
    - The filter drivers the read path turns off are listed from the configuration the command
      will use, read with the same pinned environment. Listing them from the worktree's own
      `.git` would miss the main repository's drivers, which would then run on the agent's
      files. `TestShowingARedirectedWorktreeRunsNoFilter_REQ_WT_003` is the test.
    - Removal deletes the recorded directory itself and then runs `git worktree prune`, instead
      of `git worktree remove`, which reads that file. Prune also drops any other stale record
      of the repository, as `git gc` would.
    - `TestARedirectedWorktreeGitDirIsNotFollowed_REQ_WT_004` is the test. The security review
      found this.
14. **A commit that has moved `HEAD` has landed.** A post-commit hook runs after `HEAD` moves; if
    the timeout or a failure comes then, the merge answers with the new commit and marks the row
    `merged`. Rolling back would lose a commit the user's git made.
15. **The snapshot's private index starts as a copy of the worktree's own.** Files whose stat
    information has not changed are not hashed again. Showing a worktree runs with filters off,
    so hashing an LFS file afresh would show its content where the repository holds a pointer.
    A file the agent modified is still hashed as it is on disk when shown, and through the
    user's clean filter when landed.
16. **The diff pins `--src-prefix=a/` and `--dst-prefix=b/`** against a user's `diff.noprefix`,
    and the parser unquotes C-style paths.
17. **A `worktree add` that fails part way is undone.** A post-checkout hook runs after the
    checkout, so a failure can leave the directory and the branch, which would refuse every
    retry.
18. **A version check that could not run git is not remembered.** Only a supported version or an
    `old_git` refusal is kept for the life of the daemon.
19. **A merge can require the revision that was reviewed.**
    - The diff answers with a `revision`, the tree its content was read as.
    - A merge that passes it is refused `changed`, landing nothing, if the worktree no longer
      holds that content. The agent writes in its worktree, and could otherwise slip work in
      between the review and the merge.
    - A merge without a revision lands what the worktree holds then, as before.
    - T-F2-03 carries `revision` on `worktree.diff` and `worktree.merge`, has `umb worktree
      merge` pass the revision it showed, and refuses a merge while the thread's turn is running.
    - `TestAMergeRefusesAWorktreeChangedSinceItsReview_REQ_WT_004` is the test.

## Folded into

- Tech 1.56 §3.2: the `worktrees` row.
- Tech 1.56 §5.1: `[worktrees] commit_timeout`.
- Tech 1.56 §5.2: the `worktrees` dependency rule.
- `docs/ARCHITECTURE.md` §9: the tree.
