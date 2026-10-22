-- 0006 — rule-update state (T-F1-30; Data Model §2.4e, delta `2026-10-signed-rule-bundles`).
--
-- The Data Model's DDL, copied. 0005 is applied and forward-only migrations cannot reach back
-- into it (Art. 6), so the bundle's bytes arrive by ALTER: `rules.rollback` reinstalls a
-- bundle offline, and a digest alone cannot be reinstalled.

ALTER TABLE rule_bundles ADD COLUMN content BLOB;  -- the bundle's exact bytes; NULL only for builtin

-- A bundle not to retry: rejected (REQ-SEC-013), rolled back or reset away from.
CREATE TABLE rule_rejections (
  sha256  TEXT PRIMARY KEY CHECK (length(sha256) = 64),
  reason  TEXT NOT NULL CHECK (reason IN ('unsigned','unknown_key','invalid_signature','invalid_bundle','downgrade','rolled_back','reset')),
  version INTEGER,
  at      INTEGER NOT NULL
);

-- One row: the consecutive-failure count, the fail-closed latch (REQ-SEC-015), the highest
-- version ever applied (the floor a reset keeps) and the last rejection `rules.status` reports.
CREATE TABLE rule_update_state (
  id                    INTEGER PRIMARY KEY CHECK (id = 1),
  consecutive_failures  INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
  fail_closed           INTEGER NOT NULL DEFAULT 0 CHECK (fail_closed IN (0,1)),
  ever_had_valid_key    INTEGER NOT NULL DEFAULT 0 CHECK (ever_had_valid_key IN (0,1)),
  highest_version       INTEGER NOT NULL DEFAULT 0 CHECK (highest_version >= 0),
  last_rejection_reason TEXT,
  last_rejection_at     INTEGER
);
INSERT INTO rule_update_state (id) VALUES (1);

-- Every change to the trust store, with its timestamp (REQ-SEC-014); a no-op add included.
CREATE TABLE trust_key_events (
  id          INTEGER PRIMARY KEY,
  key_id      TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  action      TEXT NOT NULL CHECK (action IN ('seeded','seed_revoked','added','readded','unchanged','revoked')),
  at          INTEGER NOT NULL
);
CREATE INDEX idx_trust_key_events_at ON trust_key_events(at);
