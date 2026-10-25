# Delta — no lost last line in an agent's zsh

| Field | Value |
|---|---|
| **Status** | `PROPOSED 2026-10-24`, awaiting the Tech Lead |
| **Date** | 2026-10-24 |
| **Task** | T-HRD-10 |
| **REQ** | REQ-AGT-007, REQ-INT-001 (no change to their text) |
| **Raised by** | Q9 of delta `2026-10-hrd02-open-questions`, answered (a) and ratified by the Tech Lead on 2026-10-24 |
| **Depends on** | — (the branch is stacked on T-HRD-09's only so the Tech Design's version numbers follow) |

## Evidence

A thread's own PTY runs the user's shell. Before each prompt, zsh's `PROMPT_SP` writes a `%`,
a line of spaces and a carriage return. They land inside the block, so a last line printed
without a newline was erased from what `run_command` returned. Measured against a real zsh,
`printf x` came back as `" "`, and `echo promptsp=$options[promptsp]` reported `on`
(`TestAZshThreadKeepsAnUnterminatedLastLine_REQ_AGT_007`, red).

## Decisions

1. **The daemon marks the sessions it owns for a thread.** `UMBRAL_AGENT_SESSION=1` is set
   only in a session with an owner thread. That covers a thread's PTY and an ACP terminal's
   session.
   - Like every `UMBRAL_*` name, an inherited or declared value is dropped first, so a
     user's pane never carries it.
   - It is the one `UMBRAL_*` variable that is not part of REQ-INT-001's set. It is an
     internal switch for the bootstrap, not an integration's interface.
2. **The zsh bootstrap turns `PROMPT_SP` off in those sessions only.** It does so after the
   user's rc, again in `__umbral_precmd`, and again in `__umbral_preexec`.
   - zsh writes the mark before it runs the precmd hooks, so what counts is the option's
     state when the previous command ended.
   - `__umbral_precmd` runs after the hooks the user's rc registered.
   - `__umbral_preexec` runs after every precmd hook, including one a lazy loader appended
     after the bootstrap's, and just before the command whose output matters.
   - So a framework that sets the option at every prompt cannot bring the mark back.
   - Nobody types into these sessions, so nothing is lost.
3. **Nothing changes elsewhere.** A user's own zsh pane keeps `PROMPT_SP`. bash and fish have
   no such option.
4. **Traceability.** The tests carry REQ-AGT-007, as the task's REQ line names it. Their
   property, `run_command` returning a thread PTY's whole output, is also REQ-AGT-003's. The
   matrix cites both.

## Spec changes

- Tech 1.73 §5.2b gets a new paragraph:
  - `UMBRAL_AGENT_SESSION=1` in a thread-owned session;
  - dropped like the others;
  - read only by the zsh bootstrap, which turns `PROMPT_SP` off.

## Verification

- `TestAZshThreadKeepsAnUnterminatedLastLine_REQ_AGT_007`:
  - `printf x` in a zsh thread returns `x`;
  - the thread reports `PROMPT_SP` off;
  - a user's zsh pane reports it on;
  - skipped where zsh is absent.

  Red first: `" "` and `on`.
- `TestAZshThreadKeepsPromptSPOffUnderAFramework_REQ_AGT_007` has two cases. In one, a user rc
  sets `PROMPT_SP` from its own `precmd` hook every prompt. In the other, a hook appends itself
  after the bootstrap's at the first prompt. In both, `printf x` returns `x` three times.
- `TestNoPaneLocationIsInherited_REQ_INT_001` now checks `UMBRAL_AGENT_SESSION`, both
  inherited and declared in a pane's `env`. It is unset in the pane.
- Mutations:
  - not setting the variable fails both zsh tests;
  - dropping the `precmd` re-apply fails the rc-registered case;
  - dropping the `preexec` re-apply fails the late-appended case;
  - taking the name out of the dropped set fails the env test.
- Spec-guardian found nothing CRITICAL or HIGH. Its findings are fixed:
  - the precmd claim, now backed by `preexec`;
  - the untested drop;
  - REQ-AGT-003 traceability;
  - the task's file list.
