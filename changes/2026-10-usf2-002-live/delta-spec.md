# Delta — US-F2-002 live: what the run taught `terminal_interact`

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-26` by the implementing agent under the Tech Lead's standing F2 instruction (extended to F2b … F2f on 2026-10-26), pending the Tech Lead's review |
| **Date** | 2026-10-26 |
| **Task** | T-F2-23 |
| **REQ** | REQ-FTU-001, REQ-AGT-006, REQ-OBS-002, REQ-RTR-002 |

## What the task left open

T-F2-23 runs US-F2-002 live (PRD F2 §9): Umbral's own agent, on `gpt-oss:20b`, answers a `psql`
prompt and a `git rebase -i` editor through `terminal_interact`, ten runs, at least 70 % green
(PRD F2 §4). Delta `2026-10-terminal-interact` designed the tool against its spec; the first
smoke runs showed the shape a local model actually writes, and four failures no offline test had
reached. Each is the tool or the runtime refusing or misreading a call a model makes, not the
model's work.

## Decisions

1. **A key may be a string.** `keys` items are `anyOf` a string or the key object. A string is
   text, with each line end in it (`\n`, `\r`, `\r\n`) sent as `enter`; an empty string is no
   key. Ollama's rendering of the nested `oneOf` reaches `gpt-oss` as an untyped list, and the
   model answers `["psql -U postgres\n"]`. The string is decided as the keys it stands for, so
   at the prompt its `enter` submits the whole line, decided as `run_command`'s, and a
   program's input is matched line by line as before (REQ-FTU-003). A key named between angle
   brackets in a string — the notation the tool renders keys in and vim's: `<Esc>`, `<CR>`,
   `<Tab>`, `<PageDown>`, `<C-c>`, `<ctrl+c>`, `<alt+x>`, case aside — is that key; a bracketed
   word that names none (`<html>`, `a<b`) is text. The model wrote `"<Esc>"` to vim and it was
   typed. A string that is one control character and nothing else is the key that encodes it:
   ESC is `escape`, LF and CR `enter` as in text, TAB `tab`, DEL and BS `backspace`, the other
   characters of `0x01`–`0x1a` ctrl and a letter; the
   model wrote vim's escape as `"\u001b"` in a list of keys, and the refusal cost it the
   rebase. A named key adds no capability: it is the key object written another way, and asks
   as one does. Escape sequences, a control character inside text and NUL are still refused.
2. **A zero value is absent.** The model fills every optional field: `exit: false`,
   `idle_ms: 0`, `regex: ""`, `timeout_ms: 0`, `keys: []`. Each is read as not given, so
   `read_screen` with them is a read, `wait_for` needs one non-zero condition, and `idle_ms`
   and `timeout_ms` take 0 in the schema. A non-zero `idle_ms` below the minimum is still
   refused.
3. **`send_keys` may wait.** A `send_keys` that also carries a condition (`regex`, `idle_ms` or
   `exit`) sends its keys and then waits as `wait_for` would, within `timeout_ms`, and answers
   with the send's summary, the wait's reason and the screen it ended on. A `timeout_ms`
   without a condition waits for nothing. The call is decided on its keys alone; the wait is a
   read. A key taken over between the two answers `taken_over` and does not wait. A
   `read_screen` that carries a condition is a `wait_for`: it answers with the screen once the
   condition holds, and stays a read. In the first measured run the model asked for the screen
   "when `pick` appears" and the refusal, repeated by its repair, ended the turn `tool_error`.
4. **A wait after keys reads only what came after them.** The wait of decision 3 matches its
   regex against the plain text the terminal wrote after the keys — a thread's own terminal
   keeps its last 256 KiB of output by `seq`, a user's pane none, and the send reports the `seq`
   it wrote after — never against the screen. When the keys end with `enter`, the terminal
   echoes them as a line, so the wait reads only what comes after that line's end: in the
   measured runs the model sent `psql -U postgres` waiting for `postgres`, and `\q` waiting
   for `$`. A regex that matches the empty string (`$`, `.*`) holds before any output, so it is
   read as a wait for 500 ms of quiet. Keys that end otherwise are echoed in place, and a regex
   they hold may still match their echo: a residual. In the smoke the model sent `\dt` with a wait for `postgres=#`, and the prompt still
   on the screen from before matched at once; the model read the old screen as the answer and
   sent the same keys again for twenty minutes. A plain `wait_for` keeps matching the screen,
   which is what a model asks for when it calls it alone.
