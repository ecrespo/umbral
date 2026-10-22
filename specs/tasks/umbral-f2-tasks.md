# Tasks — Umbral F2 (the agentic development environment)

> Source specs: `specs/prd/umbral-f2.md` (0.4) · `specs/api/umbral-daemon-api-v1.md` · `specs/technical/umbral-architecture.md` · `specs/data-model/umbral-schema.md` · `specs/plans/umbral-mvp-plan.md`
> Plan phase covered: F2 · Generated: 2026-10-22 · Status: **RATIFIED** 2026-10-22 with PRD F2 0.4 (PR #32); per-task deltas may be approved by the implementing agent under the Tech Lead's standing F2 instruction

## Conventions for this file

The same conventions as `umbral-f0-tasks.md`:
- tests first;
- every test named after its REQ;
- each task on its own branch with its own PR;
- each task's undecided points go into its own delta, ratified before merge.

Tests against real models or real ACP agents use the `live` build tag and do not run in default CI.

Slice F2a is broken down task by task, because it carries the phase's exit criterion. The other
slices are listed as one line each. Each one is broken down by a delta when it starts, once F2a
has shown what the shared pieces look like.

## Tasks — slice F2a, orchestration

### [x] 2026-10-22 T-F2-00 · Spike: OpenCode over ACP
- **What:** a throwaway client in `docs/spikes/acp/` (its own module) that:
  - launches `opencode acp` isolated from the user's config;
  - runs `initialize`, `session/new` and three prompts against the local Ollama, the last one
    cancelled;
  - logs every frame.

  It ran twice, once with OpenCode's default permissions and once with `ask`.
- **REQ:** REQ-ACP-001 (de-risks REQ-ACP-001…009; no product code)
- **Files:** `docs/spikes/acp/`, `docs/spikes/2026-10-acp-opencode.md`
- **Depends on:** —
- **Done:** report and transcripts committed. Q-F2-05 is answered (`coder/acp-go-sdk`), and Analyze
  F-01 is confirmed with evidence.

### [x] 2026-10-22 T-F2-01 · Migration 0007: worktrees and the new thread, tool-call and block columns
- **What:** no table is rebuilt, because the migration runner applies each file in one transaction
  with foreign keys on (Analyze F-03). New states and flags are new columns whose values the writers
  enforce, as 0005 did (Data Model §2.5):
  - `worktrees` table:
    - an id prefixed `wtr_` (`wt_` is taken by waits);
    - `repo_root`, `repo_id`, `base_commit`, `origin_branch`, `branch`, `path` and `thread_id`;
    - `state` (`active`, `merged`, `discarded`, `missing`), checked in the new table;
    - timestamps;
  - `threads` gains:
    - `full_auto` (0/1; the effective mode is `full-auto` when it is 1);
    - `queued` (0/1);
    - `worktree_id`;
    - `workspace_id` (nullable);
    - `agent` (an `[[acp.agents]]` name, or NULL);
    - `egress_visibility` (`observed`, `unobserved`; default `observed`);
  - an ACP `tool_calls` row's `message_id` is the turn's assistant message, the one being streamed
    when the agent's report or request arrives (REQ-ACP-004), so the column stays `NOT NULL`;
  - `tool_calls` gains `via` (`umbral`, `acp`; default `umbral`) and `decision` (`allow`, `deny`,
    `ask`, `unmediated`; NULL on rows written before 0007), so an ACP call the agent ran without
    asking is told apart from one the policy engine allowed (REQ-ACP-007);
  - `approvals.reason` stays as it is: an ACP approval uses `policy`, `destructive`, `tainted` or
    `outside_write_root` like any other;
  - `blocks` gains `via` (`shell`, `acp`; default `shell`), so `origin` keeps its `CHECK`;
  - Data Model §2 and §6 are updated (recovery: a `queued` thread is recovered `stopped` with
    `queued = 0`).
- **REQ:** REQ-WT-001, REQ-ORC-002, REQ-ORC-003, REQ-ORC-004, REQ-ACP-005, REQ-ACP-007 (storage)
- **Files:** `internal/store/migrations/0007_orchestration.sql`, `internal/store/*_test.go`,
  `internal/store/ids.go`, `internal/store/recover*.go`, `specs/data-model/umbral-schema.md`
- **Depends on:** —
- **Done:**
  - `TestMigration0007KeepsF1Rows_REQ_ORC_004` (a database holding F1 rows keeps them and passes
    `foreign_key_check`) is green;
  - `TestAQueuedTurnIsRecoveredStopped_REQ_ORC_002` is green;
  - `TestMigration0007Constraints_REQ_WT_001` and `TestRetentionKeepsAThreadWithAWorktree_REQ_WT_001`
    are green (delta `2026-10-orchestration-schema`);
  - `python3 tools/sdd_check.py` runs the new DDL;
  - `task ci` is green.

### [ ] T-F2-02 · Worktree engine
- **What:**
  - the `internal/worktrees` module (`domain` / `ports` / `adapters`) over the `git` CLI, with
    `.go-arch-lint.yml` rules and architecture §9's tree updated;
  - **create** under `$XDG_DATA_HOME/umbral/worktrees/<repo_id>/<thread_id>`, on branch
    `umbral/<thread_id>`;
  - **diff** from the base commit, untracked files included, split into hunks with stable ids;
  - **squash merge** into the origin branch, with REQ-WT-004's refusals (`dirty`, `other_branch`,
    `detached`) and its abort on a conflict. Commit hooks and signing run as the user's own
    `git commit` would, with no terminal and `[worktrees] commit_timeout`; a rejected or timed-out
    commit is `commit_failed` and leaves both sides as they were;
  - **discard**, only with confirmation;
  - **missing:** a worktree is marked `missing` at start or on access;
  - **refusals:** a non-git or bare repository, or a git older than 2.34;
  - the commands that read the repository (`status`, `diff`) run with the sanitised environment of
    REQ-CTX-003.
