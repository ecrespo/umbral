# Delta — a hosted agent is refused what an agent's terminal is refused

| Field | Value |
|---|---|
| **Status** | `PROPOSED 2026-10-24`, awaiting the Tech Lead |
| **Date** | 2026-10-24 |
| **Task** | T-HRD-05 |
| **REQ** | REQ-ORC-004, REQ-ACP-009 (no change to their text) |
| **Raised by** | Q12 of delta `2026-10-hrd02-open-questions`, ratified by the Tech Lead on 2026-10-24 |

## Evidence

Delta `2026-10-full-auto` decision 8 refuses the `userOnly` methods (API §2 "An agent's
terminal") to a client whose process, a parent of it, or its session leader is one of the agent
runtime's thread shells. A hosted ACP agent is not one of them:
- it is started by `internal/acp` with `exec.Command`, in its own process group but in the
  daemon's session;
- its pid is in no list `agentPeer` reads;
- its isolated `HOME` is `<runtime dir>/acp/<thread>/home`, three directories under the socket
  and its `token`.

`TestAHostedAgentIsRefusedTheUsersMethods_REQ_ORC_004` measured it before the fix. A fake hosted
agent derived the socket from its `HOME`, read the token, said `system.hello` as `cli` and called
`skill.install`. The call reached the method and was answered `VALIDATION_ERROR`, for its empty
params, instead of being refused for its caller. With real params it would have installed a
skill; the same holds for `mcp.server.add`, `worktree.merge` and `approval.respond`. The commands
a hosted agent runs through `terminal/create` were already covered, because they run in sessions
its thread owns.

## Decisions

1. **The ACP host lists its agents' pids, and `cmd/umbrald` adds them to the agents
   `agentPeer` checks.** A pid is listed from the moment the process starts, before its ACP
   handshake, because an agent can dial the socket before it speaks ACP.
2. **A hosted agent leads its own session (`Setsid`) instead of only its own process group.**
   `setsid` also makes a new group with the same id, so the kill on cancel and close, `-pid`, is
   unchanged. A process the agent starts and leaves to init stays in the agent's session, so
   `agentPeer`'s session check still catches it.
3. **When a hosted agent exits, everything left in its session is killed, and only then is
   its pid forgotten.** On Linux this covers every process whose session id is the agent's,
   found in `/proc`, so a child that moved to a process group of its own is killed too. The
   automated security review of the first push found that case: killing the group alone
   missed it. Otherwise a helper it left behind would keep running in a session nobody counts
   any more. The Tech Design already said that what the agent starts ends with it; until now
   that held only on kill.
   - **No pid reuse.** On Linux the exit is seen with `waitid(WNOWAIT)`, so the group is killed
     and the pid forgotten while the agent is still an unreaped zombie. Neither step can then
     reach a process that reused the pid, and a later close sends no signal once the agent is
     being reaped.
   - **macOS** has no `WNOWAIT`. There the pid stays listed until the thread's agent is
     closed, so what the agent left behind is refused until then; it is not killed, because
     its session's id may already be someone else's. The same review found that forgetting the
     pid at exit there left such a child unrefused.
   - **The agent's stderr is `/dev/null` instead of a discarding pipe.** A pipe held by such a
     child kept `Wait` from returning, so the exit was not seen and the pid stayed listed. That
     part was found by spec-guardian and predates this delta: a turn running at that moment
     still ended, because stdout closed.
   - **The second push's security review** found three more gaps, and each is closed.
     - **Reused pid unlisted.** On macOS an older agent's release could unlist a newer agent
       that had reused its pid. The listing now counts per pid.
     - **Wrong process signalled.** A pid read from `/proc` could be reused before its kill.
       Each process is now signalled through a pidfd, after its session is read again.
     - **Survivors forgotten.** A bounded scan could forget the pid while processes survived.
       Zombies are now ignored, the scan runs until a 2 s deadline, and a session not seen
       empty keeps the pid listed (fail closed).
   - **No controlling terminal.** With `Setsid` the agent has none, where before it shared the
     daemon's when the daemon ran in a terminal.
4. **The residual of `2026-10-full-auto` stays.** A process that calls `setsid` again and forks
   away from its parents, or a launcher (`systemd-run --user`, `at`, `ssh localhost`), escapes
   the check, as the Tech Lead accepted with T-HRD-02's Q5 and Q7. T-HRD-06 covers it on the
   pattern side.

## Spec changes

- **API 1.52 §2** "An agent's terminal": a hosted ACP agent's process counts, from its launch
  and by its session; its `HOME` lies beside the socket, so the refusal is what stops it. The
  wire does not change.
- **Tech 1.68:**
  - §5.3: the check composes the ACP host's `AgentPIDs`;
  - §5.3f: the process leads its own session with no controlling terminal, its group is killed
    when it exits (on Linux, before it is reaped), and its stderr is `/dev/null`.
- **API §2:** a peer that cannot be read is refused while an agent's terminal is open *or a
  hosted agent runs*.

## Verification

- **`TestAHostedAgentIsRefusedTheUsersMethods_REQ_ORC_004`** (`cmd/umbrald`): a fake hosted agent
  dials the real daemon and gets `PERMISSION_DENIED` for `skill.install`, while the user's client
  gets past the check. The test was red before the fix, and red again with the wiring removed.
- **`TestAHostedAgentIsKnownByItsSession_REQ_ORC_004`** (`internal/acp/adapters/sdk`): the
  running agent is listed and leads its own session, and after `Close` it is no longer listed.
  It is red with `Setpgid` back in place of `Setsid`, and red without the pid being recorded.
- **`TestAHostedAgentsExitEndsWhatItLeft_REQ_ORC_004`** (`internal/acp/adapters/sdk`): an agent
  ends its turn, leaves a `sleep` that holds its stderr, and exits between turns. The case runs
  twice: once with the child in the agent's group, and once with the child in a group of its
  own. Within 5 s the child is dead and the pid is no longer listed (Linux). It was red before
  decision 3 was rewritten (child alive, pid listed), and red in the second case with the kill
  of the group alone. It is red again with the `/proc` scan removed.
- **`TestAReusedPidStaysListedForItsNewerAgent_REQ_ORC_004`:** the newer agent stays listed
  after the older one's release. It is red with a plain delete in place of the count.
