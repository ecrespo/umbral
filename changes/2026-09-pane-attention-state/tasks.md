# Tasks — delta `2026-09-pane-attention-state`

### [ ] T-PA-01 · Write the `unknown` rollup and F0's missing state source into the specs
- **What:** API Spec §4 `Workspace.rollup_state` gains `unknown`; §4 `Pane` records that
  `attention_state` is `unknown` with a `null` `state_source` until a source reports one;
  PRD REQ-WS-006 notes that clients must render `unknown`.
- **REQ:** REQ-WS-006, REQ-INT-002
- **Files:** `specs/api/umbral-daemon-api-v1.md`, `specs/prd/umbral-mvp.md`
- **Done:** `python3 tools/sdd_check.py` green; `TestRollupPrefersBlocked_REQ_WS_006`
  covers the all-`unknown` and empty-workspace clauses.
