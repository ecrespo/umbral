# Delta — `run_command` returns with the shell at its prompt

| Field | Value |
|---|---|
| **Status** | `PROPOSED`. The Tech Lead asked for the fix on 2026-11-19 knowing it changes Tech §5.3b; the text of this delta has not been reviewed. Written into Tech 1.96 §5.3b and API 1.62 §5.44 as proposed, awaiting ratification |
| **Date** | 2026-11-19 |
| **Task** | T-HRD-13 |
| **REQ** | REQ-FTU-003, REQ-AGT-003, REQ-RMT-002 (no change to their text). REQ-FTU-003 is the trace of the new test because the refusal it removes is that requirement's check — keys are written only into the terminal they were decided on — and the test that failed carries it; what moves is `run_command`'s return, REQ-AGT-003 |
| **Raised by** | `TestAProgramsUnenteredInputNeverJoinsTheNextLine_REQ_FTU_003` failing on `macos-latest` (run 44210237570, and once before on T-F2-42's pull request) |
| **Depends on** | — |

## Evidence

A command's block closes at OSC 133;D, which the shell writes from its prompt hook. The shell
is at its prompt at OSC 133;B, after every other prompt hook and after `PS1` is expanded.
`run_command` returned at the first, and between the two the session reads as neither at the
prompt nor running a program.

A `terminal_interact` call decided in that window was decided on "not at the prompt". `SendKeys`
then waited for the shell to settle, found the prompt, and refused the keys with "the terminal
changed since the call was decided" (decision 5 of delta `2026-10-terminal-interact`). The
refusal fails closed, so nothing was typed that had not been decided; the cost was a spurious
refusal of an agent's first keys after a command, on a prompt slow enough to be read in between.

Measured with a bash prompt that takes 300 ms to draw (`PS1='$(sleep 0.3)$ '`):
`TestRunCommandReturnsWithTheShellAtItsPrompt_REQ_FTU_003` read `AtPrompt: false` right after
`run_command` on every run. That is the state the runner's refusal comes from; the refusal's
own message was not reproduced locally.

## Decisions

1. **`run_command` returns once the shell has drawn its next prompt.** After the command's block
   closes, the call waits for a prompt drawn after the command was typed, for at most 500 ms,
   the bound §5.3g already uses for a prompt. An ACP terminal's command, typed by the same
   code, is not waited for: its session runs one command and is never typed into again, so
   the wait would only delay its exit.
2. **The wait changes nothing the call returns.** The exit code, the output and the persisted
   mark are the closed block's. A cancel, the command's own time bound or the shell's exit
   during the wait ends it, and the result is returned as before; the wait never extends the
   120 s or 600 s bound.
3. **A shell slower than the bound is not waited for.** A prompt that takes longer than 500 ms
   leaves the window open, and a call decided in it is refused as before. The refusal stays the
   safe answer; the bound keeps a broken prompt from holding a turn. The cost is latency:
   every `run_command` returns up to one 20 ms poll later, and one in a shell whose prompt
   takes longer than the bound returns 500 ms later, every time.
4. **A cancelled command is not waited for.** Its return is REQ-AGT-007's 500 ms and stays so.

5. **A dropped link's two announcements have no order.** `session.unsubscribed` with
   `remote_disconnected` comes from the relay's connection and `remote.state_changed`
   `disconnected` from the remote module; API §5.44 was silent on their order and now says
   there is none. `TestADroppedRemoteLinkComesBackAndItsPanesResume_REQ_RMT_002` assumed one.
6. **T-HRD-13's Done line names where the runs were made.** It asked for 50 runs at
   `-cpu 1,2,4` "on that OS", and three of the tests failed on macOS and one on Windows, which
   only the runners have. The line now asks for the 50 runs on Linux and a green run of the
   pull request on each runner. A failure seen again on a runner reopens the task.

## Spec changes

- Tech 1.96 §5.3b, the `run_command` paragraph: the call returns when the block has closed and
  the shell has drawn its next prompt, or 500 ms after the close; what ends the wait early.
- API 1.62 §5.44, "A dropped link": the two announcements come in no fixed order.
- `specs/tasks/umbral-hardening-tasks.md`: T-HRD-13's Done line and its matrix row.

## Open questions

- Whether a shell with a prompt slower than 500 ms should pay that on every command, or the
  bound should be lower. Recommended: keep it; it is the bound §5.3g already uses.
- Whether the runs on macOS and Windows should be a loop in CI instead of one green run per
  runner. Recommended: no; a recurrence reopens the task.

## Verification

- `TestRunCommandReturnsWithTheShellAtItsPrompt_REQ_FTU_003`: with the 300 ms prompt, the state
  read right after `run_command` is the prompt and keys decided on it are written, three rounds.
  Red first (`AtPrompt: false`); removing the wait reddens it again. The test raises the
  bound to 30 s, so a machine that draws the prompt slowly fails nothing.
- `TestAProgramsUnenteredInputNeverJoinsTheNextLine_REQ_FTU_003` and the new test: 50 runs each
  at `-cpu 1,2,4` under the race detector, green.
