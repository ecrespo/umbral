# Delta — `terminal_interact`, the input lock and take-over

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-26` by the implementing agent under the Tech Lead's standing F2 instruction (extended to F2b … F2f on 2026-10-26), pending the Tech Lead's review |
| **Date** | 2026-10-26 |
| **Task** | T-F2-20 |
| **REQ** | REQ-FTU-001, REQ-FTU-002, REQ-FTU-003 |

## What the PRD and the task left open

REQ-FTU-001 names the three actions and the risk, not which terminal they drive, what a key is,
what a screen read returns or how long a wait lasts. REQ-FTU-002 names an ownership and a
take-over chord without saying how ownership starts, how the agent gets it back or what the chord
reaches in the daemon. REQ-FTU-003 asks to check "the line the pane's shell would receive" across
`send_keys` calls, which is undefined once a program other than the shell is in the foreground:
read as shell, `SELECT * FROM t;` has a glob and asks, and so would every `psql` query.

## Decisions

1. **The terminal is the thread's PTY.** `terminal_interact` drives the session `run_command`
   already uses (REQ-AGT-003): owned by the thread, input held by the agent from its creation. No
   user pane is ever driven, which keeps the tool off the user's shells. The two tools share the
   PTY: while a program `terminal_interact` started is in the foreground, `run_command` answers
   that the terminal is busy, and `run_command` refuses while text `terminal_interact` typed is
   still on the prompt's line.
2. **Actions.** One tool, `terminal_interact`, with `action`:
   - `send_keys` with `keys`, 1 to 64 entries, each `{"text": "…"}` or
     `{"key": NAME, "ctrl"?, "alt"?, "shift"?}`. A text is printable UTF-8 only — no C0 control
     character, no DEL, no ESC — at most 4096 bytes over the call; it is written as typed. A key
     is one of `enter`, `tab`, `backspace`, `escape`, `space`, `up`, `down`, `left`, `right`,
     `home`, `end`, `page_up`, `page_down`, `insert`, `delete`, `f1` … `f12`, or a letter or
     digit with `ctrl` or `alt`, and is encoded by the session's emulator with libghostty's key
     encoder set from the terminal's own modes (application cursor keys, keypad, Kitty
     keyboard), so `up` reaches `vim` as `ESC O A`. Raw bytes and escape sequences are not
     accepted: they could type what no approval saw.
   - `read_screen`: the plain text of the emulator's screen, its last 64 KiB, with the cursor's
     cell, the size and whether the shell is at its prompt.
   - `wait_for` with exactly one of `regex` (RE2, matched against the screen's plain text now
     and after every output), `idle_ms` (100 to 10 000: no output for that long) or
     `exit: true` (the program in the foreground ended and the shell is at its prompt again, or
     the session exited), within `timeout_ms` (default 10 000, at most 120 000). It returns which
     ended it — `matched`, `idle`, `exited` or `timeout` — and the screen as `read_screen` does. A
     timeout is a result, not an error.
3. **Risk.** `send_keys` is `Exec`, as REQ-FTU-001 says. `read_screen` and `wait_for` type
   nothing and are decided as `ReadOnly`: the registry lets a tool that declares it
   (`Spec.ReadOnlyCalls`) lower one call to `ReadOnly`, never raise or change it otherwise. The
   tool is listed by its declared risk, so `ask` mode does not offer it (REQ-AGT-009). A read
   uses the thread's PTY only when it has one in the turn's cwd: it never starts a shell, whose
   startup and prompt run what the repository's configuration names, nor replaces one, and
   answers `no_terminal` instead.
4. **What the policy decides on.** At the shell's prompt — shell integration `osc133`, no block
   open and no agent command in flight — the keys are input to the shell's line editor:
   - only text, `backspace` and a final `enter` or `ctrl+c` (which empties the line) are
     accepted — the kernel flushes what follows an interrupt in the same write, and the shell
     may drop the first byte of its next line, so the next call writes an empty line first and
     waits up to 500 ms for its prompt, which then cannot withdraw the claim of the line after it;
     any other key there is refused, because tab completion, history and line-editor bindings
     make the line the shell receives unknowable. A command is typed as text;
   - the daemon keeps the line the agent has typed since the last `enter` or `ctrl+c`, across
     calls (REQ-FTU-003), and an `enter` submits it: the call's target is that whole line, decided
     exactly as `run_command`'s, destructive patterns and the shell parser included; it is
     recorded as a block with origin `agent` and the thread's id, which `wait_for` can wait on;
   - a call that does not submit is decided on the keys as typed.
   Anywhere else — a program in the foreground — the keys are the program's input. The daemon
   keeps that input since the program's last `enter` or `ctrl+c` too, across calls, and the
   target is it with the call's keys, rendered as text (`SELECT 1;<enter>`, `<ctrl+c>`), so a
   line split across calls is decided whole. Each line is matched against the destructive
   patterns as written, without the shell parser, so `DROP TABLE x;` in `psql` asks and
   `SELECT *` does not. A program may leave its input unread — `sleep` does — and the shell then
   reads it as its next line when the program ends, so each line is also read by the shell
   parser: a pattern it finds asks, and a line with shell expansion syntax in it (`$name`, `${`,
   `$(`, a backquote) is decided by the parser alone, dynamic words included, so
   `x=rm; $x -rf ~` asks; `SELECT $1` does not, a backquoted MySQL identifier does. `full-auto`
   never runs program input unasked: it has no cwd that bounds what it does. An allow rule
   covers a line only when it covers the line and every command a shell would read in it. Text
   typed to a program and never entered is dropped from the shell's next line by the agent's
   next write at the prompt (decision 6). Residuals, written
   down: a line built at run time without that syntax and typed ahead to a program that does
   not read it, and a shell nested in the program, are matched as text and by the patterns only;
   the parser's `dynamic_word` reading is not applied to program input without that syntax,
   since a query's `*` would make every line ask. The parser's `stdin_script` reading asks, and
   so does `unparseable` when the shell would read on into the next line (an open quote, a
   trailing `|` or backslash); a line it rejects outright, as SQL's parentheses are, runs
   nothing in a shell and does not ask. The lines are read the way a terminal sends them: a
   `space` key is a space, `ctrl+m` and `ctrl+j` end a line as `enter` does, and `ctrl+h` is
   a backspace. What `ctrl+c` and `ctrl+d` do to the text before them is the program's, so one
   with text before it and more after it on the same line asks as `unmodelled_key` (at a
   line's edge both readings are one) and a rule is matched against both readings, so a
   deny still denies; neither ends the line a later call adds to; all
   the lines are matched joined as well, as a shell reading them would. Any other key but
   `ctrl+c` and `ctrl+d` — `ctrl+o`, history, completion,
   `escape`, a yank, a search, a cursor move — can make the line the program runs one the
   target does not show, so it asks as `unmodelled_key` on the destructive floor, never as
   `always` and never under an allow rule (security review of 2026-10-26).
5. **The decision holds for the terminal it saw.** At run time the tool computes the target
   again from the terminal's state and refuses one that differs from the grant's, target or
   program flag; the verdict depends on nothing else of the state, so an equal pair is the
   decision the grant made. That state is then checked again under the session's input lock
   just before the keys are written; if it changed, the call fails with "the terminal changed"
   and nothing is written. Only the window between that check and the kernel write remains.
6. **Take-over (REQ-FTU-002).** Two new methods, for the `tui` and `desktop` kinds, refused to an
   agent's terminal (API §2):
   - `thread.take_over {thread_id}` → `{session_id}` moves the thread's PTY's input to the
     human at once, under the same lock every write takes, and announces `session.input_owner`.
     The agent's next `terminal_interact` call, and every one after it while the user holds the
     terminal, returns the result `taken_over` and writes nothing; `run_command` is refused the
     same way. A thread with no PTY is `NOT_FOUND`.
   - `thread.hand_back {thread_id}` → `{}` gives the input back to the agent; a terminal not
     taken over is left as it is. It types nothing: the agent's next write at the prompt —
     `send_keys` or `run_command` — starts with `ctrl+e ctrl+u`, end of line and kill to its
     start in the line editors' default bindings, so whatever the user left there, now or typed
     ahead while a program ran, is dropped and the shell receives the agent's line alone. A
     `ctrl+c` was tried first and lost the race with a shell still drawing its prompt. The same
     discard follows a program that ends with text the agent typed to it and never entered, which
     the tty hands to the shell's next line. Residuals: a line editor that rebinds those keys or
     is left in vi command mode, and a continuation line (a trailing `\`, an open quote) the
     user left, are not fully cleared.
   - A call decided while the user held the terminal was decided as a read, and writes nothing
     even if it runs after the hand-back; a send checks its target and whether it is a program's
     input against the grant's.
   - The connection that took a terminal over hands back whatever it still holds when it
     closes, so a client that goes away does not keep the agent out.
   The daemon never looks for the chord in `session.input`: a byte sequence there could be
   typed or echoed by a program. The TUI binds the chord, `ctrl+]`, in the agent panel: it takes
   the selected thread's terminal over and shows it in place of the panel, its keys going to that
   session — every key but `ctrl+]` and `ctrl+q`, `ctrl+c` included; `ctrl+]` again hands it back.
7. **While the user holds it,** the thread's PTY is never replaced: a turn whose cwd moved is
   refused `taken_over` instead of closing the user's shell.
8. **A program left running.** A cancelled turn ends its `terminal_interact` call; the program
   the agent started stays in the foreground, as the user would see it. The agent ends it with
   its own keys, or the user does after taking over.
9. **Keys at the prompt return once the shell answered them** (added 2026-10-26, after CI ran
   `cho` for `echo pending`). A call at the prompt that enters nothing waits for the shell's
   output after its write to pause for 50 ms, up to 500 ms. Text the line editor had not read
   was still in the tty's input queue, where the `ctrl+c` of the next call flushed part of it;
   and an interrupt is acted on only when the editor next looks, so the line typed after it
   could be gathered first and enter what it dropped. The editor answers both by writing — the
   echo, `^C` and the prompt again.

## Tests

- `TestSendKeysUsesTheEmulatorsEncoder_REQ_FTU_001` (`internal/sessions/adapters/ghostty`):
  `up` is `ESC [ A`, and `ESC O A` once the program asked for application cursor keys.
- `TestTerminalInteractDrivesAProgram_REQ_FTU_001` (`internal/sessions/integration`): a line
  submitted at the prompt starts a block with origin agent; keys reach the program; `read_screen`
  shows its output; `wait_for` ends `matched`, `idle`, `exited` and `timeout`; text typed at the
  prompt is on the screen when the call returns, and so is the shell's answer to a `ctrl+c`.
- `TestTakeOverLocksTheAgentOut_REQ_FTU_002` (`internal/sessions/integration`): after a
  take-over the agent's keys and `run_command` are refused `taken_over` and nothing reaches the
  PTY, the human's input is accepted, and a hand-back at the prompt empties the line the user left.
- `TestTakeOverMethods_REQ_FTU_002` (`internal/api`): the two methods, their kinds and their
  refusal to an agent's terminal.
- `TestTheTUIChordTakesTheThreadsTerminal_REQ_FTU_002` (`internal/tui`).
- `TestAssembledLineAsksOnEnter_REQ_FTU_003` (`internal/tools/adapters/builtin`): `rm -` in one
  call and `rf /` with `enter` in the next ask as destructive; editing keys at the prompt are
  refused; a read is `ReadOnly`.
- `TestProgramInputIsMatchedAsText_REQ_FTU_003` (`internal/security/domain`): `SELECT * FROM t;`
  does not ask, `DROP TABLE x;` and `x=rm; $x -rf ~` do; full-auto asks.
- `TestProgramTailIsTheLineNotYetEntered_REQ_FTU_003` (`internal/security/domain`).
- `TestProgramPendingIsTextAnInterruptLeft_REQ_FTU_003` (`internal/security/domain`): only text after the last ctrl+c makes the next write at the prompt drop the line, since an interrupt flushes the tty.
- `TestProgramInputKeysTheMatcherDoesNotModelAsk_REQ_FTU_003` (`internal/security/domain`): line-end keys, `space`, unmodelled keys, stdin scripts and lines the shell would join.
- `TestReadsNeverStartATerminal_REQ_FTU_001`, `TestAnInterruptKeepsTheAgentsNextBlock_REQ_FTU_001`
  (`internal/sessions/integration`).
- `TestADecisionWhileTakenOverSendsNothing_REQ_FTU_002` (`internal/tools/adapters/builtin`).
- `TestAProgramsUnenteredInputNeverJoinsTheNextLine_REQ_FTU_003` (`internal/sessions/integration`).
- `TestTheDecisionHoldsForTheTerminalItSaw_REQ_FTU_003` (`internal/sessions/integration`).

## Folded into

- API 1.54: §2 (`thread.take_over` and `thread.hand_back` refused to an agent's terminal), §5.43,
  §6 (`session.input_owner` cites REQ-FTU-002).
- Tech 1.74: §5.2 (`tools/adapters` may use `AgentKeys`), §5.3g; Tech 1.76: §5.3g, decision 9.
- PRD F2 0.11: REQ-FTU-003 says how a program's input is decided.
- `specs/tasks/umbral-f2-tasks.md`: T-F2-20 broken down and done, its traceability rows.

## Review

spec-guardian's pass (FIX FIRST) found keys typed to a program reaching the shell undecided as a
line (split across calls, or typed ahead to a program that does not read them), reads able to
start or replace a shell, a call decided while taken over able to write after the hand-back, no
release when the holder goes away, a hand-back's `ctrl+c` missing a line typed ahead during a
program, a hand-back of a free terminal wiping the agent's line, and the claim of the line after
an interrupt withdrawn by the empty line's prompt. Each is fixed above with a test. The verify
pass found a program's unentered input joining the agent's next prompt line and an allow rule
covering a program line a shell would split; both are fixed with a test.