- **REQ:** REQ-WT-001, REQ-WT-003, REQ-WT-004, REQ-WT-005, REQ-WT-006, REQ-WT-008
- **Files:** `internal/worktrees/…`, `internal/config/…` (`[worktrees]`), `.go-arch-lint.yml`,
  `docs/ARCHITECTURE.md` §9, `specs/technical/umbral-architecture.md` §5.1 and §5.2 (a `worktrees`
  row)
- **Depends on:** T-F2-01
- **Done:** these tests are green, and `task ci` is green:
  - `TestWorktreeCreateIsOutsideTheRepo_REQ_WT_001`;
  - `TestWorktreeDiffHasStableHunks_REQ_WT_003`;
  - `TestWorktreeMergeSquashesIntoTheOriginBranch_REQ_WT_004`;
  - `TestWorktreeMergeRefusesADirtyCopy_REQ_WT_004`;
  - `TestWorktreeMergeRefusesAnotherBranch_REQ_WT_004`;
  - `TestWorktreeMergeRefusesADetachedHead_REQ_WT_004`;
  - `TestWorktreeMergeConflictLeavesBothSides_REQ_WT_004`;
  - `TestARejectedCommitLeavesBothSides_REQ_WT_004` (a failing `pre-commit` hook, and a signing
    step that would prompt);
  - `TestWorktreeDiscardNeedsConfirm_REQ_WT_005`;
  - `TestAMissingWorktreeIsMarked_REQ_WT_006`;
  - `TestWorktreeRefusesANonGitDirectory_REQ_WT_008`;
  - `TestWorktreeRefusesABareRepo_REQ_WT_008`;
  - `TestWorktreeRefusesAnOldGit_REQ_WT_008`.

### [ ] T-F2-03 · `worktree.*` methods, threads in their worktree, `umb worktree`
- **What:**
  - methods `worktree.create`, `list`, `get`, `diff`, `merge`, `discard` and `close_group`;
  - `thread.create` takes `worktree: true` and `workspace`;
  - a thread with a worktree uses it as cwd and write root;
  - `CONFLICT`'s `data.reason` and `data.paths` (API §8);
  - `umb worktree list|diff|merge|discard`;
  - the `worktrees` capability, served (API §9).
- **REQ:** REQ-WT-002, REQ-WT-007 (and the wire of WT-001, 003, 004 and 005)
- **Files:**
  - `internal/api/worktrees.go`
  - `internal/agents/…`
  - `cmd/umb/worktree.go`
  - `cmd/umbrald/…`
  - `specs/api/umbral-daemon-api-v1.md` (§2, §4, §5, §8, §9)
  - `specs/prd/umbral-f2.md` (traceability)
- **Depends on:** T-F2-02
- **Done:**
  - `TestAThreadWritesOnlyInItsWorktree_REQ_WT_002`,
    `TestCloseGroupNeverDropsAnUnmergedChange_REQ_WT_007` and
    `TestCloseGroupReportsAMissingWorktree_REQ_WT_007` are green;
  - `task schema` is green.

### [ ] T-F2-04 · Parallel turns and per-provider queues
- **What:**
  - turns of different threads run concurrently;
  - `[agents] max_concurrent_turns` (`config.toml`, default 3) holds a turn over the limit as
    `queued`, with REQ-ORC-002's `CONFLICT`, cancel and recovery rules;
  - a `max_parallel` key per `[[providers]]` entry in `models.toml` (default 1 for local) queues
    model calls, and the queue time is kept out of `first_token_timeout`;
  - the race detector stays clean under a three-thread load.
