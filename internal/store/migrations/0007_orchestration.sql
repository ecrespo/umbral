-- 0007 — orchestration: worktrees, parallel threads and hosted ACP agents (T-F2-01; PRD F2
-- REQ-WT-001, REQ-ORC-002…004, REQ-ACP-005, REQ-ACP-007; Data Model §2.4g, §2.5, §2.7, §2.2).
--
-- No table is rebuilt. The runner applies a file in one transaction with foreign keys on, and a
-- rebuild of `threads` would have to drop a table eleven others point at. New states and flags
-- are therefore new columns. SQLite does accept a CHECK in ADD COLUMN, and it tests the CHECK
-- against the rows already there, so each column carries its own rule. A REFERENCES column must
-- default to NULL.

-- A thread's isolated copy of a repository (REQ-WT-001). `wtr_` because `wt_` is a wait's prefix.
-- A thread may have several over its life (one merged, then a new one), but only one active.
CREATE TABLE worktrees (
  id            TEXT PRIMARY KEY CHECK (id LIKE 'wtr\_%' ESCAPE '\'),
  thread_id     TEXT NOT NULL REFERENCES threads(id),
  repo_root     TEXT NOT NULL,              -- the main working copy's top level
  repo_id       TEXT NOT NULL,              -- 16 hex of SHA-256 of the realpath of the common git dir
  base_commit   TEXT NOT NULL,              -- the commit the worktree was created from
  origin_branch TEXT NOT NULL,              -- the branch a merge squashes into (REQ-WT-004)
  branch        TEXT NOT NULL,              -- umbral/<thread_id>
  path          TEXT NOT NULL,              -- $XDG_DATA_HOME/umbral/worktrees/<repo_id>/<thread_id>
  state         TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active','merged','discarded','missing')),
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);
CREATE INDEX idx_worktrees_thread ON worktrees(thread_id);
CREATE UNIQUE INDEX idx_worktrees_active_thread ON worktrees(thread_id) WHERE state = 'active';
CREATE UNIQUE INDEX idx_worktrees_active_path ON worktrees(path) WHERE state = 'active';

-- The effective mode is `full-auto` when full_auto = 1 and the worktree is active; clearing it
-- leaves `mode` as it was (REQ-ORC-004).
ALTER TABLE threads ADD COLUMN full_auto INTEGER NOT NULL DEFAULT 0 CHECK (full_auto IN (0,1));
-- A turn accepted over `[agents] max_concurrent_turns`, waiting for a slot (REQ-ORC-002).
ALTER TABLE threads ADD COLUMN queued INTEGER NOT NULL DEFAULT 0 CHECK (queued IN (0,1));
ALTER TABLE threads ADD COLUMN worktree_id TEXT REFERENCES worktrees(id) ON DELETE SET NULL;
-- The workspace the thread belongs to (REQ-ORC-003); NULL for a thread made outside one.
ALTER TABLE threads ADD COLUMN workspace_id TEXT REFERENCES workspaces(id) ON DELETE SET NULL;
-- The `[[acp.agents]]` entry hosting the thread; NULL for Umbral's own agent (REQ-ACP-001).
ALTER TABLE threads ADD COLUMN agent TEXT;
ALTER TABLE threads ADD COLUMN egress_visibility TEXT NOT NULL DEFAULT 'observed'
  CHECK (egress_visibility IN ('observed','unobserved'));  -- REQ-ACP-007

-- Who ran the call, and how it was decided (REQ-ACP-007). NULL decision: a row from before 0007.
ALTER TABLE tool_calls ADD COLUMN via TEXT NOT NULL DEFAULT 'umbral' CHECK (via IN ('umbral','acp'));
ALTER TABLE tool_calls ADD COLUMN decision TEXT CHECK (decision IN ('allow','deny','ask','unmediated'));

-- An ACP terminal's block (REQ-ACP-005); `origin` keeps its CHECK and stays 'agent'.
ALTER TABLE blocks ADD COLUMN via TEXT NOT NULL DEFAULT 'shell' CHECK (via IN ('shell','acp'));