5. **Conditions combine.** A wait takes one or more of `regex`, `idle_ms` and `exit` and ends
   at the first that holds; output that matches as it goes quiet is a match. The model asked
   for "`postgres=>` or two seconds of quiet" in one call, and "exactly one of" refused it,
   then refused its repair, ending the turn `tool_error`.
6. **A tool call the provider cannot read is the model's invalid call.** Ollama answers a tool
   call it cannot parse out of the model's output — `gpt-oss` wrote prose before the JSON, or a
   `\x1b` JSON has no escape for — with `error parsing tool call`: an HTTP 500 before any event,
   or, while streaming, an error line after the reasoning it already sent, which is how every
   unreadable call of the measured runs came. The Ollama adapter marks both
   `ErrMalformedToolCall`. It stays a retryable 500, so the next
   candidate is tried, but it does not count against the provider's circuit breaker
   (REQ-RTR-002): the provider answered, and three unreadable steps would otherwise shut the only
   local provider for every thread. When the router gives up and any candidate failed that way,
   or the stream fails that way after its first event, where no fallback is possible, the
   runtime counts an invalid call against the model (`umbral_tool_calls_invalid_total`,
   REQ-OBS-002) and asks the same step again once with a repair message, as REQ-AGT-006
   requires: there is no call to answer, so it is a user message — the call could not be read as
   JSON, write one object matching the schema with JSON's own escapes only — added to the
   request and never stored. A second unreadable step, or one in a step that was a repair, is
   counted too and ends the turn with `tool_error`. What the failed step streamed before the error is
   kept as any failed step's is — its reasoning shown, its text, if any, stored — and the log
   line names the model, not the error, which carries the model's output.
7. **A screen opens with who has the keyboard.** A screen in a result starts with
   `[the shell is at its prompt; cursor at …]` or `[a program is in the foreground, not the
   shell: type its input, not commands; cursor at …]`, then the text; it used to end with it.
   The tool's description says to read that line, and that an editor such as vim starts in
   normal mode. In the stopped first measurement the model read the screens' tops, missed that
   psql had exited, typed `\q` to the shell and then its `docker exec` into the new psql.
