-- 0005 — the agent subdomain (T-F1-01; Data Model §2.4c-2.4f, §2.5-2.13, §5).
--
-- Every statement below is the Data Model's DDL, copied, not paraphrased: the specification is
-- the contract (Art. 6) and a column spelled differently here would be a divergence nothing
-- reports. What is not here, on purpose:
--
--   * `threads` itself, which 0001 created because `sessions.owner_thread_id` and
--     `blocks.thread_id` point at it (finding A-01). Its two attention columns arrive by ALTER
--     below, since 0001 is applied and forward-only migrations cannot reach back into it.
--   * the workspace tree (§2.4b), which is 0003's.
--
-- `skills` is created now although T-F1-34 is the first to write it, so that task never has to
-- edit a migration that has already run somewhere (delta `2026-09-skills-cli`).

-- Attention (REQ-AGT-016, REQ-NTF-002). SQLite cannot add a CHECK in an ALTER, so the writer
-- enforces attention_state's values: 'idle','working','blocked','done','unknown'.
ALTER TABLE threads ADD COLUMN attention_state TEXT NOT NULL DEFAULT 'idle';  -- REQ-AGT-016
                             -- one of 'idle','working','blocked','done','unknown'
ALTER TABLE threads ADD COLUMN seen_at INTEGER;  -- when a client last focused it; NULL = never

-- External state authority and display metadata (REQ-INT-002 to REQ-INT-005).
CREATE TABLE pane_state_reports (
  pane_id    TEXT NOT NULL REFERENCES panes(id) ON DELETE CASCADE,
  source     TEXT NOT NULL,
  agent      TEXT,
  state      TEXT NOT NULL CHECK (state IN ('idle','working','blocked','done')),
  message    TEXT,
  seq        INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (pane_id, source)
);

CREATE TABLE pane_metadata (
  pane_id    TEXT NOT NULL REFERENCES panes(id) ON DELETE CASCADE,
  source     TEXT NOT NULL,
  key        TEXT NOT NULL CHECK (length(key) BETWEEN 1 AND 32),
  value      TEXT NOT NULL CHECK (length(value) <= 80),
  expires_at INTEGER,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (pane_id, source, key)
);
CREATE INDEX idx_pane_metadata_expiry ON pane_metadata(expires_at) WHERE expires_at IS NOT NULL;

-- Provenance and trust for rule material (REQ-SEC-011 to REQ-SEC-016). Public keys only.
CREATE TABLE trust_keys (
  id          TEXT PRIMARY KEY,
  public_key  BLOB NOT NULL,                 -- Ed25519, 32 bytes
  fingerprint TEXT NOT NULL UNIQUE,          -- SHA-256 shown on add and rotate
  added_at    INTEGER NOT NULL,
  revoked_at  INTEGER
);

CREATE TABLE rule_bundles (
  version      INTEGER PRIMARY KEY,          -- strictly increasing; a downgrade is rejected
  sha256       TEXT NOT NULL,
  source       TEXT NOT NULL CHECK (source IN ('builtin','remote','local')),
  verified_with TEXT REFERENCES trust_keys(id),
  installed_at INTEGER NOT NULL,
  active       INTEGER NOT NULL DEFAULT 0 CHECK (active IN (0,1))
);
CREATE UNIQUE INDEX idx_rule_bundles_active ON rule_bundles(active) WHERE active = 1;

-- Skills installed for Umbral's agent (REQ-SKL-001 to REQ-SKL-007).
CREATE TABLE skills (
  id           TEXT PRIMARY KEY CHECK (id LIKE 'skl\_%' ESCAPE '\'),
  name         TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 64 AND name GLOB '[a-z0-9]*' AND name NOT GLOB '*[^a-z0-9-]*'),
  description  TEXT NOT NULL CHECK (length(description) BETWEEN 1 AND 1024),
  version      TEXT CHECK (version IS NULL OR length(version) <= 64),
  source       TEXT NOT NULL,              -- the path it was installed from, display only
  sha256       TEXT NOT NULL,
  size_bytes   INTEGER NOT NULL CHECK (size_bytes BETWEEN 1 AND 8388608),
  enabled      INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  installed_at INTEGER NOT NULL
);

