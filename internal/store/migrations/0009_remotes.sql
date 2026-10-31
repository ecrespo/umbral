-- 0009 — remote daemons reached over SSH (T-F2-41; PRD F2 REQ-RMT-001…003; Data Model §2.14).
--
-- A host is its name and its URL and nothing else: its link state lives in the daemon's memory,
-- and the remote daemon's token is asked for over ssh at every connection and never stored. The
-- name prefixes every id the host's daemon hands out (`nas/w1:p2`), so it holds no `/`.
CREATE TABLE remotes (
  name       TEXT PRIMARY KEY CHECK (
               length(name) BETWEEN 1 AND 32
               AND substr(name, 1, 1) BETWEEN 'a' AND 'z'
               AND name NOT GLOB '*[^a-z0-9-]*'),
  url        TEXT NOT NULL CHECK (url LIKE 'ssh://_%'),
  created_at INTEGER NOT NULL
);
