# Delta — block lifecycle decisions (findings 1, 2, 6, 7 and 10 of the T-F0-09 review)

| Field | Value |
|---|---|
| **Status** | `DRAFT — awaiting approval` |
| **Date** | 2026-09-11 |
| **Task** | T-F0-09 |
| **Raised by** | `spec-guardian` review, verdict NEEDS A DELTA |

Five decisions taken while implementing T-F0-09 narrow or extend sentences that are already
written in the approved specs. Art. 9 says they belong here rather than in a code comment.
The code implements what this proposes; nothing is committed as spec until this is approved.

## MODIFIED

### specs/api/umbral-daemon-api-v1.md → §4 Block, `state` and §7 state machine (finding 1)

- **Before:** `abandoned` is glossed as "the session died with the block open", and the §7
  block diagram has exactly one edge into it, from `session.exited`.
- **After:** `abandoned` means **the block ended without reporting how**. The session dying
  under it is one way; the other is a second `OSC 133;C` arriving with no `OSC 133;D` in
  between, which happens when a shell starts a command without telling the daemon how the
  last one finished. The §7 diagram gains:

  ```
  running --> abandoned: OSC 133 C with no preceding D
  interactive --> abandoned: OSC 133 C with no preceding D
  ```

  THE SYSTEM SHALL leave `exit_code` null on such a block.
- **Reason:** the block has to end somehow, and the three alternatives are worse. Leaving it
  `running` forever means a session's history accumulates blocks that never close. Closing
  it `finished` claims the shell reported an ending it never reported. Inventing an exit code
  puts a wrong number into the history and into the agent's context, which is the one place
  a wrong number does real damage. "We never learned how it ended" is what `abandoned`
  already means for the session-death case; this widens the gloss to match.

### specs/data-model/umbral-schema.md → §2.3 `block_chunks` (finding 2)

- **Before:** "raw output (with escapes) in zstd-compressed chunks, for faithful
  re-rendering and export".
- **After:** the same, except that THE SYSTEM SHALL strip the shell-integration sequences it
  recognises — `OSC 133;A/B/C/D`, `OSC 633;E` and `OSC 7` — and the alternate-screen mode
  sequences that bracket an `interactive` stretch. Every other escape sequence is stored
  byte for byte.
- **Reason:** those sequences are the shell talking to the daemon, not output. They are
  invisible on screen (VT-20 in the conformance suite proves it), so keeping them makes no
  replay more faithful, and a consumer exporting a block would have to filter them anyway.
  The alternate-screen pair is different in kind: REQ-BLK-004 already excludes what is
  painted between them, so storing an unbalanced `CSI ?1049h` or `CSI ?1049l` would make a
  replay switch the reader's terminal into or out of a screen it never entered.

### specs/data-model/umbral-schema.md → §2.2 `blocks.output_truncated` (finding 10)

- **Before:** the flag is defined only against the 16 MiB raw cap of §2.3.
- **After:** THE SYSTEM SHALL set `output_truncated` when **either** cap discarded output:
  the 16 MiB raw chunk history or the 1 MiB `output_plain` transcript.
- **Reason:** the flag's job is to tell a client that what it can read is not all the command
  said. REQ-BLK-007 names `output_plain` as the agent's context, so a transcript cut at 1 MiB
  with the flag reading `false` misleads exactly the consumer the flag exists for.

### specs/prd/umbral-mvp.md → REQ-BLK-003 (finding 7)

- **Before:** "IF a session emits no shell-integration sequences within the first 5 s, THEN
  THE SYSTEM SHALL mark it `integration: none` and keep delivering output without creating
  blocks."
- **After:** the same, plus: WHEN a shell-integration sequence arrives after that window,
  THE SYSTEM SHALL set `integration: osc133` and record blocks from it. THE SYSTEM SHALL NOT
  move a session from `osc133` back to `none`.
- **Reason:** the five-second window is a heuristic about silence, not a verdict about the
  shell. Without the promotion, a shell slower than the window is advertised as having no
  integration while its blocks are being recorded, so `session.get` and `block.list` disagree
  about the same session. The one-way rule is what keeps the timer from undoing a session
  that has already announced itself.

### specs/technical/umbral-architecture.md → §3.2 module table, `sessions` row (finding 6)

- **Before:** key libraries listed as `creack/pty`, go-libghostty, `shell/` bootstrap.
- **After:** add `klauspost/compress/zstd`, the compressor for `block_chunks`.
- **Reason:** it is the repository's first compression dependency and the Data Model requires
  zstd, which the Go standard library does not provide. A dependency that no spec records is
  a dependency nobody agreed to.

## Impact

| Artifact | Change |
|---|---|
| `specs/api/umbral-daemon-api-v1.md` | §4 gloss, §7 diagram (two new edges) — **the diagram must pass `node tools/mermaid_check.mjs`** |
| `specs/data-model/umbral-schema.md` | §2.2 and §2.3 prose; no DDL change |
| `specs/prd/umbral-mvp.md` | REQ-BLK-003 text; no new REQ id |
| `specs/technical/umbral-architecture.md` | §3.2 table row |

No requirement is added or removed, so `tools/sdd_check.py` counts are unchanged.