-- A thread's conversation (REQ-AGT-011, REQ-AGT-015, REQ-SEC-006).
CREATE TABLE messages (
  id               TEXT PRIMARY KEY CHECK (id LIKE 'msg\_%' ESCAPE '\'),
  thread_id        TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  turn_id          TEXT NOT NULL,
  role             TEXT NOT NULL CHECK (role IN ('user','assistant','tool','system_note')),
  content          TEXT NOT NULL,
  client_msg_id    TEXT,                          -- ULID from the client; idempotency (REQ-AGT-015)
  attachments_json TEXT NOT NULL DEFAULT '[]',   -- [{kind, ref, bytes, truncated_bytes}]
  tainted          INTEGER NOT NULL DEFAULT 0 CHECK (tainted IN (0,1)),  -- REQ-SEC-006
  created_at       INTEGER NOT NULL
);
CREATE INDEX idx_messages_thread_created ON messages(thread_id, created_at);
-- Partial, so the many rows without a client id do not collide with each other (A-04).
CREATE UNIQUE INDEX idx_messages_client_msg ON messages(thread_id, client_msg_id)
  WHERE client_msg_id IS NOT NULL;

CREATE TABLE tool_calls (
  id             TEXT PRIMARY KEY CHECK (id LIKE 'tc\_%' ESCAPE '\'),
  thread_id      TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  message_id     TEXT NOT NULL REFERENCES messages(id),
  tool           TEXT NOT NULL,
  risk           TEXT NOT NULL CHECK (risk IN ('ReadOnly','WriteFS','Exec','Network')),
  args_json      TEXT NOT NULL,
  status         TEXT NOT NULL CHECK (status IN ('pending','ok','error','denied_by_user','denied_by_policy','invalid_args')),
  result_summary TEXT,
  result_json    TEXT,
  block_id       TEXT REFERENCES blocks(id),
  started_at     INTEGER NOT NULL,
  ended_at       INTEGER
);
CREATE INDEX idx_tool_calls_thread ON tool_calls(thread_id, started_at);

CREATE TABLE approvals (
  id             TEXT PRIMARY KEY CHECK (id LIKE 'apr\_%' ESCAPE '\'),
  thread_id      TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  tool_call_id   TEXT NOT NULL UNIQUE REFERENCES tool_calls(id),
  tool           TEXT NOT NULL,
  risk           TEXT NOT NULL,
  reason         TEXT NOT NULL CHECK (reason IN ('policy','destructive','tainted','outside_write_root')),
  summary        TEXT NOT NULL,
  diff           TEXT,                        -- REQ-AGT-012
  state          TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','approved','denied','expired')),
  decision_scope TEXT CHECK (decision_scope IN ('once','thread','always')),
  created_at     INTEGER NOT NULL,
  decided_at     INTEGER
);
CREATE INDEX idx_approvals_pending ON approvals(created_at) WHERE state = 'pending';

-- Persisted decisions and configured rules (security.Decide).
CREATE TABLE policy_rules (
  id         INTEGER PRIMARY KEY,
  thread_id  TEXT REFERENCES threads(id) ON DELETE CASCADE,   -- NULL = global ('always')
  tool       TEXT NOT NULL,                  -- 'run_command', 'edit_file', 'mcp_gitlab_*'
  pattern    TEXT NOT NULL DEFAULT '*',      -- glob over command or path
  decision   TEXT NOT NULL CHECK (decision IN ('allow','deny')),
  source     TEXT NOT NULL CHECK (source IN ('user_decision','config')),
  created_at INTEGER NOT NULL
);
CREATE INDEX idx_policy_rules_lookup ON policy_rules(tool, thread_id);

-- The model catalog, and what every call cost (REQ-LLM-005).
CREATE TABLE models (
  id                           TEXT PRIMARY KEY,   -- 'ollama/gpt-oss:20b'
  provider                     TEXT NOT NULL,
  local                        INTEGER NOT NULL CHECK (local IN (0,1)),
  caps_json                    TEXT NOT NULL,      -- {tools, vision, reasoning, json_schema}
  context_window               INTEGER NOT NULL,
  price_in_micro_usd_per_mtok  INTEGER NOT NULL DEFAULT 0,
  price_out_micro_usd_per_mtok INTEGER NOT NULL DEFAULT 0,
  health                       TEXT NOT NULL DEFAULT 'unknown' CHECK (health IN ('ok','degraded','down','unknown')),
  updated_at                   INTEGER NOT NULL
);

CREATE TABLE usage (
  id             INTEGER PRIMARY KEY,
  thread_id      TEXT REFERENCES threads(id) ON DELETE SET NULL,
  turn_id        TEXT,
  model_id       TEXT NOT NULL,
  provider       TEXT NOT NULL,
  status         TEXT NOT NULL CHECK (status IN ('ok','error','timeout','rate_limited','invalid_tool_call')),
  in_tokens      INTEGER NOT NULL DEFAULT 0,
  out_tokens     INTEGER NOT NULL DEFAULT 0,
  first_token_ms INTEGER,
  cost_micro_usd INTEGER NOT NULL DEFAULT 0,
  error          TEXT,
  created_at     INTEGER NOT NULL
);
CREATE INDEX idx_usage_model_created ON usage(model_id, created_at);
CREATE INDEX idx_usage_thread        ON usage(thread_id);

-- What left the machine (REQ-SEC-002). Insert-only, except for retention.
CREATE TABLE egress_log (
  id             INTEGER PRIMARY KEY,
  thread_id      TEXT,
  provider       TEXT NOT NULL,
  host           TEXT NOT NULL,
  bytes          INTEGER NOT NULL,
  payload_sha256 TEXT NOT NULL CHECK (length(payload_sha256) = 64),
  created_at     INTEGER NOT NULL
);
CREATE INDEX idx_egress_created ON egress_log(created_at DESC);

-- MCP servers the agent is a client of (REQ-MCP-001 to REQ-MCP-003).
CREATE TABLE mcp_servers (
  id            TEXT PRIMARY KEY CHECK (id LIKE 'mcp\_%' ESCAPE '\'),
  name          TEXT NOT NULL UNIQUE CHECK (name GLOB '[a-z0-9_-]*'),
  transport     TEXT NOT NULL CHECK (transport IN ('stdio','http')),
  command       TEXT,
  args_json     TEXT NOT NULL DEFAULT '[]',
  url           TEXT,
  env_refs_json TEXT NOT NULL DEFAULT '{}',   -- {"TOKEN":"keyring:umbral/gitlab"}
  trust         TEXT NOT NULL DEFAULT 'untrusted' CHECK (trust IN ('trusted','untrusted')),
  state         TEXT NOT NULL DEFAULT 'connecting' CHECK (state IN ('connecting','connected','unavailable','disabled')),
  last_error    TEXT,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  CHECK ((transport = 'stdio' AND command IS NOT NULL) OR (transport = 'http' AND url IS NOT NULL))
);
