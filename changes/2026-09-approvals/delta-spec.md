# Delta — what an approval remembers, and what ends one

| Field | Value |
|---|---|
| **Status** | `PROPOSED 2026-09-27 — pending the Tech Lead's ratification. T-F1-14 implements it and is merged with it open, at the user's instruction to carry on through T-F1-20.` |
| **Date** | 2026-09-27 |
| **Task** | T-F1-14 |
| **Raised by** | T-F1-14 |

## Evidence

API §5.25: "`scope = always` persists an `allow` or `deny` rule (table `policy_rules`).
Destructive patterns ignore `always` (REQ-SEC-005)." It does not say what `thread` persists, what
pattern a remembered rule carries, nor what happens to an approval whose turn stops. §5.24 names
no parameters. `approvals` has no column for the call's target.

## Decisions

1. **What a rule remembers.** `thread` persists a rule for that thread, `always` a global one;
   both carry the tool and, as the pattern, the call's target — the command line, the path, the
   URL — which `approvals.summary` holds, so the user decided on exactly what they saw.
2. **Decided `once` whatever the scope asked:** a destructive command (REQ-SEC-005 — it asks
   every time); a target holding `*` or `?`, which a rule's glob would read as a wider pattern
   than the call; and a compound command line (`go build && go test`), which the rules read one
   command at a time (delta `2026-09-policy-precedence`), so a rule on the whole line would never
   match again. The approval's `decision_scope` records `once`.
3. **Persist, then resume** (REQ-AGT-011): the decision, and its rule, are written with the thread
   back to `running` in one transaction before the paused turn continues. The approval and the
   thread's `awaiting_approval` (attention `blocked`) are written before `approval.requested`.
   A remembered rule, allow or deny, applies to the rest of the same turn. An answer that
   arrives together with a cancel is a cancel: the tool does not run.
4. **A denial** is a `denied_by_user` tool result the model reads; the turn continues
   (REQ-AGT-005).
5. **A turn cancelled while it waits** leaves the approval `expired`; answering it later is
   `CONFLICT`. A write that fails while pausing is `storage_error` (delta
   `2026-09-agent-runtime`).
6. **`approval.list`** takes `{thread_id?, all?}`, oldest first. Both methods are interactive
   clients' (§2's cli row names neither); the `approval` prefix is advertised under `threads`.
7. This supersedes delta `2026-09-agent-runtime` decision 7 (an `ask` with no approval flow is
   `denied_by_policy`): there is always one now.

## Impact

- API 1.20: §5.24's parameters, §5.25's rules.
- Tech 1.28: §5.3d's approval bullet.
- No PRD or Data Model change.
