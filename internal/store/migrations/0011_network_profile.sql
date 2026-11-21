-- 0011 — whether a hosted thread's agent was given a network the sandbox enforces (T-HRD-20c;
-- PRD F2 REQ-ACP-007, REQ-SBX-008, REQ-SBX-009; Data Model §2.5, delta `2026-11-model-relay`).
--
-- One column, not a rebuilt `threads` (Q3): `egress_visibility` keeps its two stored values,
-- and a hosted thread reads `confined` on the wire when this says so. Written at each launch
-- of the thread's agent.
ALTER TABLE threads ADD COLUMN network_profile TEXT NOT NULL DEFAULT 'open'
  CHECK (network_profile IN ('open','confined'));