- **REQ:** REQ-ORC-001, REQ-ORC-002, REQ-ORC-005
- **Files:**
  - `internal/agents/…`
  - `internal/llmgw/router/…`
  - `internal/config/…`
  - `specs/technical/umbral-architecture.md` §5.1
  - `specs/api/umbral-daemon-api-v1.md` (§5.23 `thread.get`'s `queued`, §5.20 `thread.send`'s
    `CONFLICT`, §7)
- **Depends on:** T-F2-01
- **Done:** these tests are green under `-race`:
  - `TestThreeThreadsRunConcurrently_REQ_ORC_001`;
  - `TestAFourthTurnIsQueued_REQ_ORC_002`;
  - `TestASecondSendToAQueuedThreadConflicts_REQ_ORC_002`;
  - `TestCancellingAQueuedTurnDropsIt_REQ_ORC_002`;
  - `TestLocalCallsQueuePerProvider_REQ_ORC_005`;
  - `TestQueueTimeIsNotFirstTokenTime_REQ_ORC_005`.

### [ ] T-F2-05 · `full-auto` inside an active worktree
- **What:**
  - the policy engine gains `full-auto`: `WriteFS` inside the worktree and `Exec` with its cwd inside
    it are allowed; `Network`, the destructive list and taint still ask;
  - a thread without an active worktree is refused `full-auto` with `VALIDATION_ERROR`;
  - a worktree that leaves `active` clears `full_auto`, leaving the thread's own mode;
  - an ACP thread's permission requests are decided under it too (REQ-ACP-003);
  - `policy.explain` shows the step.
- **REQ:** REQ-ORC-004
- **Files:**
  - `internal/security/domain/policy*.go`
  - `internal/agents/…`
  - `specs/technical/umbral-architecture.md` §5.3 (a `full-auto` column)
  - `specs/api/umbral-daemon-api-v1.md` §5.36
- **Depends on:** T-F2-03
- **Done:** these tests are green:
  - `TestFullAutoOnlyInsideAWorktree_REQ_ORC_004`;
  - `TestFullAutoStillAsksForNetworkAndDestructive_REQ_ORC_004`;
  - `TestFullAutoStillAsksWhenTainted_REQ_ORC_004`;
  - `TestALeftWorktreeClearsFullAuto_REQ_ORC_004`.

### [ ] T-F2-06 · The agent panel shows every thread
- **What:**
  - the TUI agent panel lists the focused workspace's threads, each with its attention state,
    worktree, `queued` mark and pending approvals;
  - the user can switch between them;
  - approvals of any thread are answered with control chords, as in T-F1-20.
- **REQ:** REQ-ORC-003
- **Files:** `internal/tui/…`, `specs/technical/umbral-architecture.md` §5.3e
- **Depends on:** T-F2-03, T-F2-04
- **Done:** `TestAgentPanelListsEveryThread_REQ_ORC_003` (harness) is green.

