# Delta — the `umb` command-line surface, and the packages that hold the socket path

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-09-20 — folded into specs/` |
| **Date** | 2026-09-20 |
| **Task** | T-F0-11 |
| **Approved by** | Ernesto Crespo |
| **Raised by** | `spec-guardian` review of T-F0-11: findings 1, 3 and 7 |

## Evidence

Three things T-F0-11 had to decide are decided nowhere in `specs/`.

**1. What "the current session" is, and what happens outside one.** REQ-CLI-002 says
`umb block last --json` prints "the last closed block of **the current session**". Nothing
says how the CLI learns which session that is, and nothing says what it should do when
there is no current session, which is every invocation from an ordinary terminal. API Spec
§5.17 takes `{block_id | "last", session_id?}` without saying what `"last"` means when
`session_id` is absent. The implementation answers both, but only in a code comment — the
one place Art. 9 says a decision must not live.

**2. Where the socket path lives.** `internal/client` cannot import `internal/api`, so the
runtime-directory resolution moved to `internal/config`. That is the right call and
`task arch` agrees, but the rule it obeys exists only in `.go-arch-lint.yml`: Tech Design
§5.1 lists neither `api`, `client`, `config`, `store` nor `tui`, and the §5.2 table has no
row for any of them. Both the code and the tasks file cite "Tech Design §5.2" for a
sentence that is not there.

**3. Everything about the CLI except exit code 69.** The PRD names `umb status`,
`umb block last --json` and exit 69. It does not name `--socket`, `--daemon-path`,
`--no-autostart`, `--json` on `status`, the 0/1/69 exit-code table, the human-readable
output, or what happens when a write fails. Each is a reasonable decision; a script author
can find none of them.

## MODIFIED

### specs/prd/umbral-mvp.md → REQ-CLI-002, and a new REQ-CLI-004

- **REQ-CLI-002, before:** "…the last closed block of the current session…".
- **After:** the same, plus what identifies the current session (`UMBRAL_SESSION_ID`, the
  variable the daemon injects into every managed pane, Tech Design §5.2b) and what happens
  without it.
- **REQ-CLI-004 (new, MUST):** the exit-code contract, because it is the only thing a
  script can branch on.

### specs/api/umbral-daemon-api-v1.md → §5.17 `block.get`

- **After:** `"last"` with a `session_id` means the last closed block of that session;
  without one it means the last closed block of the whole history. Stated because a client
  cannot otherwise tell "no block in this session" from "no block anywhere".

### specs/technical/umbral-architecture.md → §5.1 and §5.2

- **After:** §5.1 lists `internal/api`, `internal/client`, `internal/config`,
  `internal/store` and `internal/tui`; §5.2 gains the rows `api → bus, store, config`,
  `client → config, never api`, and `tui → client`, with the reason: the client and the
  server of one protocol must not depend on each other, so what they share lives in
  `config`.

### specs/technical/umbral-architecture.md → §9.4 (new): the `umb` surface

- **After:** the commands, the common flags, the exit codes, and the two output rules —
  `--json` prints one object per line, and a failed write is exit 1 except for `EPIPE`,
  which is what `| head` does on purpose.

## NOT MODIFIED

No method, schema or error code changes; `protocol_version` stays at 1. This delta writes
down decisions the implementation already made and that nothing else can observe.

## Verification

- `python3 tools/sdd_check.py` exits 0 with REQ-CLI-004 carrying a task and a matrix row.
- `TestBlockLastUsesTheSessionFromTheEnvironment_REQ_CLI_002` already pins the
  `UMBRAL_SESSION_ID` half; `TestBlockLastExits69WhenTheDaemonIsUnavailable_REQ_CLI_003`
  and `TestAutostartFailureExits69_REQ_CLI_003` pin the exit codes.
- `task arch` stays green: the §5.2 rows describe what `.go-arch-lint.yml` already enforces.
