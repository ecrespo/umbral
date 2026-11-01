-- 0010 — declarative plugins installed by digest (T-F2-60; PRD F2 REQ-PLG-001, REQ-PLG-002;
-- Data Model §2.15, delta `2026-10-plugins`).
--
-- The bundle itself lives in `plugins/<name>/` beside the database; this row is its record.
-- `manifest_json` is the parsed manifest, so listing plugins reads no file. The servers and
-- skills a plugin adds are ordinary `mcp_servers` and `skills` rows, named in its manifest.
CREATE TABLE plugins (
  id            TEXT PRIMARY KEY CHECK (id LIKE 'plg\_%' ESCAPE '\'),
  name          TEXT NOT NULL UNIQUE CHECK (
                  length(name) BETWEEN 1 AND 32 AND name NOT GLOB '*[^a-z0-9]*'),
  version       TEXT NOT NULL CHECK (length(version) BETWEEN 1 AND 64),
  description   TEXT NOT NULL CHECK (length(description) BETWEEN 1 AND 1024),
  source        TEXT NOT NULL,              -- the path it was installed from, display only
  sha256        TEXT NOT NULL CHECK (length(sha256) = 64),
  size_bytes    INTEGER NOT NULL CHECK (size_bytes BETWEEN 1 AND 8388608),
  manifest_json TEXT NOT NULL CHECK (json_valid(manifest_json)),
  installed_at  INTEGER NOT NULL,
  -- 'installing' from the row until its last server and skill are in; a row still so at start
  -- is an install a crash stopped, which recovery undoes (Data Model §6 step 10).
  state         TEXT NOT NULL DEFAULT 'installing' CHECK (state IN ('installing', 'installed'))
);
