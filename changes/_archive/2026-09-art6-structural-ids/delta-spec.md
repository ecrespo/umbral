# Delta — structural identifiers as an exception written into Art. 6

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-09-20 — folded into specs/` |
| **Date** | 2026-09-20 |
| **Task** | T-F0-14 (blocked on this), T-F0-15, T-F0-18 |
| **Raised by** | Analyze finding C-05, found while reconciling the two spec lineages |
| **Approved by** | Ernesto Crespo |

## Evidence

Art. 6 requires public identifiers to be type-prefixed ULIDs. The orchestration surface then
specified the opposite for the structure it introduced, and did so in four places without
recording an exception anywhere:

| Artifact | What it says |
|---|---|
| PRD REQ-WS-002 | identifiers of the form `w<n>`, `w<n>:t<m>`, `w<n>:p<m>` |
| Tech Design DD-009 | "Public identifiers (`w1`, `w1:t1`, `w1:p1`) are allocated by the daemon" |
| API §4 | the Workspace, Tab and Pane objects carry `"w1"`, `"w1:t1"`, `"w1:p2"` |
| Data Model §2.4b | `workspaces.id` has `CHECK (id LIKE 'w%')`; `tabs.id` and `panes.id` have no check |

Meanwhile API §3 still declared "Type-prefixed ULIDs … (Constitution Art. 6)" and listed only the
eight `ses_`-style prefixes, and the Tech Design's own Constitution check still asserted "Art. 6:
prefixed ULIDs". Two documents therefore approved a design a third forbade. The constitution's
closing rule is explicit: *an exception without a written justification is a violation.*

## Decision

Write the exception into Art. 6 rather than change the identifiers.

The reason ULIDs exist in Art. 6 is stated in its own rationale: no ID collisions. That risk is
about identifiers that travel — between machines, into logs, across a merge. Structural
identifiers do none of that. They are allocated by one daemon, live inside one installation, and
are never exchanged with another. What they *do* have to be is typeable: the addressing surface
of this design is the command line, and `umb pane split w1:t1` is the feature.
`umb pane split pn_01J9Z3K8T2QH6W4V5X7Y8Z9A0B` is the same feature with the usability removed,
and a user who cannot say which pane they mean will not use panes from scripts at all.

Keeping ULIDs internally with `w1:t1` as a display alias was considered and rejected: it adds a
resolution step to every one of `pane.split|list|get|focus|rename|move|close`, and the alias would
still be the thing users, scripts and `UMBRAL_PANE_ID` actually carry, so the ULID would be an
identifier nothing addresses.

## MODIFIED

### specs/constitution.md → Art. 6 and the amendment table

- **Before:** "public identifiers as **type-prefixed ULIDs** (`ses_`, `blk_`, `thr_`, …)".
- **After:** the same rule, followed by the exception for the workspace tree, its scope and its
  three conditions (daemon-allocated, unique and stable within the session, never reused while the
  object exists).
- **Reason:** the exception exists in the design either way; the constitution is where it has to be
  written down. Constitution version 1.2, third amendment.

### specs/api/umbral-daemon-api-v1.md → §3 Identifiers

- **Before:** one bullet claiming every identifier is a type-prefixed ULID.
- **After:** two bullets — the ULID family, and the structural family with its grammar and the
  pointer to the Art. 6 exception.

### specs/technical/umbral-architecture.md → Constitution check, DD-009

- **Before:** "Art. 6: prefixed ULIDs and UTC ms timestamps (Data Model)."
- **After:** the same, plus the structural exception and where it is authorised.

### specs/data-model/umbral-schema.md → §2 conventions and §2.4b

- **Before:** "IDs = `TEXT` prefixed ULID"; `tabs.id` and `panes.id` unconstrained.
- **After:** the convention names both families, and the three tables enforce their grammar with a
  `CHECK`, so a malformed identifier cannot be written at all.

### specs/prd/umbral-mvp.md → Constitution check

- **After:** Art. 6's entry names the exception and points at the amendment.

## NOT MODIFIED

REQ-WS-002, DD-009 and the API objects are unchanged: this delta authorises what they already say.
`pane_aliases` is unchanged — an alias is a previous structural identifier, and it inherits the
same grammar.

## Verification

- `python3 tools/sdd_check.py` exits 0, and its migration-0003 check inserts `w1`, `w1:t1` and
  `w1:p1`, which now have to satisfy the new `CHECK` constraints to pass.
- No document asserts unqualified "every identifier is a ULID" any more:
  `grep -rn "prefixed ULID" specs/` returns only the qualified statements.
