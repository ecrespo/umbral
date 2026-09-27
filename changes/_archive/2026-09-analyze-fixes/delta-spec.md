# Delta — Fixes for findings A-01…A-07

## ADDED

### specs/prd/umbral-mvp.md → §6.6 Security
- **REQ-SEC-008** · MUST · unwanted — IF the operating system keyring is unavailable when the daemon starts, THEN THE SYSTEM SHALL start without aborting, disable the providers whose credential is `keyring:<path>`, mark their models `health = down` with reason `keyring_unavailable`, and show that reason in `umb status`.

### specs/prd/umbral-mvp.md → §6.3 Agent
- **REQ-AGT-015** · MUST · unwanted — IF `thread.send` arrives with a `client_msg_id` already processed in the same thread, THEN THE SYSTEM SHALL reply with the original `turn_id` and `message_id` without creating a new turn.

### specs/technical/umbral-architecture.md → §8.1 Appendix: VT conformance cases (A-02)

| ID | Case | Priority |
|---|---|---|
| VT-01 | Cursor movement CUP/CUU/CUD/CUF/CUB and screen bounds | MUST |
| VT-02 | Erase ED/EL (0, 1, 2) | MUST |
| VT-03 | DECSTBM scroll region with IND/RI/NEL | MUST |
| VT-04 | Basic SGR: 16 colors, bold, italic, underline, inverse, reset | MUST |
| VT-05 | SGR 256 colors (38;5 / 48;5) | MUST |
| VT-06 | SGR truecolor (38;2 / 48;2) | MUST |
| VT-07 | Alternate screen 1049: enter, exit and restore content and cursor | MUST |
| VT-08 | Bracketed paste 2004 | MUST |
| VT-09 | SGR 1006 mouse reporting | MUST |
| VT-10 | Wide characters (CJK) with width 2 | MUST |
| VT-11 | Grapheme clusters (ZWJ emoji) as one logical cell | MUST |
| VT-12 | Combining characters | MUST |
| VT-13 | DECAWM autowrap at the right margin | MUST |
| VT-14 | Reflow of wrapped lines on resize | MUST |
| VT-15 | HT/HTS/TBC tabs with default stops | MUST |
| VT-16 | DECSC/DECRC cursor save/restore | MUST |
| VT-17 | IL/DL/ICH/DCH insert/delete | MUST |
| VT-18 | OSC 0/2 (title) | MUST |
| VT-19 | OSC 7 (cwd) | MUST |
| VT-20 | OSC 133 A/B/C/D and OSC 633;E intercepted without visible effects | MUST |
| VT-21 | Kitty keyboard protocol (push/pop of flags) | SHOULD |
| VT-22 | OSC 8 hyperlinks | SHOULD |

Each case is a `testdata/vt/VT-NN-<slug>.in` / `.golden` pair (plain text + attributes).

## MODIFIED

### specs/data-model/umbral-schema.md → §5 Migrations (A-01)
- **Before:** 0001 included `sessions`, `blocks`, `block_chunks` and `blocks_fts`; `threads` was created in 0002.
- **After:** 0001 also includes `threads` (full §2.5 definition) and its index. 0002 no longer creates `threads`.
- **Reason:** A-01; with `foreign_keys=ON`, an FK to a non-existent table makes every INSERT fail.

### specs/data-model/umbral-schema.md → §2.4 `blocks_fts` (A-07)
- **After:** the comment is replaced by explicit triggers:
```sql
CREATE TRIGGER blocks_fts_ai AFTER INSERT ON blocks BEGIN
  INSERT INTO blocks_fts(rowid, command, output_plain) VALUES (new.rowid, new.command, new.output_plain);
END;
CREATE TRIGGER blocks_fts_ad AFTER DELETE ON blocks BEGIN
  INSERT INTO blocks_fts(blocks_fts, rowid, command, output_plain) VALUES ('delete', old.rowid, old.command, old.output_plain);
END;
CREATE TRIGGER blocks_fts_au AFTER UPDATE OF command, output_plain ON blocks BEGIN
  INSERT INTO blocks_fts(blocks_fts, rowid, command, output_plain) VALUES ('delete', old.rowid, old.command, old.output_plain);
  INSERT INTO blocks_fts(rowid, command, output_plain) VALUES (new.rowid, new.command, new.output_plain);
END;
```

### specs/data-model/umbral-schema.md → §2.6 `messages` (A-04)
- **After:** column `client_msg_id TEXT` (ULID, nullable) and index `CREATE UNIQUE INDEX idx_messages_client_msg ON messages(thread_id, client_msg_id) WHERE client_msg_id IS NOT NULL;`.

### specs/api/umbral-daemon-api-v1.md → §4 `Session` (A-05), §5.14 `thread.send` (A-04) and §5.21 `mcp.server.add` (A-06)
- **`Session`:** adds `owner_thread_id: string | null`.
- **`thread.send`:** adds the optional parameter `client_msg_id` (ULID). `umbral-tui` and `umb` always send it. A duplicate returns the original result (REQ-AGT-015).
- **`mcp.server.add`:** `env_keyring_refs` is renamed to `env_refs` (column `env_refs_json`).

### specs/technical/umbral-architecture.md → §8 Testing Strategy (A-02)
- **After:** the "VT conformance" row references appendix §8.1. REQ-TERM-002 is verified against the MUST cases VT-01…VT-20.

### specs/tasks/umbral-f0-tasks.md → T-F0-02 and T-F0-07
- **T-F0-02:** migration 0001 includes `threads` and the FTS triggers.
- **T-F0-07:** implements VT-01…VT-20, plus VT-21/22 if time allows.

### specs/tasks/umbral-f1-tasks.md → T-F1-01, T-F1-02, T-F1-13
- **T-F1-01:** no longer creates `threads`; adds `client_msg_id`.
- **T-F1-02:** covers REQ-SEC-008 with the test `TestKeyringUnavailableDisablesProviders_REQ_SEC_008`.
- **T-F1-13:** covers REQ-AGT-015 with the test `TestSendIdempotentByClientMsgID_REQ_AGT_015`.

### tools/sdd_check.py
- **After:** the migration 0001 simulation includes `threads`.

## REMOVED
— (none)
