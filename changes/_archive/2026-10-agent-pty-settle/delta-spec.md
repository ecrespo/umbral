# Delta — a cancelled command's shell is settled before the agent's next one

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-10-22 by the Tech Lead ("Ratifico los deltas"); folded into Tech 1.55 §5.3b` |
| **Date** | 2026-10-22 |
| **Task** | T-HRD-03 (the AGT_007 flake), raised by F1's closing work |
| **Raised by** | `TestACancelBeforeTheCommandStartsStillStopsIt_REQ_AGT_007` failing now and then on macOS and Ubuntu (CI run 39181065284): `echo still-here` ran as `cho still-here` |

## Evidence

**Measured, outside the daemon.** A harness drove real bash 5.3 with `shell/bash/umbral.bash`
the way `stopAgent` does: SIGTERM to the shell's children and SIGINT to the shell, repeated
until `133;D` was read, then the next command typed at once. 4 of 200 next commands ran without
their first byte (`633;E;cho still-here`). Waiting for the prompt (`133;B`) and 50 ms of silence
first did not help (4/300); a leading Ctrl-U did not help (4/300): readline returned the whole
line, and the parser dropped its first byte. An interrupt that reaches the shell after the
command has ended stays pending and is spent on the next line parsed. An empty line typed first
took it: 0 of 900. The daemon's own test, interrupting every millisecond, failed at round 21 of
the first run under zsh and passes 200 rounds with the empty line.

**The empty line exposed an older hole.** With a hook of the user's own in bash's
`PROMPT_COMMAND` (Starship, direnv), an empty line opened a block named after the hook: the
integration armed its DEBUG trap at the end of one prompt and kept it armed through the next
`PROMPT_COMMAND`. The agent's claim fell on that block and its command ran unrecorded
(spec-guardian, reproduced by `TestAPromptHookIsNotTakenForTheNextCommand_REQ_AGT_003`, red
before the fix). A person pressing Enter on an empty prompt got the same phantom block
(REQ-BLK-001).

## Decisions

1. **After interrupting a thread's shell, the agent's next command is typed after an empty
   line.** Only after an interrupt: an uninterrupted PTY gets the command alone. The empty line
   runs nothing and opens no block, so the claim falls on the command.
2. **The bash integration disarms its DEBUG trap in the first `PROMPT_COMMAND` hook**
   (`__umbral_save_status`), so no hook of the prompt is taken for a command. A real command fires
   the trap before the prompt runs, so nothing it recorded is lost. zsh's `preexec` does not run
   for an empty line, and fish's neither; neither changes.
3. **A person who takes the thread's input after a cancel is not protected** by decision 1: the
   empty line is typed for agent input only. Accepted; the case needs a human typing into an
   agent's PTY within moments of a cancel.

## Specification changes

- **Tech** §5.3b, `run_command`: one sentence on decisions 1 and 2. Tech 1.55.

## Not modified

No REQ changes: REQ-AGT-003 and REQ-AGT-007 already say what must hold, and this is how it holds.
No API change.
