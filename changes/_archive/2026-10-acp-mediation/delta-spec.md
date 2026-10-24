# Delta — ACP mediation: permissions, files, terminals

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-24` by the implementing agent under the Tech Lead's standing F2 instruction; folded into API 1.49 §4, Tech 1.64 §5.3f and Data Model 1.26 §2.2 |
| **Date** | 2026-10-24 |
| **Task** | T-F2-08 |
| **REQ** | REQ-ACP-003, REQ-ACP-004, REQ-ACP-005, REQ-ACP-007 (the `tool_calls` rows), REQ-ORC-004 |

## What the PRD and the task left open

REQ-ACP-003 maps a kind to a risk but not to a target: it does not say where an `execute`
request's command or a `fetch` request's URL is read from, nor how a call with several
`locations` is decided. REQ-ACP-004 covers a write "an allowed permission … of the same turn
listed", but `fs/write_text_file` carries no tool call id in ACP, so only the turn-wide rule can
ever apply. REQ-ACP-005 says a terminal is "a new session owned by the thread" holding a block,
but not how a block is opened in a session, what `terminal/output` returns while the command
runs, or how a terminal ends. Nothing said what happens to a request that arrives between turns.

## Decisions

1. **The running turn mediates.** The agent's permissions, `fs/*` and `terminal/create` go to
   the sink of the turn running when they arrive, which the agent runtime implements: it holds
   the thread's mode, rules, write root, approval queue and assistant message. The `Mediator`
   port and its `Refuse` default are removed. A request that arrives between turns is refused
   (a permission with `reject_once`, the rest with an ACP error): it has no turn to own its row
   or its approval.
2. **What a permission is decided on.** The kind gives the risk (REQ-ACP-003) and the target:
   - `edit`, `delete`, `move`: each of the call's `locations`, resolved like `write_file`'s target
     (symlinks followed, relative paths against the session directory); a call with none asks;
   - `execute`: `raw_input.command` (a string, or an array joined with spaces) in
     `raw_input.cwd`, else `raw_input.workdir`, else the session directory (REQ-ORC-004); a call
     without a command asks;
   - `fetch`: `raw_input.url`; `read`, `search`, `think`: the first location or the title;
   - any other kind: `Exec` on the title, which asks unless a rule allows it.

   Each target is decided by the policy engine under the turn's mode and rules, and the strictest
   verdict wins: deny, then ask, then allow. The action's tool is the row's (`acp:<kind>`), so an
   approval remembered for a thread or always (API §5.25) matches the same kind and target again.
3. **The answer.** An allow selects the agent's `allow_once` option, anything else its
   `reject_once` one, and an `allow_always` option is never selected (unchanged from T-F2-07).
   An approval the user remembers is Umbral's rule, never the agent's.
4. **The reported call's row carries the decision.** The row the agent's `tool_call` made gets the
   risk and `decision` (`allow`, `deny`, `ask`), and an approval points at it; a permission whose
   target Umbral cannot resolve is refused and recorded `deny`; a permission for a
   call not reported yet makes its row first. Later updates keep the decision. A call never asked
   about stays `unmediated`.
5. **A write is covered by the turn.** `fs/write_text_file` has no tool call id, so a write to a
   path that an allowed permission of the same turn listed in its `locations` (compared after
   resolution) is allowed without asking, recorded `decision = allow`. Coverage ends with the
   turn.
6. **Files.** A path must be absolute (ACP requires it); a relative one is an ACP error, never
   resolved against a directory the agent did not name. A read is `ReadOnly` inside the write root
   and asks outside it (`outside_write_root`), in every mode; a write is `WriteFS`, its approval
   showing the diff `write_file` would. Umbral performs both with `read_file`'s and `write_file`'s
   own guards: a regular file of at most 16 MiB, opened without blocking, written by atomic
   rename through the resolved target. `line` and `limit` select lines of the raw content; a read
   returns at most 4 MiB, and past that it is an error asking for a window. Each request is a row
   of its own, `acp:fs/read_text_file` or `acp:fs/write_text_file`, `via = acp`, under the turn's
   assistant message, ended `ok`, `error`, `denied_by_policy` or `denied_by_user`.
7. **Terminals are thread-owned shells.** `terminal/create` is decided as `Exec` on its command
   line — `command` and `args` quoted for a POSIX shell, `env` as an `env NAME=value` prefix — with
   its `cwd` (or the session directory), so the destructive list and full-auto's cwd rule see it
   as they see `run_command`. Allowed, it gets a new session owned by the thread
   (`owner_thread_id`), with shell integration and no pane, and the line is typed as the agent; the
   block it opens has `origin = agent`, the thread's id and `via = acp`. The session's shell is
   bash, else zsh, and the terminal is refused without either: the line is quoted for a POSIX
   shell, which fish is not (its single quotes read `\'` as an escape), and bash comes first
   because zsh's `PROMPT_SP` mark — a `%`, a line of spaces and a carriage return written inside
   the block — erases a last line printed without a newline. The line is typed into the shell's
   line editor, which acts on a control character even inside quotes (Ctrl-U erases what came
   before it), so a control character in the command, an argument or `env` is an ACP error before
   anything is decided; `run_command`'s commands are refused one for the same reason. The policy
   also decides the *policy line* — the same words, each made one token by turning whitespace,
   separators, quotes and backslashes inside it into `_` — and the strictest verdict stands: the
   command normaliser splits without reading quotes, so `env 'A=x y' rm -rf /` alone reads as the
   command `y`, and `rm 'x;y' -rf /` as two harmless ones, while the shell runs `rm -rf /`. The
   approval shows the line as typed. A word that is shell syntax of its own — a shell's `-c`
   script, `eval`'s operand, `env -S`'s — is read by the normaliser without its quotes in either
   line, so a terminal that may hold one asks whatever else allows it (`reason = script`). The
   check reads no flag grammar and skips no word, so it fails closed: any word naming a program
   that runs a script it is given — a POSIX or C shell by name (`sh`, `bash`, `zsh`, `dash`, `ash`,
   `ksh`, `mksh`, `oksh`, `osh`, `ysh`, `hush`, `yash`, `posh`, `rbash`, `rzsh`, `tcsh`, `csh`,
   with or without a version or a suffix such as `-static`), fish,
   xonsh, elvish, nu, busybox, PowerShell, `cmd`, `ssh` (whose remote command is one), `eval`, or
   a runner whose operand is a shell line (`watch`, `su`, `script`, `flock`) — in any case,
   `.exe` or not, whatever its flags, or
   `env` followed by any option but `-i`, `-0`, `-u`, `--ignore-environment`, `--null` and
   `--unset=`, compared whole. An interpreter's own code (`python -c`, `perl -e`) is not shell
   syntax the normaliser reads: what it does is outside the destructive list, as any program's is.
   The list is a floor like the destructive list it guards, not a sandbox, and it errs towards
   asking; a real shell tokenizer for every command line is T-HRD-02's. A cwd outside the write root asks even when a
   remembered rule allows the line (REQ-ACP-005). Its row is `acp:terminal/create`, ended `ok`
   once the command's line is written to the shell; a claim or write that fails is the request's
   error and closes the session. A terminal session is never taken for the thread's own PTY.
8. **What a terminal serves.** `terminal/output` returns the plain text the command printed so far
   — the stream stripped of escape sequences as it arrives, its unfinished last line included —
   cut from the start to
   `outputByteLimit` (default 1 MiB) with `truncated` set, and the exit status once there is one.
   `wait_for_exit` waits for the block to close and answers its exit code; a signal is not known
   from the shell's report and is left out. `kill` stops the command's process group, as a
   cancelled `run_command` is stopped, and keeps the session; `release` kills a running command
   and closes the session. A terminal lives until its release, the agent's process ending, or the
   thread's agent being closed.
9. **The cap.** At most `[acp] max_terminals` terminals not yet released per thread; the next
   `terminal/create` is an ACP error naming the limit, decided before anything is asked or
   recorded.
10. **Waiting is not silence.** A request of the agent's stays outstanding while it waits for an
    approval or a terminal's exit, so the silence timer is paused (REQ-ACP-006, unchanged).

## Open

- A terminal's 500 ms termination on cancel is T-F2-09's (REQ-ACP-008).
- The plain text is a stripping of the stream, not the emulator's screen: a program that redraws
  a line (a progress bar) shows every version of it. The block's own output, recorded at its end,
  is the emulator's.
- The command normaliser reads no quotes, so a `run_command` line the model writes itself —
  `env 'A=x y' rm -rf /` — still hides its program from the destructive floor. An ACP terminal
  is safe because Umbral builds its words; the model's own lines need a real tokenizer, left to
  the Hardening phase (T-HRD-02).
- Seen while testing and not changed here: `run_command` in a zsh PTY loses a last line printed
  without a newline, for the same `PROMPT_SP` reason. It is the thread's own shell, the user's
  choice, and is left to the Hardening phase.

## Review

The spec-guardian pass found the real tool registry refusing `write_file`'s action without a
content — every `edit` permission and `fs/*` request was rejected while the tests' fake registry
accepted them — and a terminal's line holding control characters the shell's line editor would
act on. A security review then found the quoted line hiding its program from the destructive
floor, which the policy line closes. All were fixed with tests that fail without the fix; decisions 4 and 7 carry the rest.

## Tests

- `internal/agents`: `TestACPPermissionEntersTheApprovalQueue_REQ_ACP_003`,
  `TestACPWriteOutsideTheWorktreeAsks_REQ_ACP_004`, `TestACPReadOutsideTheWorktreeAsks_REQ_ACP_004`,
  `TestACPReadInsideTheWorktreeIsReadOnly_REQ_ACP_004`,
  `TestACPWriteCoveredByAPermissionIsNotAskedTwice_REQ_ACP_004`,
  `TestACPToolCallsAreRecordedWithTheirDecision_REQ_ACP_007`,
  `TestFullAutoDecidesACPPermissions_REQ_ORC_004`,
  `TestAnACPTerminalOutsideTheWriteRootAsks_REQ_ACP_005`,
  `TestAnUndecidablePermissionIsRecordedDenied_REQ_ACP_007`,
  `TestATerminalIsDecidedOnTheWordsTheShellSees_REQ_ACP_005`,
  `TestATerminalThatRunsAScriptAsks_REQ_ACP_005`.
- `internal/acp/domain`: `TestACPKindsMapToRisks_REQ_ACP_003`,
  `TestACallsInputNamesItsTargets_REQ_ACP_003`, `TestATerminalsCommandLineIsQuoted_REQ_ACP_005`,
  `TestATerminalsControlCharactersAreRefused_REQ_ACP_005`,
  `TestATerminalsPolicyLineKeepsItsWords_REQ_ACP_005`,
  `TestATerminalThatRunsAScriptIsFlagged_REQ_ACP_005`.
- `internal/sessions`: `TestATerminalRunsOnlyInAPOSIXShell_REQ_ACP_005`;
  `internal/sessions/domain`: `TestAnAgentCommandWithAControlCharacterIsRefused_REQ_ACP_005`.
- `cmd/umbrald`, against the daemon's own tool registry: `TestACPFilesAreMediatedInTheDaemon_REQ_ACP_004`.
- `internal/acp/adapters/sdk`: `TestACPNeverSelectsAllowAlways_REQ_ACP_003`,
  `TestACPTerminalsAreCapped_REQ_ACP_005`, `TestAnACPTerminalServesItsOutputAndExit_REQ_ACP_005`,
  `TestACPFileRequestsArePerformedOnTheDecidedTarget_REQ_ACP_004`.
- `internal/sessions/integration`: `TestAnACPTerminalIsItsOwnThreadSession_REQ_ACP_005`.
- `cmd/umbrald`, against a real daemon: `TestACPTerminalIsAnAuditedBlock_REQ_ACP_005`.
