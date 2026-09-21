-- 0004 — what a restart needs, and nothing else (T-F0-18, REQ-TERM-009, REQ-TERM-010,
-- REQ-TERM-011; Data Model §2.4b, §2.4d, §6).
--
-- It is 0004 and the agent subdomain moves to 0005 because migrations are forward-only
-- (Art. 6) and neither file was written: renumbering an unwritten migration costs nothing,
-- while making an F0 requirement wait for F1's table does not work. Delta
-- `2026-09-restore-semantics` records it, and it is the same move
-- `2026-09-structure-migration` made when the structure tables took 0003.

-- Focus survives a restart. Before this it lived in the workspaces service and was lost, so
-- a client reopened on whichever workspace sorted first rather than the one in use. There is
-- no singleton row: the state is already per workspace, and the focused workspace is the one
-- with the greatest focused_at.
ALTER TABLE workspaces ADD COLUMN focused_tab_id TEXT REFERENCES tabs(id) ON DELETE SET NULL;
ALTER TABLE workspaces ADD COLUMN focused_at INTEGER;

-- A stored command that Umbral has not run.
--
-- REQ-TERM-011 is the reason this column exists rather than being derived: a restart must
-- never re-execute a command on its own, so the daemon has to remember, across restarts,
-- that a pane's command is still waiting. `layout.apply` and restore set it; `pane.split`
-- does not, because a client asking for a command now is asking for it to run.
--
-- SQLite cannot add a column with a non-constant default, and DEFAULT 0 is what every
-- existing pane should have: those panes' commands were launched when they were created.
ALTER TABLE panes ADD COLUMN command_pending INTEGER NOT NULL DEFAULT 0
  CHECK (command_pending IN (0, 1));

-- The recent screen of each pane, for REQ-TERM-010's opt-in replay.
--
-- One row per pane, replaced on each capture rather than appended: the requirement is "the
-- stored recent screen", not a history, and an append-only table of screens would grow
-- without bound while holding exactly the secrets the setting is disabled by default to
-- avoid. ON DELETE CASCADE means closing a pane forgets its screen with no sweeper.
CREATE TABLE pane_history (
  pane_id     TEXT PRIMARY KEY REFERENCES panes(id) ON DELETE CASCADE,
  screen_zst  BLOB NOT NULL,
  rows        INTEGER NOT NULL,
  captured_at INTEGER NOT NULL
);
