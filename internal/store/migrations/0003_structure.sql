-- 0003 — the workspace tree: workspaces, tabs, panes and pane aliases (T-F0-14,
-- REQ-WS-001, REQ-WS-002, REQ-WS-003, REQ-WS-007; Data Model §2.4b).
--
-- It is 0003 and not part of 0001 because 0001 is already applied to every developer's
-- database and migrations are forward-only (Art. 6). The agent subdomain that older drafts
-- numbered 0002 is 0004 for the same reason; delta `2026-09-structure-migration` records
-- the renumbering.
--
-- The identifiers are the exception the Art. 6 amendment of 2026-09-20 authorises: `w<n>`,
-- `w<n>:t<m>` and `w<n>:p<m>` instead of prefixed ULIDs, because these are names a person
-- types at a prompt. The CHECK constraints are what keep that grammar from being a
-- convention the code can quietly drift from — SQLite has no regex, so the pair of GLOBs on
-- `workspaces` is how "w followed by digits and nothing else" is said: the first requires
-- the shape, the second forbids any character outside it.

CREATE TABLE workspaces (
  id          TEXT PRIMARY KEY CHECK (id GLOB 'w[0-9]*' AND id NOT GLOB '*[^w0-9]*'),
  label       TEXT NOT NULL,
  cwd         TEXT NOT NULL,
  order_index INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL,
  closed_at   INTEGER
);

CREATE TABLE tabs (
  id           TEXT PRIMARY KEY CHECK (id GLOB 'w[0-9]*:t[0-9]*'),
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  label        TEXT NOT NULL,
  order_index  INTEGER NOT NULL DEFAULT 0,
  layout_json  TEXT NOT NULL DEFAULT '{}',   -- portable tree (API Layout)
  created_at   INTEGER NOT NULL,
  closed_at    INTEGER
);
CREATE INDEX idx_tabs_workspace ON tabs(workspace_id, order_index);

CREATE TABLE panes (
  id          TEXT PRIMARY KEY CHECK (id GLOB 'w[0-9]*:p[0-9]*'),
  tab_id      TEXT NOT NULL REFERENCES tabs(id) ON DELETE CASCADE,
  session_id  TEXT REFERENCES sessions(id),   -- NULL while the pane has no live session
  label       TEXT,
  cwd         TEXT NOT NULL,
  command_json TEXT,                          -- argv used to relaunch it on restore
  env_json    TEXT NOT NULL DEFAULT '{}',
  order_index INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL,
  closed_at   INTEGER
);
CREATE INDEX idx_panes_tab ON panes(tab_id, order_index);

-- One pane per session, enforced rather than assumed: Data Model §2.4b says a pane hosts at
-- most one live session, and the partial index is what stops a move or a restore from
-- attaching the same terminal twice. It is partial because many panes legitimately share
-- the NULL that means "no session yet".
CREATE UNIQUE INDEX idx_panes_session ON panes(session_id) WHERE session_id IS NOT NULL;

-- REQ-WS-007: a moved pane keeps its terminal and gets a new identifier, and the old one
-- stays resolvable for the life of that terminal. The cascade is what "for the life of that
-- terminal" means in DDL — the aliases die with the pane, never before it.
CREATE TABLE pane_aliases (
  alias_id   TEXT PRIMARY KEY CHECK (alias_id GLOB 'w[0-9]*:p[0-9]*'),
  pane_id    TEXT NOT NULL REFERENCES panes(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL
);
