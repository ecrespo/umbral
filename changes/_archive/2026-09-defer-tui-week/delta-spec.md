# Delta — the TUI's week moves from F0's exit to the release gate

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-09-26 — decided by E. Crespo ("podemos dejar el criterio 3 para el final de la implementación"); applied to specs/ and archived` |
| **Date** | 2026-09-26 |
| **Task** | none — a plan change, no code |
| **Raised by** | The Tech Lead, on 2026-09-26, once every other F0 criterion was met |

## Evidence

F0's third exit criterion reads: *"The TUI is used for one week as the main terminal without
blocking regressions."* On 2026-09-26 it was the only one left: criteria 1, 2, 4, 5 and 6 are
met, REQ-BLK-003 holds (T-F0-21, T-F0-22), and every F0 task through T-F0-24 is done. It is a
human's week, not code, and nothing in F1 depends on it having happened first.

## Decision

**The week of daily use becomes a gate of release 0.1**, in the Hardening phase, instead of an
exit criterion of F0. The TUI that users will receive is the one at the end of the
implementation, with F1's agent panel in it; a week spent on F0's TUI would validate a client
that is about to change, and the week that matters is the one on the TUI that ships.

What is **not** relaxed: the criterion's text stays as written — one week, as the main
terminal, with no blocking regression, recorded where `docs/qa/f0-tui.md` was — and release 0.1
does not ship without it.

## Specification changes

- **Plan §F0, exit criteria:** criterion 3 is marked *moved to the release gate* with a pointer
  to this delta, and F0 closes on the remaining five and REQ-BLK-003.
- **Plan §Hardening and release 0.1:** gains the criterion as a release gate.
- **Plan changelog:** 1.7.
- `docs/f0-closure-plan.md` item 4 points to the release gate.
- **Hardening tasks:** `T-REL-01` carries the criterion with its exact text as the Done line,
  so a tasks file — not only a plan bullet — holds release 0.1 to it.

## Phase

Moves an item from F0 to the Hardening phase. No REQ or test changes; one task, `T-REL-01`.
