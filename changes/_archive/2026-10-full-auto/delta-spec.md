# Delta — full-auto inside an active worktree

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-23 by the implementing agent under the Tech Lead's standing F2 instruction — decision 8's list reaches F1 surfaces and awaits the Tech Lead's ratification; folded into API 1.46 (§2, §3, §4 `Thread`, §5.19, §5.22, §5.36, §5.41), Tech 1.61 (DD-006, §5.3) and Data Model 1.25 (§2.5, §5)` |
| **Date** | 2026-10-23 |
| **Task** | T-F2-05 |
| **REQ** | REQ-ORC-004 |

## Decisions the PRD and the task file left open

1. **`full-auto` is a flag, never a stored mode.** `threads.full_auto` (migration 0007) sits
   beside `threads.mode`, which keeps the mode the thread returns to. API §4's `mode` reports
   `full-auto` while the flag is set; the policy engine knows `full-auto` as a mode value
   (`secdomain.ModeFullAuto`) that only a turn's effective mode ever takes.
2. **A turn decides under `full-auto` only while its worktree is active.** `agents.TurnPlace`
   reads the thread's active worktree once, as the turn starts, and returns the cwd, the write
   root and the mode together: `full-auto` when the flag is set and the worktree is active, the
   thread's own mode otherwise. The write root is then always the worktree. `policy.explain`
   reads the same function, so an explanation and a turn agree.
3. **Entering it.** `thread.update` with `mode: "full-auto"` sets the flag and leaves `mode`. The
   runtime asks the engine for the thread's active worktree first — which also runs the missing
   check — and the thread store's `UPDATE` requires the worktree row to be `active` in the same
   write, so a worktree that leaves in between cannot be entered. Without one the answer is
   `VALIDATION_ERROR`. Any other `mode` sets the mode and clears the flag. A mode change during a
   turn is still `CONFLICT`.
4. **`thread.create` refuses `full-auto`** with `VALIDATION_ERROR`: a new thread's worktree is not
   active until the create has made it, and a create that falls back without one (REQ-WT-008)
   must not leave a thread that believes it is in `full-auto`. The client creates the thread with
   its worktree, then updates it.
5. **Leaving it is the database's.** Migration 0008's triggers clear the flag in the same write
   that moves a worktree out of `active` — a merge, a discard, a `close_group`, the missing check
   — or takes the worktree from its thread. No code path can forget it.
6. **The mode step.** In `full-auto`, step 4 of DD-006 decides `WriteFS` as `auto-edit` does
   (inside the write root `allow`, outside it `ask` with `outside_write_root`) and `Exec` by its
   cwd the same way. Steps 1-3 come first, so the destructive floor, a `deny` rule and taint still
   ask or deny; `Network` falls to the mode default, `ask`, unless a user `allow` rule allows it,
   as in `normal`. `Exposed` shows every tool, as in `normal`. A command with no cwd asks: it is
   not known to start inside (an ACP `execute` with none is given the agent's session directory
   by T-F2-08 before it is decided).
7. **An Exec's cwd is the one the turn hands the tool**: the worktree's directory matching the
   thread's cwd. The daemon does not track the shell's current directory (it parses no OSC 7), and
   the thread's PTY keeps its shell between commands, so a line that ran `cd` elsewhere leaves the
   next one starting there. A line can also `cd` out, or write outside through its own paths.
   The policy sees neither: under `full-auto` the worktree is Art. 5's disposable boundary for
   what is allowed unasked, not a sandbox. **Open for the Tech Lead:** whether `full-auto` should
   start every line in the turn's cwd, or track OSC 7, before release 0.1.
