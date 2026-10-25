# Delta — destructive patterns for `umb rules` and `umb mcp remove`

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-10-24 by the Tech Lead ("ratifico los deltas")`; folded into Tech 1.70 §5.3, and archived |
| **Date** | 2026-10-24 |
| **Task** | T-HRD-07 |
| **REQ** | REQ-SEC-005, REQ-CLI-008 (no change to their text) |
| **Raised by** | Q2 and Q6 of delta `2026-10-hrd02-open-questions`, ratified by the Tech Lead on 2026-10-24 |
| **Depends on** | delta `2026-10-shell-parser` (T-HRD-06), whose branch this one is built on |

## Evidence

`umb rules key add|rotate|remove`, `umb rules rollback`, `umb rules reset` and `umb mcp remove`
matched no destructive pattern. `policy_test.go` listed `umb mcp remove x` as harmless. All six
methods are `userOnly`, so the daemon refuses them to an agent's terminal
(`internal/api/rules.go`, `internal/api/mcp.go`). The exception is the Tech §5.3 residual: a
process the peer check cannot see.

## Decisions

1. **Two built-in patterns.**
   - `umb_rules_change`: `^umb( \S+)* rules( \S+)* (key (add|rotate|remove)|rollback|reset)( |$)`.
   - `umb_mcp_remove`: `^umb( \S+)* mcp( \S+)* remove( |$)`.

   Both match the normalised line of T-HRD-06, so a wrapper, a path to `umb`, global flags or
   `sh -c` do not hide them.
2. **The daemon's refusal stays the control.** The patterns are defence in depth for the
   residual only. An approved command from inside an agent's terminal still ends
   `PERMISSION_DENIED` when the peer check sees the caller (T-F2-05, T-HRD-05). Tech §5.3 says so.
3. **What stays harmless:** `umb rules status`, `umb rules key list` and `umb mcp list` read
   only.

## Spec changes

- Tech 1.70 §5.3: the built-in list gains both entries, and the sentence that the daemon's
  refusal is the control.

## Verification

- `TestDestructiveAlwaysAsk_REQ_SEC_005` has these new destructive lines: the three key changes,
  `rollback`, `reset`, `--socket` and `sh -c` forms, and three `umb mcp remove` forms.
- Its new harmless lines are `umb rules status`, `umb rules key list` and `echo umb rules reset`.
- `umb mcp remove x` moved from its harmless list to its destructive list.
- Red first: 20 failures before the patterns.
- Mutations: dropping `rollback` from the alternation reddens 2 cases, and breaking `remove`
  reddens 6.