### [ ] T-F2-07 · ACP client: manifest, launch, session, prompt
- **What:**
  - the `internal/acp` module on `github.com/coder/acp-go-sdk`, with `.go-arch-lint.yml` rules;
  - **config:** `[[acp.agents]]` and `[acp]` in `config.toml`;
  - **manifests:** the built-in OpenCode manifest, which generates the config that sets every
    permission to `ask` and declares no MCP servers or plugins; an entry without a manifest is
    refused, and so is a worktree holding a repository-level agent config (`project_config`)
    (REQ-ACP-009);
  - **launch:** the agent starts in the worktree (or the thread's directory without one), with its
    own `HOME` and XDG directories under the runtime directory and an allowlisted environment;
  - **session:** `initialize`, then `session/new`;
  - **prompt:** `thread.send` sends `session/prompt`; message and thought chunks and tool calls map
    onto `thread.delta` and the persisted messages;
  - **errors:** `agent_error` on exit, invalid ACP, or `silence_timeout` with no pending approval
    and no outstanding agent request;
  - `stop_reason = agent_error` is added to API §6 and §7;
  - DD-012 is revised: a hosted ACP thread is the level-1 authority of the pane that owns it.
- **REQ:** REQ-ACP-001, REQ-ACP-002, REQ-ACP-006, REQ-ACP-009
- **Files:**
  - `internal/acp/…`
  - `internal/config/…`
  - `internal/api/threads.go`
  - `cmd/umbrald/…`
  - `go.mod`
  - `.go-arch-lint.yml`
  - `specs/api/umbral-daemon-api-v1.md` (§5.19, §6, §7)
  - `specs/technical/umbral-architecture.md` (DD-012, §5.1, §5.2)
- **Depends on:** T-F2-03
- **Done:** these tests are green with a fake ACP agent in Go, and `task schema` is green:
  - `TestAnACPAgentIsLaunchedInItsWorktree_REQ_ACP_001`;
  - `TestAnACPThreadWithoutAWorktreeRunsInItsDirectory_REQ_ACP_001`;
  - `TestAnACPThreadStreamsLikeAnyOther_REQ_ACP_002`;
  - `TestAnACPThreadIsItsPanesStateAuthority_REQ_ACP_002`;
  - `TestAnACPAgentCrashEndsTheTurn_REQ_ACP_006`;
  - `TestASilentACPAgentEndsTheTurn_REQ_ACP_006`;
  - `TestAnAgentWaitingOnUmbralIsNotSilent_REQ_ACP_006`;
  - `TestAPendingApprovalPausesTheSilenceTimer_REQ_ACP_006`;
  - `TestAnAgentWithoutAManifestIsRefused_REQ_ACP_009`;
  - `TestTheACPEnvironmentHoldsNoSecrets_REQ_ACP_009`;
  - `TestTheOpenCodeManifestAsksForEveryPermission_REQ_ACP_009`;
  - `TestARepositoryAgentConfigIsRefused_REQ_ACP_009`;
  - `TestTheUsersGlobalAgentConfigIsNotRead_REQ_ACP_009`.

### [ ] T-F2-08 · ACP mediation: permissions, files, terminals
- **What:**
  - **permissions:** `session/request_permission` is decided by kind (`edit`/`delete`/`move` →
    `WriteFS` on `locations`, `execute` → `Exec`, `fetch` → `Network`, `read`/`search`/`think` →
    `ReadOnly`, anything else → `Exec`), through the policy engine and the approval queue. Umbral
    answers `allow_once` or `reject_once`, never `allow_always`;
  - **files:** `fs/read_text_file` and `fs/write_text_file` are performed by Umbral; a read outside
    the write root asks; a write an allowed permission already covered is not asked again;
  - each `fs/*` and `terminal/create` request Umbral decides gets a `tool_calls` row of its own, so
    its approval points at it;
  - **terminals:** `terminal/create` makes a thread-owned session per terminal, holding a block with
    `via = acp`, at most `max_terminals` per thread;
  - every reported tool call is recorded in `tool_calls` with `via = acp`, its mapped risk and its
    `decision` (`unmediated` when the agent did not ask).
- **REQ:** REQ-ACP-003, REQ-ACP-004, REQ-ACP-005, REQ-ACP-007 (the `tool_calls` rows), REQ-ORC-004 (ACP requests under `full-auto`)
- **Files:** `internal/acp/…`, `internal/sessions/…`, `internal/agents/…`, `internal/store/…`,
  `specs/technical/umbral-architecture.md` §5.3b
- **Depends on:** T-F2-05, T-F2-07
- **Done:** these tests are green:
  - `TestACPPermissionEntersTheApprovalQueue_REQ_ACP_003`;
  - `TestACPNeverSelectsAllowAlways_REQ_ACP_003`;
  - `TestACPKindsMapToRisks_REQ_ACP_003` (table: every kind, `fetch` → `Network`, an unknown kind →
    `Exec`);
  - `TestACPWriteOutsideTheWorktreeAsks_REQ_ACP_004`;
  - `TestACPReadOutsideTheWorktreeAsks_REQ_ACP_004`;
  - `TestACPReadInsideTheWorktreeIsReadOnly_REQ_ACP_004`;
  - `TestACPWriteCoveredByAPermissionIsNotAskedTwice_REQ_ACP_004`;
  - `TestACPToolCallsAreRecordedWithTheirDecision_REQ_ACP_007`;
  - `TestFullAutoDecidesACPPermissions_REQ_ORC_004` (an `edit` inside the worktree and an `execute`
    without a cwd are allowed, a `fetch` still asks);
  - `TestACPTerminalIsAnAuditedBlock_REQ_ACP_005`;
  - `TestACPTerminalsAreCapped_REQ_ACP_005`.

### [ ] T-F2-09 · ACP cancel, traces and `egress_visibility`
- **What:**
  - **cancel:** `thread.cancel` sends `session/cancel`, kills the terminals within 500 ms, and the
    process within 5 s;
  - **traces:** one OTel trace per ACP turn, with spans per agent tool call and per mediated request;
  - **disclosure:** `egress_visibility = unobserved`, shown at creation by the TUI and by
    `umb thread create`.
- **REQ:** REQ-ACP-007, REQ-ACP-008
- **Files:**
  - `internal/acp/…`
  - `internal/obs/…`
  - `internal/tui/…`
  - `cmd/umb/thread.go`
  - `specs/api/umbral-daemon-api-v1.md` (§4 `Thread`)
- **Depends on:** T-F2-08
- **Done:** these tests are green:
  - `TestACPCancelStopsWithin500ms_REQ_ACP_008`;
  - `TestAnUnresponsiveACPAgentIsKilledAfter5s_REQ_ACP_008`;
  - `TestAnACPTurnHasATrace_REQ_ACP_007`;
  - `TestAnACPThreadIsMarkedUnobserved_REQ_ACP_007`;
  - `TestAnACPThreadDisclosesItsTrafficAtCreation_REQ_ACP_007` (the `thread.create` reply and
    `umb thread create`'s output).

### [ ] T-F2-10 · Third-party agent detection
- **What:**
  - built-in manifests for OpenCode, Claude Code, Gemini CLI, Codex and Goose;
  - `$XDG_CONFIG_HOME/umbral/agents/*.toml` overrides them by name, and an invalid file is skipped
    whole;
  - matching runs over the pane's process tree and sets `Pane.detected_agent`, never state;
  - `agent.explain` and `umb agent explain <pane>`.
- **REQ:** REQ-XAG-001, REQ-XAG-002, REQ-XAG-003
- **Files:**
  - `internal/integrations/…`
  - `internal/api/…`
  - `cmd/umb/agent.go`
  - `specs/api/umbral-daemon-api-v1.md` (§2, §4 `Pane`, §5)
  - `specs/technical/umbral-architecture.md` (DD-012)
- **Depends on:** —
- **Done:**
  - these tests are green:
    - `TestAPaneRunningOpenCodeIsLabelled_REQ_XAG_001`;
    - `TestDetectionNeverSetsState_REQ_XAG_001`;
    - `TestALocalManifestOverridesByName_REQ_XAG_002`;
    - `TestAnInvalidManifestIsSkippedAndReported_REQ_XAG_002`;
    - `TestAgentExplainNamesTheRule_REQ_XAG_003`;
  - `task schema` is green.

### [ ] T-F2-11 · Router: capabilities, circuit breakers, policy, `model.route`
- **What:**
  - a candidate lacking a capability the request needs is filtered out: tools, vision, or a large
    enough window;
  - one breaker per provider, with `breaker_failures` and `breaker_cooldown`, closed again by a
    probe;
  - `[router] policy` orders the candidates;
  - `model.route` answers with the ordered candidates and the reasons.
- **REQ:** REQ-RTR-001, REQ-RTR-002, REQ-RTR-003
- **Files:**
  - `internal/llmgw/router/…`
  - `internal/config/…` (`breaker_failures`, `breaker_cooldown`, `[router] policy`)
  - `internal/api/models.go`
  - `specs/api/umbral-daemon-api-v1.md` (§5)
  - `specs/technical/umbral-architecture.md` (§5.1, the router)
- **Depends on:** —
- **Done:**
  - these tests are green:
    - `TestACandidateWithoutToolsIsDropped_REQ_RTR_001`;
    - `TestABreakerOpensAndProbes_REQ_RTR_002`;
    - `TestRouterPoliciesOrderTheCandidates_REQ_RTR_003` (table: `local-first`, `cost`, `quality`);
    - `TestModelRouteExplainsTheOrder_REQ_RTR_003`;
  - `task schema` is green.

### [ ] T-F2-12 · US-F2-001 live: three agents, one repository, and the F2a budgets
- **What:**
  - the reference scenario of PRD F2 §9: two local `gpt-oss:20b` threads and one OpenCode thread
    over ACP, all on the local Ollama, each in its own worktree, running concurrently;
  - measured against PRD F2 §4, and against NFR-F2-01, NFR-F2-02 and NFR-F2-03;
  - reported in `docs/reports/`.
- **REQ:** REQ-ORC-001, REQ-ACP-002, REQ-ACP-007 (the F2 exit criterion, measured live)
- **Files:** `cmd/umbrald/usf2_live_test.go` (tag `live`), `e2e/usf2/…`, `docs/reports/`
- **Depends on:** T-F2-03 … T-F2-09
- **Done:** the report shows:
  - 3/3 threads finished;
  - no change outside the worktrees;
  - merges clean, or conflicts reported;
  - a trace for every turn;
  - every ACP side effect decided by the policy engine;
  - the three budgets measured.

## Tasks — later slices (broken down when each starts)

Each of these is detailed — What, Files, Done, test names — by a delta when its slice starts.

### [ ] T-F2-20 · `terminal_interact`, the input ownership lock, take-over
- **Slice:** F2b
- **REQ:** REQ-FTU-001, REQ-FTU-002, REQ-FTU-003
- **Depends on:** slice F2a

### [ ] T-F2-21 · The embedded yzma provider for the `fast` class
- **Slice:** F2b
- **REQ:** REQ-LLM-009
- **Depends on:** slice F2a

### [ ] T-F2-22 · Active AI on failed blocks, local only
- **Slice:** F2b
- **REQ:** REQ-AAI-001, REQ-AAI-002, REQ-AAI-003
- **Depends on:** slice F2a

### [ ] T-F2-23 · US-F2-002 live: interactive programs
- **Slice:** F2b
- **REQ:** REQ-FTU-001
- **Depends on:** slice F2a

### [ ] T-F2-30 · Wails v3 client over the protocol
- **Slice:** F2c
- **REQ:** REQ-DSK-001
- **Depends on:** slice F2a

### [ ] T-F2-31 · Per-hunk diff approval and the worktree's file tree
- **Slice:** F2c
- **REQ:** REQ-DSK-002, REQ-DSK-003
- **Depends on:** slice F2a

### [ ] T-PKG-04 · Wails icons, app id and Taskfile override
- **Slice:** F2c
- **REQ:** REQ-PKG-004, REQ-PKG-005
- **Depends on:** slice F2a

### [ ] T-PKG-05 · macOS `.icon` and `Assets.car`
- **Slice:** F2c
- **REQ:** REQ-PKG-007
- **Depends on:** slice F2a

### [ ] T-F2-40 · Live PTY handoff between daemon versions
- **Slice:** F2d
- **REQ:** REQ-HND-001, REQ-HND-002
- **Depends on:** slice F2a

### [ ] T-F2-41 · Remote daemons over SSH and federation
- **Slice:** F2d
- **REQ:** REQ-RMT-001, REQ-RMT-002, REQ-RMT-003
- **Depends on:** slice F2a

### [ ] T-F2-50 · Windows: ConPTY, named pipe, Credential Manager, PowerShell
- **Slice:** F2e
- **REQ:** REQ-WIN-001, REQ-WIN-002, REQ-WIN-003
- **Depends on:** slice F2a

### [ ] T-F2-60 · Declarative plugins installed by digest
- **Slice:** F2f
- **REQ:** REQ-PLG-001, REQ-PLG-002
- **Depends on:** slice F2a

### [ ] T-F2-61 · Pane graphics for clients with `graphics`
- **Slice:** F2f
- **REQ:** REQ-PLG-003
- **Depends on:** slice F2a

## Traceability matrix (F2)

| REQ | Tasks | Verification |
|---|---|---|
| REQ-WT-001 | T-F2-01, T-F2-02, T-F2-03 | `TestWorktreeCreateIsOutsideTheRepo_REQ_WT_001`, `TestMigration0007Constraints_REQ_WT_001`, `TestRetentionKeepsAThreadWithAWorktree_REQ_WT_001` |
| REQ-WT-002 | T-F2-03 | `TestAThreadWritesOnlyInItsWorktree_REQ_WT_002` |
| REQ-WT-003 | T-F2-02 | `TestWorktreeDiffHasStableHunks_REQ_WT_003` |
| REQ-WT-004 | T-F2-02 | `TestWorktreeMergeSquashesIntoTheOriginBranch_REQ_WT_004`, `TestWorktreeMergeRefusesADirtyCopy_REQ_WT_004`, `TestWorktreeMergeRefusesAnotherBranch_REQ_WT_004`, `TestWorktreeMergeRefusesADetachedHead_REQ_WT_004`, `TestWorktreeMergeConflictLeavesBothSides_REQ_WT_004`, `TestARejectedCommitLeavesBothSides_REQ_WT_004` |
| REQ-WT-005 | T-F2-02 | `TestWorktreeDiscardNeedsConfirm_REQ_WT_005` |
| REQ-WT-006 | T-F2-02 | `TestAMissingWorktreeIsMarked_REQ_WT_006` |
| REQ-WT-007 | T-F2-03 | `TestCloseGroupNeverDropsAnUnmergedChange_REQ_WT_007`, `TestCloseGroupReportsAMissingWorktree_REQ_WT_007` |
| REQ-WT-008 | T-F2-02 | `TestWorktreeRefusesANonGitDirectory_REQ_WT_008`, `TestWorktreeRefusesABareRepo_REQ_WT_008`, `TestWorktreeRefusesAnOldGit_REQ_WT_008` |
| REQ-ORC-001 | T-F2-04 | `TestThreeThreadsRunConcurrently_REQ_ORC_001` |
| REQ-ORC-002 | T-F2-01, T-F2-04 | `TestAFourthTurnIsQueued_REQ_ORC_002`, `TestASecondSendToAQueuedThreadConflicts_REQ_ORC_002`, `TestCancellingAQueuedTurnDropsIt_REQ_ORC_002`, `TestAQueuedTurnIsRecoveredStopped_REQ_ORC_002` |
| REQ-ORC-003 | T-F2-01, T-F2-06 | `TestAgentPanelListsEveryThread_REQ_ORC_003` |
| REQ-ORC-004 | T-F2-01, T-F2-05, T-F2-08 | `TestMigration0007KeepsF1Rows_REQ_ORC_004`, `TestFullAutoOnlyInsideAWorktree_REQ_ORC_004`, `TestFullAutoStillAsksForNetworkAndDestructive_REQ_ORC_004`, `TestFullAutoStillAsksWhenTainted_REQ_ORC_004`, `TestALeftWorktreeClearsFullAuto_REQ_ORC_004`, `TestFullAutoDecidesACPPermissions_REQ_ORC_004` |
| REQ-ORC-005 | T-F2-04 | `TestLocalCallsQueuePerProvider_REQ_ORC_005`, `TestQueueTimeIsNotFirstTokenTime_REQ_ORC_005` |
| REQ-ACP-001 | T-F2-07 | `TestAnACPAgentIsLaunchedInItsWorktree_REQ_ACP_001`, `TestAnACPThreadWithoutAWorktreeRunsInItsDirectory_REQ_ACP_001` |
| REQ-ACP-002 | T-F2-07 | `TestAnACPThreadStreamsLikeAnyOther_REQ_ACP_002`, `TestAnACPThreadIsItsPanesStateAuthority_REQ_ACP_002` |
| REQ-ACP-003 | T-F2-08 | `TestACPPermissionEntersTheApprovalQueue_REQ_ACP_003`, `TestACPNeverSelectsAllowAlways_REQ_ACP_003`, `TestACPKindsMapToRisks_REQ_ACP_003` |
| REQ-ACP-004 | T-F2-08 | `TestACPWriteOutsideTheWorktreeAsks_REQ_ACP_004`, `TestACPReadOutsideTheWorktreeAsks_REQ_ACP_004`, `TestACPReadInsideTheWorktreeIsReadOnly_REQ_ACP_004`, `TestACPWriteCoveredByAPermissionIsNotAskedTwice_REQ_ACP_004` |
| REQ-ACP-005 | T-F2-01, T-F2-08 | `TestACPTerminalIsAnAuditedBlock_REQ_ACP_005`, `TestACPTerminalsAreCapped_REQ_ACP_005` |
| REQ-ACP-006 | T-F2-07 | `TestAnACPAgentCrashEndsTheTurn_REQ_ACP_006`, `TestASilentACPAgentEndsTheTurn_REQ_ACP_006`, `TestAnAgentWaitingOnUmbralIsNotSilent_REQ_ACP_006`, `TestAPendingApprovalPausesTheSilenceTimer_REQ_ACP_006` |
| REQ-ACP-007 | T-F2-01, T-F2-08, T-F2-09 | `TestAnACPTurnHasATrace_REQ_ACP_007`, `TestAnACPThreadIsMarkedUnobserved_REQ_ACP_007`, `TestACPToolCallsAreRecordedWithTheirDecision_REQ_ACP_007`, `TestAnACPThreadDisclosesItsTrafficAtCreation_REQ_ACP_007` |
| REQ-ACP-008 | T-F2-09 | `TestACPCancelStopsWithin500ms_REQ_ACP_008`, `TestAnUnresponsiveACPAgentIsKilledAfter5s_REQ_ACP_008` |
| REQ-ACP-009 | T-F2-07 | `TestAnAgentWithoutAManifestIsRefused_REQ_ACP_009`, `TestTheACPEnvironmentHoldsNoSecrets_REQ_ACP_009`, `TestTheOpenCodeManifestAsksForEveryPermission_REQ_ACP_009`, `TestARepositoryAgentConfigIsRefused_REQ_ACP_009`, `TestTheUsersGlobalAgentConfigIsNotRead_REQ_ACP_009` |
| REQ-XAG-001 | T-F2-10 | `TestAPaneRunningOpenCodeIsLabelled_REQ_XAG_001`, `TestDetectionNeverSetsState_REQ_XAG_001` |
| REQ-XAG-002 | T-F2-10 | `TestALocalManifestOverridesByName_REQ_XAG_002`, `TestAnInvalidManifestIsSkippedAndReported_REQ_XAG_002` |
| REQ-XAG-003 | T-F2-10 | `TestAgentExplainNamesTheRule_REQ_XAG_003` |
| REQ-RTR-001 | T-F2-11 | `TestACandidateWithoutToolsIsDropped_REQ_RTR_001` |
| REQ-RTR-002 | T-F2-11 | `TestABreakerOpensAndProbes_REQ_RTR_002` |
| REQ-RTR-003 | T-F2-11 | `TestRouterPoliciesOrderTheCandidates_REQ_RTR_003`, `TestModelRouteExplainsTheOrder_REQ_RTR_003` |
| REQ-FTU-001 | T-F2-20, T-F2-23 | deferred: named when the slice starts |
| REQ-FTU-002 | T-F2-20 | deferred: named when the slice starts |
| REQ-FTU-003 | T-F2-20 | deferred: named when the slice starts |
| REQ-LLM-009 | T-F2-21 | deferred: named when the slice starts |
| REQ-AAI-001 | T-F2-22 | deferred: named when the slice starts |
| REQ-AAI-002 | T-F2-22 | deferred: named when the slice starts |
| REQ-AAI-003 | T-F2-22 | deferred: named when the slice starts |
| REQ-DSK-001 | T-F2-30 | deferred: named when the slice starts |
| REQ-DSK-002 | T-F2-31 | deferred: named when the slice starts |
| REQ-DSK-003 | T-F2-31 | deferred: named when the slice starts |
| REQ-PKG-004 | T-PKG-04 | a test of the Wails window options (`app_id`, `WM_CLASS`) |
| REQ-PKG-005 | T-PKG-04 | a test that parses the embedded `.ico` and checks its sizes |
| REQ-PKG-007 | T-PKG-05 | a test that the built bundle carries `Assets.car` and `CFBundleIconName = appicon`; the Icon Composer compile is manual (PRD F2 §11) |
| REQ-PKG-008 | — | SHOULD, deferred |
| REQ-HND-001 | T-F2-40 | deferred: named when the slice starts |
| REQ-HND-002 | T-F2-40 | deferred: named when the slice starts |
| REQ-RMT-001 | T-F2-41 | deferred: named when the slice starts |
| REQ-RMT-002 | T-F2-41 | deferred: named when the slice starts |
| REQ-RMT-003 | T-F2-41 | deferred: named when the slice starts |
| REQ-WIN-001 | T-F2-50 | deferred: named when the slice starts |
| REQ-WIN-002 | T-F2-50 | deferred: named when the slice starts |
| REQ-WIN-003 | T-F2-50 | deferred: named when the slice starts |
| REQ-PLG-001 | T-F2-60 | deferred: named when the slice starts |
| REQ-PLG-002 | T-F2-60 | deferred: named when the slice starts |
| REQ-PLG-003 | T-F2-61 | deferred: named when the slice starts |

## Execution log

| Date | Tasks | Result | Notes |
|---|---|---|---|
| 2026-10-22 | — | proposed | PRD F2 0.2 and this file drafted. The Tech Lead decided Q-F2-01…04 the same day. |
| 2026-10-22 | T-F2-00 | done | OpenCode 1.18.33 over ACP with `coder/acp-go-sdk` v0.13.5, against `gpt-oss:20b` (`docs/spikes/2026-10-acp-opencode.md`). With default permissions OpenCode asks the client nothing: it writes files and runs commands itself. With `ask` every write and command arrives as `session/request_permission`, with path, diff or command, and writes come through `fs/write_text_file`; commands still run in its own shell. Reads are never mediated. Cancel ends the turn `cancelled` in about 0.4 s. That evidence and the Analyze pass (22 findings, FIX FIRST) reshaped PRD F2 to 0.3. |
| 2026-10-22 | — | revised | spec-guardian verify pass on PRD F2 0.3: FIX FIRST (N-01…N-10). Applied in PRD F2 0.4 and here: `tool_calls.decision`, the merge's `commit_failed`, the ACP manifest's isolation and project-config refusal, and a named test for every clause the pass found untested. |
| 2026-10-22 | — | ratified | The Tech Lead ratified PRD F2 0.4, this file and constitution 1.3 (PR #32), and allowed per-task F2 deltas to be approved by the implementing agent. |
| 2026-10-22 | T-F2-01 | done | Migration `0007_orchestration` adds `worktrees` and the F2 columns by `ALTER`, each with its `CHECK`: SQLite accepts one in `ADD COLUMN`, which corrects Data Model §2.5. Recovery stops a queued turn. Mutations removing a `CHECK` or adding a cascade to `worktrees.thread_id` turn the tests red. Delta `2026-10-orchestration-schema`, approved under the standing instruction. |
| — | — | — | — |