8. **An agent's terminal may not land its own work** (the required item of delta
   `2026-10-worktree-methods`). `umb_worktree_change` is a textual barrier a line built by
   expansion escapes, and `full-auto` runs such lines unasked. So:
   - `internal/api` reads the connecting process's pid from the kernel on accept (`SO_PEERCRED`
     on Linux, `LOCAL_PEERPID` on macOS).
   - Methods marked `userOnly` are refused with `PERMISSION_DENIED` to a client inside an agent's
     terminal, whatever its `client_kind`: `worktree.merge`, `worktree.discard`,
     `worktree.close_group`, `approval.respond`, `thread.update`, `skill.install`,
     `skill.set_enabled`, `skill.remove`, `mcp.server.add`, `mcp.server.remove`, `rules.key.add`,
     `rules.key.remove`, `rules.key.rotate`, `rules.rollback`, `rules.reset`, `session.input`, and every
     method that starts a process: `session.create`, `pane.split`, `tab.create`,
     `workspace.create` and `layout.apply`. The list is wider than merge and discard because each
     of the others would let a turn approve, or change what decides, its own actions, or start a
     process outside its terminal — a command, a shell or an environment such as `BASH_ENV` —
     where the check no longer sees it: the same escape. Reads stay open. The check runs before
     `NOT_IMPLEMENTED`: the refusal is about the caller.
   - **This changes F1 behaviour, in every mode.** Deltas `2026-10-skill-catalog`,
     `2026-10-umb-mcp-panel` and `2026-10-worktree-methods` made `umb skill install`, `umb mcp add`
     and `umb worktree merge|discard` destructive patterns the user may approve in a turn; such an
     approved command now fails with `PERMISSION_DENIED`, and the user runs it in their own pane.
     The standing instruction covers F2's own deltas, so **this list awaits the Tech Lead's
     ratification**; narrowing it to the worktree methods is the alternative.
   - "Inside an agent's terminal" is: a thread's live PTY shell (`sessions.Service.AgentShellPIDs`)
     is the process or one of its parents (`proctree.Ancestors`), or leads its POSIX session
     (`getsid`), which catches a process its shell's children left to init. `cmd/umbrald` composes
     the check; `internal/api` only asks it.
   - The verdict taken as the connection starts holds for it: a process that connects and then
     exits, leaving the socket to a child, is still refused.
   - While any agent's terminal is open, a peer that cannot be read — no pid, or one that has
     exited — is taken for an agent's. Only Linux and macOS report a peer's pid.
   - **Residual, for the Tech Lead:** a process that calls `setsid` and then forks away from its
     parents escapes both tests, and so does a command handed to a launcher outside the terminal
     (`systemd-run --user`, `at`, `ssh localhost`). Each takes a deliberate escape in a line the
     model wrote; the destructive patterns still ask before the `umb` commands for these methods,
     a textual barrier only under `full-auto`.
9. **`policy.explain`** accepts `mode: "full-auto"` as a "what if" with the thread's write root,
   and a thread in `full-auto` lends `full-auto` while its worktree is active. The trace's `mode`
   step shows `full-auto` and decides.
10. **ACP is T-F2-08's.** REQ-ORC-004 also governs an ACP thread's permission requests; they reach
    the same engine with the same effective mode, and `TestFullAutoDecidesACPPermissions_REQ_ORC_004`
    belongs to that task.
11. **No `thread.updated` notification** exists for the flag clearing: clients read `mode` from
    `thread.get` and `thread.list`, as for every other field of a thread.

## Tests

- `TestFullAutoAllowsWritesAndCommandsInsideTheWriteRoot_REQ_ORC_004`,
  `TestFullAutoStillAsksForNetworkAndDestructive_REQ_ORC_004` and
  `TestFullAutoStillAsksWhenTainted_REQ_ORC_004`: the engine.
- `TestFullAutoOnlyInsideAWorktree_REQ_ORC_004`: the runtime refuses `full-auto` at creation and
  without an active worktree, keeps the thread's own mode beside the flag, runs a write and a
  command in the worktree unasked, and asks again once the worktree is gone. Three mutations
  (the effective mode, the runtime's check, the place's mode outside a worktree) each reddened it.
- `TestFullAutoFollowsTheWorktreeRow_REQ_ORC_004`: the real store's guarded write and the flag
  clearing on a merge; two mutations reddened it.
- `TestALeftWorktreeClearsFullAuto_REQ_ORC_004`: migration 0008's triggers.
- `TestAThreadInFullAutoSaysSo_REQ_ORC_004` and `TestExplainShowsFullAuto_REQ_ORC_004`: the wire
  and `policy.explain`.
- `TestAnAgentsTerminalCannotLandItsOwnWork_REQ_ORC_004`: the method table's `userOnly` set is
  API §2's list; each is refused from an agent's terminal; reads are served; the verdict taken at connection holds after the process
  reads as outside; another connection from outside is served. `TestAnUnreadablePeerIsTakenForAnAgents_REQ_ORC_004`:
  a peer with no pid.
- `TestAgentShellsAreKnown_REQ_ORC_004`: a thread's PTY shell is an agent's terminal, a user's
  pane is not, and a closed one stops being one.
- `TestAClientInsideAnAgentsTerminalIsKnown_REQ_ORC_004`: the ancestry and the session tests each
  recognise a process, with real processes, and a peer that has exited is taken for an agent's;
  each was mutated away and reddened it.
- The spec-guardian review (FIX FIRST) found a peer that exited after connecting let through,
  `pane.split` with a command and `session.input` as routes around the check (an automated
  security review then found `session.create`, and `env` in a split or a layout, so every method
  that starts a process is refused), the F1 change of
  the wider list unratified, the test covering 8 of the methods, and an Exec with no cwd allowed;
  each is fixed (the list put to the Tech Lead), the code ones with a mutated test.
