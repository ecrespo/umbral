# Delta — display metadata: what a report writes, and how it stays apart

| Field | Value |
|---|---|
| **Status** | `PROPOSED 2026-10-17` |
| **Date** | 2026-10-17 |
| **Task** | T-F1-26 (REQ-INT-004) |
| **Raised by** | API §5.32 lists the members, the normalization and the limits. It does not say: how `state_labels` and `tokens` are shaped; what an absent member does; where a metadata `seq` is kept (`pane_metadata` has no column for one); whether the limits truncate or refuse; what a client is told; or how `metadata` appears on a `Pane` |

## Evidence

- **API §5.32** gives `{pane_id, source, title?, display_name?, state_labels?, tokens?: object,
  ttl_ms?: 1-86400000, seq?}`, an 80-character cap, "16 token keys per report and 32 live keys
  per pane", and "a `null` value clears the key". `state_labels` has no type.
- **API §4** shows `"metadata":{"title":"go test","tokens":{"summary":"unit"}}` on a `Pane`.
  Until this task the daemon always sent `{}`.
- **Data Model §2.4c** keys `pane_metadata` by `(pane_id, source, key)` with `value` of at most 80
  characters and an optional `expires_at`. It has no `seq` column, and migrations are
  forward-only (Art. 6).
- **REQ-INT-006**: an own-agent pane "accepts display metadata only".
- **API §6** has no metadata notification.

## Decisions

1. **Members become keys.**
   - `title` → `title`; `display_name` → `display_name`.
   - `state_labels: {state: label}` → `label:<state>`, where `state` is one of the five attention
     states.
   - `tokens: {name: value}` → `token:<name>`. A name is 1–26 characters of `[A-Za-z0-9._-]`, so
     the key fits the column's 32.
   - The tree reads the rows back into API §4's `metadata`. When two sources set one member, the
     later write wins. `updated_at` is strictly increasing per pane, as for the state reports.
2. **Absent keeps, `null` clears, and each source owns its keys.**
   - A member not in the report stays as it was.
   - A `null` member clears it, and so does a value that normalizes to nothing.
   - `state_labels: null` clears every label the source set, and `tokens: null` every token. Either
     alone is a valid report.
   - One source never clears another's keys: when `claude-code` clears its title, another
     source's title is shown.
   - A report with none of the four members is a `VALIDATION_ERROR`.