8. **The live test.** `cmd/umbrald/usf2002_live_test.go` (tag `live`) starts a hermetic daemon
   with `gpt-oss:20b` on the local Ollama, a Postgres in Docker for the `psql` task and a
   repository whose last three commits are to be squashed, with `EDITOR=vim`, for the rebase.
   It answers each ask as a user reading it would: a `terminal_interact` call is approved once
   unless its summary holds a destructive command (`rm -`, `DROP`, `--force`, `reset --hard`,
   `push`, vim's `:!` …), so the asks for keys the matcher does not model — vim's escape, on
   the destructive floor — are approved, and every other tool's ask is denied. It checks each task by its result —
   the table's rows in Postgres, two commits with the work of all three and no rebase in
   progress — not by the model's words. It writes the report and each run's transcript, and a
   turn that does not end within 10 minutes is cancelled and logged with the calls still
   pending, and every call's full result is read from the daemon's `tool_calls`. The daemon
   is started with `GIT_EDITOR=vim`: the harness that ran the smoke exported `GIT_EDITOR=true`,
   which the daemon passed on to the agent's shell, and git, which reads it before `EDITOR`,
   rebased at once without opening vim — every failed rebase of the smoke runs was that, not
   the model.

## Tests

- `TestAKeyMayBeAString_REQ_FTU_001`, `TestAStringMayNameKeys_REQ_FTU_001`,
  `TestALoneControlCharacterIsItsKey_REQ_FTU_001`, `TestSendKeysMayWaitAfter_REQ_FTU_001`,
  `TestAReadScreenMayWait_REQ_FTU_001`, `TestTheScreenLeadsWithWhoHoldsTheKeyboard_REQ_FTU_001`
  (`internal/tools/adapters/builtin`): string keys, a lone control character, zero values as
  absent, send then wait with a fresh wait after the keys' `seq`, a plain `wait_for` on the
  screen, the holder line first.
- `TestAWaitAfterKeysSeesOnlyTheirOutput_REQ_FTU_001` (`internal/sessions/integration`): a real
  PTY; a fresh wait does not match a line already on the screen, a screen wait does, a fresh
  wait matches the output the keys caused, and a regex-or-quiet wait ends matched or idle, and after an entered line a
  program's answer matches while the line's echo does not.
- `TestOnlyAThreadsTerminalKeepsRecentOutput_REQ_FTU_001` (`internal/sessions`): a user's pane
  keeps no output, a thread's terminal at most 256 KiB.
- `TestAWaitMayCombineConditions_REQ_FTU_001` (`internal/sessions/domain`): one or more
  conditions accepted, none refused, the ranges kept.
- `TestAnUnreadableToolCallIsRetriedOnce_REQ_AGT_006`, `TestAnUnreadableRetryEndsTheTurn_REQ_AGT_006`
  (`internal/agents`): one retry with the repair message, never stored, counted against the
  model; a second ends `tool_error`.
- `TestAnUnreadableToolCallInTheStreamIsRetried_REQ_AGT_006` (`internal/agents`): the error
  after the reasoning retried the same way.
- `TestAnUnreadableToolCallIsCounted_REQ_OBS_002` (`internal/agents`): both steps counted.
- `TestAnUnreadableToolCallIsMarked_REQ_AGT_006` (`internal/llmgw/adapters/ollama`): the 500
  marked, still retryable, and the same error in the stream; another 500, a 503 and another
  error in the stream not marked.
- `TestAnUnreadableToolCallDoesNotTripTheBreaker_REQ_RTR_002` (`internal/llmgw`): the next
  candidate still tried, the breaker left closed.
- `TestUSF2002InteractivePrograms_REQ_FTU_001` (tag `live`): the report.

## Results

Measured on 2026-10-30, ten runs against `gpt-oss:20b` on the local Ollama
(`docs/reports/usf2-002-2026-10-30.md`): **0 of 10 runs green**, against PRD F2 §4's 70 %.
psql passed 10/10. The rebase failed 10/10, on the model's use of vim rather than the tool:
- the cursor left on `squash`'s last letter after `ciw … <Esc>`, so the next `j ciw` replaced a
  hash (runs 2, 3);
- `"Esc"` written as text (runs 1, 5);
- `"\u001b:"` refused, followed by calls Ollama could not parse and a failed repair, ending
  `tool_error` (runs 4, 9);
- normal-mode commands that did not do what the model meant, two of them looping to
  `run_timeout` (runs 3, 6, 7, 8, 10).

The daemon's properties held in every run. Each call was decided and recorded, and each
destructive ask reached the user (run 7's `rm -fr .git/rebase-merge`). Decision 6's retry
fired eight times and ended two turns as REQ-AGT-006 requires. Nothing was left behind.
Whether US-F2-002 passes is left to the Tech Lead, as T-F2-12's measurement was.

Residuals for the Tech Lead:
- **An ESC prefix inside text.** `"\u001b:"` is refused by decision 1. Reading a leading ESC
  as `escape` followed by text would widen "a control character inside text is refused".
- **vim's cursor.** The model does not track it across edits and does not read the screen
  between them, although the description says to.

## Folded into

- Tech 1.80: §5.3g decisions 1 to 5 and 7; §3.3 item 2 (repair) and DD-004's breakers, decision 6.
- PRD F2 0.13 §9: US-F2-002 measured.
- `specs/tasks/umbral-f2-tasks.md`: T-F2-23 done, its traceability rows.

## Review

Pending spec-guardian.
