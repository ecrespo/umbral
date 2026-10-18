# Delta — the integration surface: who decides a pane's state

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-10-17` by E. Crespo, after a spec-guardian review whose findings are folded into decisions 4, 5, 7, 10 and 15; decisions 2, 4, 12, 14 and 15 as recommended. Folded into API 1.33, Tech Design 1.42, Data Model 1.19 and the F1 tasks file |
| **Date** | 2026-10-17 |
| **Task** | T-F1-25 (REQ-INT-001, REQ-INT-002, REQ-INT-003, REQ-INT-005, REQ-INT-006) |
| **Raised by** | API §4 says the `umbral:shell` derivation is "specified nowhere" and defers it to the task that owns REQ-INT-002. §5.31 does not say what several sources on one pane do, what a report without `seq` does, how long a report lives, or what a moved pane's hook reaches. Tech §5.2b does not say what happens to an inherited `UMBRAL_*` value |

## Evidence

- **API §4** (`Pane`): "The `umbral:shell` derivation is named here but specified nowhere: which
  block state maps to which attention state is deferred to the task that owns REQ-INT-002."
- **DD-012** orders the authorities: own agent, then external report, then the shell. No text
  says where that order is computed, or what a pane with no live session shows.
- **API §5.31** gives `source`, `seq` and `PERMISSION_DENIED`. It gives no bounds for `agent` or
  `message`, says nothing about `umbral:`-prefixed sources, and nothing about two sources at once.
- **Tech §5.2b** says Umbral's values win "over any caller-supplied value". A daemon autostarted
  by `umb` from another daemon's pane inherits that pane's `UMBRAL_PANE_ID`. A thread's PTY,
  which belongs to no pane, would keep it.
- **`pane.move`** renamed `panes.id` in place. `pane_state_reports.pane_id` and
  `pane_metadata.pane_id` are foreign keys with no `ON UPDATE`, so moving a pane that had a report
  failed with `FOREIGN KEY constraint failed`. `TestPaneStateReportsFollowAMove` reproduced it
  before the fix.
- Only `UMBRAL_SESSION=1` was injected before this task.

## Decisions

1. **The tree resolves a pane's state, in DD-012's order, when it reads the pane.**
   - `workspaces/domain.ResolveAttention` is the rule. The tree store reads its inputs with each
     pane: the owner thread of the live session, that thread's `attention_state`, the latest
     report, whether the session is alive, its integration verdict and its latest block.
   - Nothing is cached. `pane.get`, `pane.list`, `session.snapshot` and the workspace rollup
     (REQ-WS-006) all read the same answer.
   - The order:
     1. `umbral:agent`;
     2. an external report;
     3. `umbral:shell`;
     4. `unknown` with a `null` source.
2. **`umbral:shell` maps the latest block of the live session.**
   - `running` or `interactive` → `working`.
   - `finished` or `abandoned` → `idle`.
   - No block yet, with `integration: osc133`, → `idle`: a shell at its first prompt.
   - Anything else → `unknown`, with no source. That covers `pending`, `none`, and no live
     session. A pane running `top` from a layout has no integration, so it is `unknown` unless
     something reports for it.
   - **`done` is never derived.** A command that ended is not news nobody has seen. Only
     REQ-AGT-016 and external reports produce `done`.
3. **`Pane.thread_id` is the thread whose PTY the live session is**
   (`sessions.owner_thread_id` of an `alive` session). It is what REQ-INT-006 tests.
   - **No API path creates such a pane today.** Thread PTYs are not attached to panes. The rule
     holds for when one is. Until then the tests prove it by writing those rows directly.
4. **What a source may be called, and what a report may carry.**
   - `source`: API §5.31's 1–80 characters of `[A-Za-z0-9:._-]`.
   - A source starting `umbral:` is a `VALIDATION_ERROR`. It would claim one of Umbral's own
     authorities.
   - `agent`: at most 80 characters.
   - `message`: at most 240 characters, the cap REQ-NTF-001 puts on a notification body.
   - Both are stored as sent, control characters included, and shown by nothing yet. Whatever
     displays them first (T-F1-26, the TUI) normalizes them the way REQ-NTF-001 does.
   - `state` excludes `unknown`. A source with nothing to say releases the pane.
5. **`seq` is per pane and source** (REQ-INT-003).
   - A report is discarded, with `{}`, when it carries a `seq`, the source has a stored report for
     the pane, and the `seq` is not past the stored one.
   - A report without `seq` always applies, and it keeps the stored `seq`. A numbered report after
     it is still compared with the last number.
   - The first report of a source applies whatever its `seq`. A source whose reports so far were
     unnumbered has accepted no number either: its row stores `seq = -1`, which no source can
     send, so its first numbered report applies at `0` too. No migration is needed; the column
     has no `CHECK`.
   - A release forgets the number. A late report arriving after a release takes the pane again.
     A source that numbers its reports and releases must not keep reporting.
   - The check and the write share one `BEGIN IMMEDIATE` transaction.
6. **Several sources on one pane: the most recent report decides.**
   - Every write gets an `updated_at` past every other report of that pane, so two hooks firing in
     the same millisecond are still ordered by arrival.
   - A release hands the pane to the most recent of the remaining sources. When none remains,
     Umbral's own detection decides again (REQ-INT-005).
   - A repeated report of the same state changes nothing a client sees.
7. **Addressing.**
   - `pane_id` may be an alias of a moved pane (REQ-WS-007). The report is stored under the
     pane's current id. A hook keeps the `UMBRAL_PANE_ID` it was started with, and that id goes
     stale on a move.
   - A closed or unknown pane is `NOT_FOUND`.
   - A release by a source that never reported for the pane is `{}` and changes nothing.
   - A release is accepted on an own-agent pane. Removing a report never takes authority.
   - A refused report (REQ-INT-006) stores nothing and announces nothing.
   - The own-agent check reads the pane before the write's transaction. A thread attached to the
     pane in between would let one report be stored; the agent still decides (decision 1), so
     authority holds. No API path attaches a thread to a pane today; the task that adds one moves
     the check into the write's transaction.
   - Once a report or release has committed, the answer is `{}` even when announcing it fails;
     the failure is logged. A hook told `INTERNAL_ERROR` for a stored report would retry with the
     same `seq` and be discarded.
8. **Reports follow a move.**
   - `pane.move` renames the pane's reports and metadata with it.
   - The foreign-key check is deferred to the commit (`PRAGMA defer_foreign_keys`), which SQLite
     resets when the transaction ends.
   - This also fixes the move failure in the evidence.
9. **Lifetime.**
   - A report lives until it is released or the daemon restarts (Data Model §6 step 8, unchanged).
   - Closing a pane does not delete its report, but a closed pane is never read again.
10. **`pane.state_changed` follows every change of a pane's effective state or source, whoever
    made it** (§6).
    - The module remembers what it last announced per pane, and per workspace rollup. It compares
      after:
      - a report or release;
      - `block.started`, `block.updated` or `block.closed`;
      - `session.integration` or `session.exited`;
      - `thread.attention_changed`.
    - All of these events are published after their rows are written (DD-007).
    - `cmd/umbrald` subscribes the module to them, because `integrations` may not import
      `sessions` or `agents` (Tech §5.2).
    - A rollup that moved is announced as `workspace.updated`.
    - **Each refresh re-reads the whole tree.** An event the bus dropped under pressure is caught
      up by the next one. A change followed by no event at all stays unannounced until one comes.
      No periodic refresh was added, so an idle daemon does no work.
    - Announcements are serialised with the writes, so they leave in the order written.
    - Pane creation, moves and closes are not among the events. They carry the pane's state
      themselves (`pane.created`, `pane.moved`), so the next refresh may send one redundant
      `pane.state_changed` for a moved pane's new id. That is harmless.
11. **Environment (REQ-INT-001, Tech §5.2b).**
    - **Every session:**
      - `UMBRAL_ENV=1`;
      - `UMBRAL_SESSION_ID`, chosen before the launch so the child can be told;
      - `UMBRAL_SOCKET_PATH`, the daemon's socket;
      - `UMBRAL_BIN_PATH`.
    - **Every pane's session** also gets `UMBRAL_WORKSPACE_ID`, `UMBRAL_TAB_ID` and
      `UMBRAL_PANE_ID`. A thread's PTY, in no pane, gets none of the three.
    - `UMBRAL_SESSION=1` stays as well. Nothing reads it, but removing it could break someone's
      rc file.
    - **Inherited or declared `UMBRAL_*` values are dropped first, not merely overridden.**
      Otherwise a session without a pane location would keep another pane's.
    - **`UMBRAL_BIN_PATH`** is the `umb` beside `umbrald`, then the first on `PATH`. When neither
      exists it is left out, with a warning in the daemon log.
    - **A moved pane's processes keep the ids they were started with.** Decision 7 is why that
      still reaches the pane.
12. **The CLI: `umb pane report_state <state> [pane]` and `umb pane release_state [pane]`.**
    - Both take `--source`. `report_state` also takes `--agent`, `--message` and `--seq`.
    - **The command is the method's name, underscore included.** That is decision 1 of
      `2026-09-cli-workspace-surface`: the tree commands cannot express a command whose name
      differs from its method.
    - **The pane defaults to `$UMBRAL_PANE_ID`.** Outside a pane, with none named, the command
      fails before connecting.
    - `--seq` is sent only when given.
    - Success prints nothing. A hook's answer is its exit code (REQ-CLI-004).
    - **The socket does not default to `$UMBRAL_SOCKET_PATH`.** A hook passes
      `--socket "$UMBRAL_SOCKET_PATH"`. If the default followed the variable, every `umb`
      started from a pane, a test run included, would talk to that pane's daemon unasked.
13. **Who may call, and the capability.**
    - Every client kind may call both methods. §2's `cli` row already gives `umb` `pane.*` except
      `pane.move`.
    - The capability is `integrations`. Without the module both methods answer
      `NOT_IMPLEMENTED`, and `integrations` is not advertised.
14. **"Waits" in REQ-INT-002.**
    - API 1.32 has no wait on a pane's state. `thread.wait` observes threads and
      `block.wait_output` observes output. A report therefore has nothing to drive there yet.
    - `pane.state_changed` is the event a pane wait would follow.
    - The other half of REQ-AUT-005 is not in this task: discarding a source over the report rate
      limit.
15. **"Notifications" in REQ-INT-002 is `pane.state_changed`** (and `workspace.updated` for the
    rollup), the protocol notifications of API §6.
    - A user-facing notification (REQ-NTF-001, `notification.show`, T-F1-27) is not raised by a
      report in this task. Whether an external `blocked` or `done` should raise one is T-F1-27's
      to decide; the recommendation is below, and the ratification writes the answer into
      T-F1-27's **What**.

## What changes in the specifications

- **API §4 `Pane`:** `state_source`'s order and the `umbral:shell` mapping (decisions 1–3). The
  "specified nowhere" sentence goes. *Written on the task branch.*
- **API §5.31:** decisions 4–7, 9, 13 and the `{}` result. *Written on the task branch.*
- **API §6:** when `pane.state_changed` is sent, and the `workspace.updated` that goes with it
  (decision 10). *Written on the task branch.*
- **Tech §5.2b:** inherited values dropped, the thread PTY's absent location, `UMBRAL_BIN_PATH`'s
  lookup, the kept `UMBRAL_SESSION` (decision 11). *Folded at ratification.*
- **Tech §5.1 / §5.2:** the `integrations` module's layout (`domain`, `ports`,
  `adapters/reportstore`, the service) and its `.go-arch-lint.yml` rows. The cmd-side
  subscription is decision 10. *Folded at ratification.*
- **Tech §9.4:** `umb pane report_state` / `release_state` (decision 12). *Folded at
  ratification.*
- **Data Model §2.4c:** `pane_state_reports` rows follow a move, `updated_at` is strictly
  increasing per pane, and `seq = -1` means unnumbered (decisions 5, 6 and 8). *Folded at
  ratification.*
- **API §2:** the sentence naming methods advertised outside their prefix gains
  `pane.report_state` and `pane.release_state` (`integrations`). API 1.33 with a history row.
  *Folded at ratification.*
- **PRD:** no change. REQ-INT-001/002/003/005/006 are met as written, with decision 14's reading
  of "waits" and decision 15's of "notifications".

`task schema` passes on the branch. The two methods were already in §5.31's heading, and the
notification was already in §6.

## Tests

| Test | Proves |
|---|---|
| `TestOneAuthorityPerPane_DD_012` (`workspaces/domain`) | decisions 1–2, every case of the rule |
| `TestPaneStateFollowsItsAuthority_DD_012` (`workspaces/integration`) | the rule read back from SQLite through `GetPane`, `ListPanes` and the rollup, across every authority |
| `TestPaneStateReportsFollowAMove` | decision 8. It failed with the foreign-key error before the fix |
| `TestEveryPaneIsToldWhereItIs_REQ_INT_001` | the tree hands each session its pane's location: create, split and restore. It reddens when restore passes none |
| `TestEnvInjectedAndAuthoritative_REQ_INT_001` (`sessions/integration`) | all seven variables in a real PTY, each beating a declared value |
| `TestNoPaneLocationIsInherited_REQ_INT_001` | decision 11's dropping. It reddens with either filter removed |
| `TestExternalReportDrivesRollup_REQ_INT_002` (`integrations/integration`) | state, source, rollup, stored `agent`/`message`, `pane.state_changed` and `workspace.updated`, and a repeated report staying silent |
| `TestTheLatestReportWins_REQ_INT_002` | decision 6 on a frozen clock. It reddens without the strictly increasing `updated_at` |
| `TestReportReachesAMovedPaneByItsAlias` | decision 7 |
| `TestStaleSeqIgnored_REQ_INT_003`, `TestStaleSeq_REQ_INT_003` | decision 5, the unnumbered-then-`0` case included. They redden with `<` for `<=`, with no staleness, when a report without `seq` resets the number, and when an unnumbered row stores `0` |
| `TestReleaseRestoresOwnDetection_REQ_INT_005` | the release returns to `umbral:shell` and announces it; another source's release changes nothing; a second release is `{}` |
| `TestOwnAgentCannotBeDisplaced_REQ_INT_006` | `PERMISSION_DENIED`, nothing stored, nothing announced, the agent still decides |
| `TestReportsForMissingPanes`, `TestReportValidation_REQ_INT_002` | `NOT_FOUND`, and decision 4's bounds |
| `TestRefreshFollowsOwnDetection_DD_012` | decision 10 for a change no report made |
| `TestUmbBinPathPrefersTheSibling_REQ_INT_001` (`cmd/umbrald`) | decision 11's `UMBRAL_BIN_PATH` lookup: sibling, then `PATH`, then nothing; a non-executable sibling is skipped |
| `TestPaneReportsReachTheWire_REQ_INT_002`, `TestPaneReportsNeedTheirModule_REQ_API_003` (`internal/api`) | the wire: params, `seq` absent as absent, the §3 codes, `umb` allowed, the notification with a `null` source, the capability only when served |
| `TestPaneReportStateFromAHook_REQ_INT_002`, `TestPaneReportStateNeedsAPane_REQ_INT_002` (`cmd/umb`) | decision 12 |

## For the Tech Lead to decide

- **Decision 2: never derive `done` from the shell.** Recommended. A finished command is the
  shell's ordinary state. `done` means "finished and not yet seen", and nothing observes whether
  a person looked at a pane.
- **Decision 4: a `umbral:` source is `VALIDATION_ERROR`, not `PERMISSION_DENIED`.** Recommended.
  It is a malformed name on any pane. `PERMISSION_DENIED` stays REQ-INT-006's answer for a valid
  source on an agent's pane.
- **Decision 12: the command is spelled `report_state`, with the underscore.** Recommended, for
  the one-command-one-method rule. The alternative is `report-state` and an exception in the tree
  commands.
- **Decision 14: "waits" in REQ-INT-002 has no consumer until a pane wait exists.** Recommended:
  accept it as met by `pane.state_changed`, and give any future pane wait that event.
- **Decision 15: an external report raises no user notification in this task.** Recommended for
  T-F1-27: an external `blocked` raises one, through the same rate limit as any source
  (REQ-NTF-002), because `blocked` is what a user must act on; `done` and the rest do not.