3. **Normalization (REQ-NTF-001's, at 80).**
   - Control and Unicode format characters are removed, so an escape sequence loses its `ESC` and
     shows as text.
   - Whitespace runs collapse to one space, and the value is trimmed. A control character that is
     also whitespace (tab, newline, carriage return) counts as whitespace, so `a\tb` reads `a b`.
   - The result is cut at 80 characters, not refused.
4. **The limits refuse the whole report, and nothing is written.**
   - More than 16 `tokens` in one report is refused.
   - More than 32 live rows on the pane after the report is refused. The count covers every
     source, and a key two sources both set counts twice.
   - Both answer `VALIDATION_ERROR`, naming the limit.
5. **`ttl_ms` covers every key the report sets.**
   - An expired row is never read, whether or not anything has deleted it yet.
   - Each write deletes the pane's expired rows.
   - **Expiry is not announced.** A client sees it on its next read or on the pane's next
     `pane.updated`. A timer per key would cost a goroutine per TTL for something only a display
     shows.
   - **Without `ttl_ms`, a key lives 24 hours from its last write**, the cap Data Model §4 and the
     retention job already set and the top of `ttl_ms`'s range. Every displayed row gets an
     `expires_at`, so what the tree shows and what retention purges agree, and a title not
     refreshed for a day disappears rather than being shown one moment and purged the next.
   - `ttl_ms: 0` is a `VALIDATION_ERROR`, because §5.32's range starts at 1. Omit the member for
     the 24-hour default.
   - Two clocks: the tree reads expiry against SQLite's clock, while writes and the 32-key count
     use the module's injected `Now`. They are the same wall clock in the daemon; tests that
     freeze `Now` do not rely on expiry, and the expiry test runs on the real clock.
6. **`seq` follows §5.31's rule, counted apart from the state reports.**
   - It is kept per pane and source in a reserved row, `#seq`. That row moves with the pane and is
     cleared at start like the rest, and it needs no migration.
   - The `#seq` row is never displayed and counts toward no limit.
   - `-1` means unnumbered.
   - The `#seq` row has no `expires_at`, and the retention job's age purge skips it
     (`internal/store/retention.go`): forgetting it after a day would let a replayed old report be
     accepted again.
7. **Who and where.**
   - `source` follows §5.31, including the reserved `umbral:` prefix.
   - An own-agent pane accepts metadata (REQ-INT-006).
   - An alias reaches a moved pane, and the metadata moves with it: T-F1-25's rename already
     carries `pane_metadata`.
   - A closed or unknown pane is `NOT_FOUND`.
   - Every client kind may call it, and the capability is `integrations`.
8. **Separation (REQ-INT-004, DD-013).**
   - This path never refreshes the pane states. `ResolveAttention` and the rollup do not read
     metadata.
   - A written report is announced as **`pane.updated`** with the whole `Pane`, its `metadata`
     included. It is never announced as `pane.state_changed` or `workspace.updated`.
   - "Waits" holds as written: thread waits observe a thread's attention and `block.wait_output`
     observes output, and metadata touches neither.
   - "Notifications" holds as written: no REQ-NTF notification is raised.
9. **No `umb pane report_metadata` yet.** The task's **Files** do not name `cmd/umb`.

## What changes in the specifications

- **API §4 `Pane`:** what `metadata` holds (decisions 1, 2 and 5). *Written on the task branch.*
- **API §5.32:** decisions 1–8. *Written on the task branch.*
- **API §6:** `pane.updated` is also sent after a written metadata report. *Written on the task
  branch.*
- **Data Model §2.4c:** the key names, the `#seq` row, strictly increasing `updated_at`, how expiry
  is read, and how the 32 is counted (decisions 1, 4, 5 and 6). *Folded at ratification.*
- **Data Model §4:** the retention job's 24 h purge skips the `#seq` row (decision 6). *Folded at
  ratification.*
- **Tech §5.2 `integrations` row:** metadata is written by the module and read by the tree, and
  never on the state path (decision 8). *Folded at ratification.*
- **PRD:** no change.

## Tests

| Test | Proves |
|---|---|
| `TestMetadataNeverChangesWaits_REQ_INT_004` (`integrations/integration`) | On an own-agent pane, with a label for `blocked`: state, source, rollup and the thread's attention unchanged; one `pane.updated` and nothing semantic, nor after a refresh; null/absent; the 16 and 32 limits refusing whole; stale `seq`; TTL expiry read on the real clock; `NOT_FOUND`. A source clearing only its own keys, and `state_labels: null`. A key without `ttl_ms` expiring 24 h after its write. It reddens without either limit, without expiry, with an own-agent refusal, with a semantic event, without the `seq` check, when `null` does not clear, without control stripping, when a null object does not clear or clears another source's keys, and when a key without `ttl_ms` gets no expiry |
| `TestMetadataFollowsAMove_REQ_INT_004` | decision 7's alias and move |
| `TestNormalizeDisplay_REQ_INT_004`, `TestMetadataReportBuild_REQ_INT_004` (`integrations/domain`) | decisions 1–5 |
| `TestRetentionPurgesAuditAndExpiredMetadata` (`internal/store`) | the age purge keeps a day-old `#seq` row (decision 6) |
| `TestPaneMetadataReachesTheWire_REQ_INT_004` (`internal/api`) | `null` versus absent on the wire, including `tokens: null` and `state_labels: null`, `ttl_ms: 0`, `umb` allowed, and the §4 shape |

## For the Tech Lead to decide

- **Decision 5: a key without `ttl_ms` lives 24 hours.** Recommended: it is the cap Data Model §4
  already sets, so display and retention agree. The alternative, no expiry until cleared,
  contradicts §4 and would show keys the retention job is about to delete.

- **Decision 5: expiry is not announced.** Recommended for now: no client displays metadata yet.
  The TUI task that first shows it can add a timer if it needs one.
- **Decision 9: `umb pane report_metadata` is left for later.** Recommended. It would be one more
  tree command, like `report_state`, and can ride with T-F1-37 or a small task of its own when a
  hook needs it.
