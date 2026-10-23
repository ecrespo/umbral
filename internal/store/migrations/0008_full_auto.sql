-- 0008 — full-auto follows its worktree (T-F2-05; PRD F2 REQ-ORC-004; Data Model §2.5).
--
-- A thread is in `full-auto` while `full_auto = 1` and its worktree is `active`. These triggers
-- keep the flag from outliving the worktree: whatever moves a worktree out of `active` — a merge,
-- a discard, a `close_group`, the missing-check at start or at a turn — clears the flag of the
-- threads it belongs to in the same write, and so does a thread losing its worktree. `mode` is
-- left as it was, so the thread returns to the mode it held under `full-auto`.

CREATE TRIGGER worktrees_leave_full_auto AFTER UPDATE OF state ON worktrees
  WHEN OLD.state = 'active' AND NEW.state <> 'active'
BEGIN
  UPDATE threads SET full_auto = 0 WHERE worktree_id = NEW.id AND full_auto = 1;
END;

CREATE TRIGGER threads_full_auto_needs_worktree AFTER UPDATE OF worktree_id ON threads
  WHEN NEW.full_auto = 1 AND NOT EXISTS (
    SELECT 1 FROM worktrees WHERE id = NEW.worktree_id AND state = 'active')
BEGIN
  UPDATE threads SET full_auto = 0 WHERE id = NEW.id;
END;
