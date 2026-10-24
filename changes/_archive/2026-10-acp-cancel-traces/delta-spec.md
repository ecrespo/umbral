# Delta — ACP cancel, traces and disclosure

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-24` by the implementing agent under the Tech Lead's standing F2 instruction; folded into API 1.50 §5.21, Tech 1.65 §5.3f and §9.4 |
| **Date** | 2026-10-24 |
| **Task** | T-F2-09 |
| **REQ** | REQ-ACP-007, REQ-ACP-008 |

## What the PRD and the task left open

REQ-ACP-008 says a cancel terminates "the thread's ACP terminals within 500 ms" but not whether
a terminal's session goes with its command, nor in which order the terminals and
`session/cancel` are handled. REQ-ACP-007 asks for "one span per agent tool call and one per
mediated request" without naming them or saying when a span of a call reported over several
updates ends. It asks that the user be shown, at creation, what Umbral does not record, without
saying where `umb` and the TUI say it. API §5.21 promised that `thread.cancel` answers within
500 ms, which a hosted turn cannot keep: the turn ends only when the agent answers the cancel.

## Decisions

1. **The terminals first.** A cancelled hosted turn kills the command of every terminal the
   agent holds (SIGTERM to its process group, SIGKILL 300 ms later, as `run_command`'s cancel)
   **before** it sends `session/cancel`, so REQ-ACP-008's 500 ms does not depend on the agent.
   The terminals' sessions are kept: they are the agent's until it releases them, and its
   `terminal/output` and `wait_for_exit` keep answering with the exit the kill produced. A
   terminal whose session was still starting when the sweep ran is killed as it is registered;
   the sweep lasts until the next turn begins, whose terminals run. The process is killed if the turn has not ended 5 s after the cancel, unchanged from T-F2-07.
2. **`thread.cancel` on a hosted thread** answers when the turn has ended: as soon as the agent
   ends it, or 5 s later when its process is killed. Its 500 ms is the terminals', not the
   answer's.
3. **Spans.** A hosted turn's trace is the turn span Umbral's own turns have (Tech §7.1). Under
   it:
   - each tool call the agent reports is a span `tool.acp:<kind>` with the call id, started by
     its first update or permission and ended by the first update that is not `pending` or
     `in_progress`; a call still open when the turn ends is ended then, with status `pending`;
   - each mediated request (`fs/read_text_file`, `fs/write_text_file`, `terminal/create`) is a
     span `tool.acp:<method>`, started when it arrives — before it is decided — and ended with
     its outcome, or as an error when it is refused before its row is written.
4. **The disclosure.** `thread.create`'s answer already carries `egress_visibility`; clients say
   it where the user reads:
   - `umb thread create --agent NAME` sends `agent` and keeps stdout for the id; on stderr it
     says that NAME is an external agent whose own network traffic and file reads Umbral does
     not record, with the `egress_visibility` it was given, and, when the answer carries
     `agent_unavailable`, why the agent did not start. Every daemon string is escaped first.
   - the TUI's agent panel lists a hosted thread with its agent and `unobserved`, and when that
     thread is selected adds a line saying Umbral does not record the agent's own network
     traffic or file reads. A thread of Umbral's own shows neither. The TUI does not create
   hosted threads, so for it "at creation" is the first listing that shows one.

## Tests

- `TestACPCancelStopsWithin500ms_REQ_ACP_008` (`cmd/umbrald`, a real daemon): a hosted agent
  holding a `sleep 30` terminal is cancelled; the terminal's block ends within 500 ms. Without
  decision 1 it ended 5006 ms later, with the agent's process.
- `TestACancelKillsTheACPTerminalsFirst_REQ_ACP_008`,
  `TestATerminalStartedDuringACancelIsKilled_REQ_ACP_008` (and that the next turn's terminal
  runs) and `TestAnUnresponsiveACPAgentIsKilledAfter5s_REQ_ACP_008` (`internal/acp/adapters/sdk`).
- `TestAnACPTurnHasATrace_REQ_ACP_007` (`internal/agents`): the spans of decision 3, under the
  turn's, a request refused before its row included.

## Review

spec-guardian found nothing to fix first. Its three SHOULD items were applied before the
commit: Tech §5.3e names the panel's hosted-thread row and notice; a mediated request's span
opens on arrival, so a request refused before its row still has one; a terminal registered after
a cancel's sweep is killed, which the first version missed.
- `TestAnACPThreadIsMarkedUnobserved_REQ_ACP_007` (`cmd/umbrald`): the create reply,
  `thread.get` and the stored row.
- `TestAnACPThreadDisclosesItsTrafficAtCreation_REQ_ACP_007` (`cmd/umb`),
  `TestTheThreadListReadsAHostedThread_REQ_ACP_007` and
  `TestAgentPanelSaysAHostedThreadIsUnobserved_REQ_ACP_007` (`internal/tui`): decision 4.
