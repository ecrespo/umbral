# Delta — F1 closes; its QA walk and loose ends move to the Hardening phase

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-10-18 — decided by E. Crespo ("apruebo los 9 deltas. el 2 y 3 lo trabajamos en detalle en una nueva sesión? cierra el F1"); applied to specs/ and archived. Revised 2026-10-22 by the implementing agent, under the Tech Lead's instruction to finish F1 ("Ratifico los deltas y terminar todas las tareas pendientes para la fase F1"), when the closing measurement was taken: T-F1-38's evidence added for criterion 4, and T-HRD-03's flaky test replaced, the one first listed having been fixed under T-HRD-03 by delta `2026-10-agent-pty-settle`. The revision awaits the Tech Lead's review` |
| **Date** | 2026-10-18, revised 2026-10-22 |
| **Task** | none — a plan change, no code |
| **Raised by** | The Tech Lead, on 2026-10-18, once every F1 implementation task was merged |

## Evidence

On 2026-10-18 every F1 task was merged (`T-F1-01` … `T-F1-37`, PRs up to #24). One box was
still open, `T-F1-20`, and only for its Done line's second half: *"`docs/qa/f1-tui.md`
completed"*, the human walk the Tech Lead had deferred to the end of F1. The rule-signing key of
`docs/runbooks/rule-signing.md` §4.1 had not been created either, so the trust seed T-F1-30
ships is empty. Both are the Tech Lead's to do, and the Tech Lead chose to do them in a session
of their own. The nine deltas raised by T-F1-27 … T-F1-37 were ratified the same day, leaving
seven questions their "Open for the Tech Lead" sections had recorded, and three small defects
were known.

The closing measurement could not be taken that day. The US-003 run measured 12/20 with Ollama
busy with another client (`docs/reports/us003-2026-10-18-contended.md`), and criterion 4 — a
script waiting on a thread — had only raw JSON-RPC to do it with, because `thread.wait` was
interactive-only. On 2026-10-22 `T-F1-38` (`umb thread`, delta `2026-10-cli-thread-wait`, PR #28)
gave the CLI the wait, the US-003 run warmed the model up and refused to measure a busy one
(18/20, `docs/reports/us003-2026-10-22.md`), and delta `2026-10-agent-pty-settle` (PR #27) fixed
the cancel race behind the intermittent `REQ_AGT_007` test.

## Decision

**F1 closes on its five "Done" criteria**, measured on the closing tree
(`docs/checkpoints/2026-10-22-f1-closed.md`). What is left moves to the Hardening phase as
tasks, so a tasks file, not a closed phase's notes, holds each one:

1. **`T-REL-02` — the walk of `docs/qa/f1-tui.md`**, with the extensions view T-F1-37 added. It
   is a release 0.1 gate, like `T-REL-01`, and goes before it: the week is spent on a panel that
   has been walked once. `T-F1-20` is marked done with its walk carried there.
2. **`T-HRD-01` — the project's rule-signing key**, created by the Tech Lead under the runbook,
   its fingerprint seeded in the trust store. Not a release gate: an empty seed refuses every
   bundle, which is fail-closed (DD-016), and the built-in rules apply.
3. **`T-HRD-02` — the seven questions the ratified deltas left open**, each answered by a delta
   or accepted in writing before release 0.1:
   - from `2026-10-skill-catalog`: the normaliser's blind spots, `umb rules` commands not being
     destructive, `skill_load` not checking the stored copy's digest;
   - from `2026-10-umb-mcp-panel`: the catalog not tainting the turn, pattern-only guards on the
     agent's own `umb`, `umb mcp remove` not being destructive, which `env_refs` a `cli` client
     may set.
4. **`T-HRD-03` — three known defects:** `fetch_url` follows four redirects where Tech §5.3
   allows five; `TestAResponseOverTheLimitIsResultTooLarge_REQ_API_005` timed out reading the
   reply on the macOS runner once (PR #26, run green on retry); API §5.37 is used twice
   (`api.schema` and `skill.*`). The `REQ_AGT_007` flake first listed here was fixed by delta
   `2026-10-agent-pty-settle` before the phase closed.

What is **not** relaxed: `T-F1-20`'s walk keeps its text, and release 0.1 does not ship without
it.

## Specification changes

- **Plan 1.14:** §F1's table brought up to date and its five criteria marked met; §Hardening
  lists the four tasks; `T-F1-20`'s row points to `T-REL-02`.
- **Plan 1.14 (revision):** a `T-F1-38` row in §F1's table, and the criteria dated 2026-10-22.
- **F1 tasks:** `T-F1-20` marked done with a note; the execution log records the closure.
- **Hardening tasks:** `T-REL-02`, `T-HRD-01`, `T-HRD-02` and `T-HRD-03`.

## Phase

Closes F1 and moves four items to the Hardening phase. No REQ or test changes.
