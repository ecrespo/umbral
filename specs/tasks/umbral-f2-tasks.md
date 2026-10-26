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

### [x] 2026-10-22 T-F2-02 · Worktree engine
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
  - `TestWorktreeRefusesAnOldGit_REQ_WT_008`;
  - also `TestParseUnifiedDiffSplitsFilesAndHunks_REQ_WT_003`, `TestHunkIDsAreStable_REQ_WT_003`,
    `TestRepoIDIsSixteenHexOfTheCommonDir_REQ_WT_001`, `TestGitVersionFloor_REQ_WT_008`,
    `TestRefusalsAreValidationOrConflict_REQ_WT_004`, `TestWorktreeRowsRoundTrip_REQ_WT_001` and
    `TestWorktreeCommitTimeoutIsConfigurable_REQ_WT_004` (delta `2026-10-worktree-engine`).

### [x] 2026-10-22 T-F2-03 · `worktree.*` methods, threads in their worktree, `umb worktree`
- **What:**
  - methods `worktree.create`, `list`, `get`, `diff`, `merge`, `discard` and `close_group`;
  - `thread.create` takes `worktree: true` and `workspace`;
  - a thread with a worktree uses it as cwd and write root;
  - `CONFLICT`'s `data.reason` and `data.paths` (API §8), with the five reasons of delta
    `2026-10-worktree-engine` decision 5;
  - `revision` on `worktree.diff` and `worktree.merge`, passed by `umb worktree merge`, and a
    merge refused while the thread's turn runs (the same delta, decision 19);
  - the engine wired in `cmd/umbrald`: `CheckAll` at start (REQ-WT-006) and
    `[worktrees] commit_timeout`;
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

### [x] 2026-10-23 T-F2-04 · Parallel turns and per-provider queues
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

### [x] 2026-10-23 T-F2-05 · `full-auto` inside an active worktree
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
  - `TestALeftWorktreeClearsFullAuto_REQ_ORC_004`;
  - and, from delta `2026-10-full-auto`, `TestAnAgentsTerminalCannotLandItsOwnWork_REQ_ORC_004`
    (the daemon-side refusal delta `2026-10-worktree-methods` required).

### [x] 2026-10-23 T-F2-06 · The agent panel shows every thread
- **What:**
  - the TUI agent panel lists the focused workspace's threads, each with its attention state,
    worktree, `queued` mark and pending approvals;
  - the user can switch between them;
  - approvals of any thread are answered with control chords, as in T-F1-20.
- **REQ:** REQ-ORC-003
- **Files:** `internal/tui/…`, `specs/technical/umbral-architecture.md` §5.3e
- **Depends on:** T-F2-03, T-F2-04
- **Done:** `TestAgentPanelListsEveryThread_REQ_ORC_003` (harness) is green.

### [x] 2026-10-23 T-F2-07 · ACP client: manifest, launch, session, prompt
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
  - `stop_reason = agent_error` is added to API §6 (§7's "any stop_reason but cancelled" already
    covers it);
  - DD-012 is revised: a hosted ACP thread is the level-1 authority of the pane that owns it.
- **REQ:** REQ-ACP-001, REQ-ACP-002, REQ-ACP-006, REQ-ACP-009
- **Files:**
  - `internal/acp/…`
  - `internal/config/…`
  - `internal/api/threads.go`
  - `cmd/umbrald/…`
  - `go.mod`
  - `.go-arch-lint.yml`
  - `specs/api/umbral-daemon-api-v1.md` (§3, §4, §5.19, §5.20, §6; §7 needed no change)
  - `specs/technical/umbral-architecture.md` (DD-012, §3.2, §5.1, §5.2, §5.3f)
  - `specs/prd/umbral-f2.md` (REQ-ACP-009, 0.6)
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
  - `TestAnACPAgentsErrorTextIsNotRepeated_REQ_ACP_006`;
  - `TestAnAgentWithoutAManifestIsRefused_REQ_ACP_009`;
  - `TestTheACPEnvironmentHoldsNoSecrets_REQ_ACP_009`;
  - `TestTheOpenCodeManifestAsksForEveryPermission_REQ_ACP_009`;
  - `TestARepositoryAgentConfigIsRefused_REQ_ACP_009`;
  - `TestTheUsersGlobalAgentConfigIsNotRead_REQ_ACP_009`.

### [x] 2026-10-24 T-F2-08 · ACP mediation: permissions, files, terminals
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
  `specs/technical/umbral-architecture.md` §5.3f
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
- **Also written** (delta `2026-10-acp-mediation`): `TestACallsInputNamesItsTargets_REQ_ACP_003`,
  `TestACPFileRequestsArePerformedOnTheDecidedTarget_REQ_ACP_004`,
  `TestAnACPTerminalIsItsOwnThreadSession_REQ_ACP_005`,
  `TestAnACPTerminalServesItsOutputAndExit_REQ_ACP_005`,
  `TestATerminalsCommandLineIsQuoted_REQ_ACP_005`,
  `TestAnACPTerminalOutsideTheWriteRootAsks_REQ_ACP_005`,
  `TestATerminalsControlCharactersAreRefused_REQ_ACP_005`,
  `TestAnAgentCommandWithAControlCharacterIsRefused_REQ_ACP_005`,
  `TestATerminalRunsOnlyInAPOSIXShell_REQ_ACP_005`,
  `TestACPFilesAreMediatedInTheDaemon_REQ_ACP_004`,
  `TestAnUndecidablePermissionIsRecordedDenied_REQ_ACP_007`,
  `TestATerminalsPolicyLineKeepsItsWords_REQ_ACP_005`,
  `TestATerminalIsDecidedOnTheWordsTheShellSees_REQ_ACP_005`,
  `TestATerminalThatRunsAScriptIsFlagged_REQ_ACP_005`,
  `TestATerminalThatRunsAScriptAsks_REQ_ACP_005`.

### [x] 2026-10-24 T-F2-09 · ACP cancel, traces and `egress_visibility`
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

### [x] 2026-10-22 T-F2-10 · Third-party agent detection
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
  - written as `internal/integrations/{detect.go,domain/agents.go,adapters/manifests,adapters/proctree}`,
    `internal/api/agents.go`, `cmd/umbrald/detection.go` and `cmd/umb/agent.go`; API §5.39 (§5.38
    was taken), delta `2026-10-agent-detection`
- **Depends on:** —
- **Done:**
  - these tests are green:
    - `TestAPaneRunningOpenCodeIsLabelled_REQ_XAG_001`;
    - `TestDetectionNeverSetsState_REQ_XAG_001`;
    - `TestALocalManifestOverridesByName_REQ_XAG_002`;
    - `TestAnInvalidManifestIsSkippedAndReported_REQ_XAG_002`;
    - `TestAgentExplainNamesTheRule_REQ_XAG_003`;
  - `task schema` is green.

### [x] 2026-10-22 T-F2-11 · Router: capabilities, circuit breakers, policy, `model.route`
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
  - written as `internal/llmgw/policy.go` and `router.go`, `internal/config/models.go`,
    `internal/api/models.go` and `cmd/umbrald/gateway.go`; API §5.40, delta `2026-10-policy-router`
- **Depends on:** —
- **Done:**
  - these tests are green:
    - `TestACandidateWithoutToolsIsDropped_REQ_RTR_001`;
    - `TestABreakerOpensAndProbes_REQ_RTR_002`;
    - `TestRouterPoliciesOrderTheCandidates_REQ_RTR_003` (table: `local-first`, `cost`, `quality`);
    - `TestModelRouteExplainsTheOrder_REQ_RTR_003`;
  - `task schema` is green.

### [x] 2026-10-24 T-F2-12 · US-F2-001 live: three agents, one repository, and the F2a budgets
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
- **Written as:** `cmd/umbrald/usf2_live_test.go` and `usf2_nfr_live_test.go` (tag `live`),
  `e2e/usf2/fixture.sh` and `run.sh`, acptest's `time` mode, `docs/reports/usf2-2026-10-24.md`;
  the budgets by `TestLiveWorktreeCreateOn1GiB_NFR_F2_01`, `TestLiveIdleThreadOverhead_NFR_F2_02`
  and `TestLiveACPMediationLatency_NFR_F2_03`;
  delta `2026-10-usf2-live` (PRD F2 0.7 §9).

## Tasks — later slices (broken down when each starts)

Each of these is detailed — What, Files, Done, test names — by a delta when its slice starts.

### [x] 2026-10-26 T-F2-20 · `terminal_interact`, the input ownership lock, take-over
- **Slice:** F2b
- **What:**
  - `terminal_interact` with `send_keys`, `read_screen` and `wait_for` on the thread's PTY, the
    session `run_command` uses; named keys encoded by libghostty's key encoder in the terminal's
    modes;
  - `send_keys` is `Exec`, the reads `ReadOnly` (`Spec.ReadOnlyCalls`);
  - at the prompt the line is assembled across calls and an `enter` decided on it as
    `run_command`'s; a program's input is matched as text;
  - the state a call was decided on is checked again under the input lock;
  - `thread.take_over` and `thread.hand_back`, and the TUI's `ctrl+]` in the agent panel.
- **REQ:** REQ-FTU-001, REQ-FTU-002, REQ-FTU-003
- **Files:**
  - `internal/sessions/domain/interact.go`, `internal/sessions/interact.go`, `agent.go`,
    `blocks.go`, `service.go`, `ports/ports.go`, `adapters/ghostty/keys.go`
  - `internal/security/domain/program_input.go`, `policy.go`
  - `internal/tools/domain/tool.go`, `adapters/registry/registry.go`,
    `adapters/builtin/terminal.go`
  - `internal/api/threads.go`, `server.go`; `cmd/umbrald/main.go`, `agents.go`
  - `internal/tui/takeover.go`, `ports/ports.go`, `adapters/daemon/daemon.go`
  - API 1.54 §2, §5.43, §6; Tech 1.74 §5.2, §5.3g
- **Depends on:** slice F2a
- **Done:**
  - these tests are green:
    - `TestKeysAreValidated_REQ_FTU_001`, `TestSendKeysUsesTheEmulatorsEncoder_REQ_FTU_001`,
      `TestTerminalInteractDrivesAProgram_REQ_FTU_001`, `TestReadsNeverStartATerminal_REQ_FTU_001`,
      `TestAnInterruptKeepsTheAgentsNextBlock_REQ_FTU_001`;
    - `TestTakeOverLocksTheAgentOut_REQ_FTU_002`, `TestTakenOverIsAResult_REQ_FTU_002`,
      `TestADecisionWhileTakenOverSendsNothing_REQ_FTU_002`, `TestTakeOverMethods_REQ_FTU_002`,
      `TestTheTUIChordTakesTheThreadsTerminal_REQ_FTU_002`;
    - `TestTheLineIsAssembledAcrossCalls_REQ_FTU_003`, `TestAssembledLineAsksOnEnter_REQ_FTU_003`,
      `TestProgramInputIsMatchedAsText_REQ_FTU_003`, `TestProgramTailIsTheLineNotYetEntered_REQ_FTU_003`,
      `TestTheDecisionHoldsForTheTerminalItSaw_REQ_FTU_003`,
      `TestAProgramsUnenteredInputNeverJoinsTheNextLine_REQ_FTU_003`;
  - `task schema` is green.
- **Written as:** delta `2026-10-terminal-interact`.

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
| REQ-WT-001 | T-F2-01, T-F2-02, T-F2-03 | `TestWorktreeCreateIsOutsideTheRepo_REQ_WT_001`, `TestMigration0007Constraints_REQ_WT_001`, `TestRetentionKeepsAThreadWithAWorktree_REQ_WT_001`, `TestRepoIDIsSixteenHexOfTheCommonDir_REQ_WT_001`, `TestWorktreeRowsRoundTrip_REQ_WT_001`, `TestAFailedCreateLeavesNothing_REQ_WT_001`, `TestThreadIDsThatCanNameAPath_REQ_WT_001` |
| REQ-WT-002 | T-F2-03 | `TestAThreadWritesOnlyInItsWorktree_REQ_WT_002`, `TestATurnRunsInItsThreadsWorktree_REQ_WT_002`, `TestATurnsCwdStaysInItsWorktree_REQ_WT_002`, `TestAThreadsPTYFollowsItsCwd_REQ_WT_002`, `TestExplainUsesTheThreadsWorktree_REQ_WT_002` |
| REQ-WT-003 | T-F2-02 | `TestWorktreeDiffHasStableHunks_REQ_WT_003`, `TestParseUnifiedDiffSplitsFilesAndHunks_REQ_WT_003`, `TestHunkIDsAreStable_REQ_WT_003`, `TestWorktreeDiffReadsPathsWhateverTheConfig_REQ_WT_003`, `TestQuotedDiffPathsAreUnquoted_REQ_WT_003`, `TestShowingARedirectedWorktreeRunsNoFilter_REQ_WT_003` |
| REQ-WT-004 | T-F2-02, T-F2-03 | `TestWorktreeMergeSquashesIntoTheOriginBranch_REQ_WT_004`, `TestWorktreeMergeRefusesADirtyCopy_REQ_WT_004`, `TestWorktreeMergeRefusesAnotherBranch_REQ_WT_004`, `TestWorktreeMergeRefusesADetachedHead_REQ_WT_004`, `TestWorktreeMergeConflictLeavesBothSides_REQ_WT_004`, `TestARejectedCommitLeavesBothSides_REQ_WT_004`, `TestRefusalsAreValidationOrConflict_REQ_WT_004`, `TestWorktreeCommitTimeoutIsConfigurable_REQ_WT_004`, `TestAMergeNeverOverwritesAnIgnoredFile_REQ_WT_004`, `TestASlowPostCommitHookStillLands_REQ_WT_004`, `TestAMergeOfWorkAlreadyOnTheBranchIsEmpty_REQ_WT_004`, `TestARedirectedWorktreeGitDirIsNotFollowed_REQ_WT_004`, `TestAMergeRefusesAWorktreeChangedSinceItsReview_REQ_WT_004`, `TestAReviewedRevisionMergesThroughTheUsersFilter_REQ_WT_004`, `TestAnIgnoredAttributesFileCannotReshapeAReviewedMerge_REQ_WT_004`, `TestWorktreeMethodsOverTheWire_REQ_WT_004`, `TestUmbMayReviewButNotCreateWorktrees_REQ_WT_004`, `TestUmbWorktreeMergesExactlyTheDiffItShowed_REQ_WT_004`, `TestUmbWorktreeWaitsForALongMerge_REQ_WT_004`, `TestUmbWorktreeExplainsARefusal_REQ_WT_004`, `TestAWorktreeCannotChangeUnderARunningTurn_REQ_WT_004`, `TestAWorktreeChangesAsSoonAsItsTurnEnds_REQ_WT_004`, `TestAMergeThatLandedIsAMergeAfterAFailedCleanUp_REQ_WT_004` |
| REQ-WT-005 | T-F2-02 | `TestWorktreeDiscardNeedsConfirm_REQ_WT_005` |
| REQ-WT-006 | T-F2-02, T-F2-03 | `TestAMissingWorktreeIsMarked_REQ_WT_006`, `TestListMarksAMissingWorktree_REQ_WT_006` |
| REQ-WT-007 | T-F2-03 | `TestCloseGroupNeverDropsAnUnmergedChange_REQ_WT_007`, `TestCloseGroupReportsAMissingWorktree_REQ_WT_007`, `TestCloseGroupDiscardsOnlyThroughItsGuard_REQ_WT_007`, `TestCloseGroupHoldsEveryGuardAcrossItsDiscards_REQ_WT_007`, `TestCloseGroupOverTheWire_REQ_WT_007` |
| REQ-WT-008 | T-F2-02, T-F2-03 | `TestWorktreeRefusesANonGitDirectory_REQ_WT_008`, `TestWorktreeRefusesABareRepo_REQ_WT_008`, `TestWorktreeRefusesAnOldGit_REQ_WT_008`, `TestGitVersionFloor_REQ_WT_008`, `TestAThreadKeepsWorkingWhenItsWorktreeIsRefused_REQ_WT_008`, `TestAThreadWhoseWorktreeIsRefusedKeepsWorking_REQ_WT_008`, `TestThreadCreateSaysWhenItsWorktreeWasRefused_REQ_WT_008` |
| REQ-ORC-001 | T-F2-04, T-F2-12 | `TestThreeThreadsRunConcurrently_REQ_ORC_001`, `TestLiveUSF2001_REQ_ORC_001` (tag `live`, run by `e2e/usf2/run.sh`) |
| REQ-ORC-002 | T-F2-01, T-F2-04 | `TestAFourthTurnIsQueued_REQ_ORC_002`, `TestASecondSendToAQueuedThreadConflicts_REQ_ORC_002`, `TestCancellingAQueuedTurnDropsIt_REQ_ORC_002`, `TestAQueuedTurnIsRecoveredStopped_REQ_ORC_002`, `TestHeldTurnsStartInArrivalOrder_REQ_ORC_002`, `TestAQueuedTurnDoesNotStall_REQ_ORC_002`, `TestAWorktreeChangeRefusesAQueuedThread_REQ_ORC_002`, `TestQueuedRoundTrips_REQ_ORC_002`, `TestClosingTheRuntimeStopsAQueuedTurn_REQ_ORC_002`, `TestMaxConcurrentTurnsIsConfigurable_REQ_ORC_002` |
| REQ-ORC-003 | T-F2-01, T-F2-03, T-F2-06 | `TestAgentPanelListsEveryThread_REQ_ORC_003`, `TestThePanelsThreadJoinsTheFocusedWorkspace_REQ_ORC_003`, `TestThePanelListsAgainWhenAThreadChanges_REQ_ORC_003`, `TestATurnStartedElsewhereIsSeen_REQ_ORC_003`, `TestTheThreadListAgainstARealDaemon_REQ_ORC_003`, `TestUnknownWorkspaceIsRefused_REQ_ORC_003`, `TestAThreadKeepsItsWorkspaceAndWorktree_REQ_ORC_003` |
| REQ-ORC-004 | T-F2-01, T-F2-05, T-F2-08 | `TestMigration0007KeepsF1Rows_REQ_ORC_004`, `TestFullAutoOnlyInsideAWorktree_REQ_ORC_004`, `TestFullAutoStillAsksForNetworkAndDestructive_REQ_ORC_004`, `TestFullAutoStillAsksWhenTainted_REQ_ORC_004`, `TestALeftWorktreeClearsFullAuto_REQ_ORC_004`, `TestFullAutoAllowsWritesAndCommandsInsideTheWriteRoot_REQ_ORC_004`, `TestFullAutoFollowsTheWorktreeRow_REQ_ORC_004`, `TestAThreadInFullAutoSaysSo_REQ_ORC_004`, `TestExplainShowsFullAuto_REQ_ORC_004`, `TestAnAgentsTerminalCannotLandItsOwnWork_REQ_ORC_004`, `TestAnUnreadablePeerIsTakenForAnAgents_REQ_ORC_004`, `TestAgentShellsAreKnown_REQ_ORC_004`, `TestAClientInsideAnAgentsTerminalIsKnown_REQ_ORC_004`, `TestFullAutoDecidesACPPermissions_REQ_ORC_004` |
| REQ-ORC-005 | T-F2-04 | `TestLocalCallsQueuePerProvider_REQ_ORC_005`, `TestQueueTimeIsNotFirstTokenTime_REQ_ORC_005`, `TestACallCancelledInTheQueueIsNotSent_REQ_ORC_005`, `TestAReloadKeepsTheLocalDefault_REQ_ORC_005`, `TestOnlyAWaitingCallSaysItIsQueued_REQ_ORC_005`, `TestATimedOutCallHoldsItsPlaceUntilItsRequestCloses_REQ_ORC_005`, `TestAWaitForTheProviderDoesNotStall_REQ_ORC_005`, `TestASentCallStallsAgain_REQ_ORC_005`, `TestACompactionWaitingForItsProviderDoesNotStall_REQ_ORC_005`, `TestMaxParallelIsReadPerProvider_REQ_ORC_005` |
| REQ-ACP-001 | T-F2-07 | `TestAnACPAgentIsLaunchedInItsWorktree_REQ_ACP_001`, `TestAnACPThreadWithoutAWorktreeRunsInItsDirectory_REQ_ACP_001`, `TestAnACPAgentIsLaunchedInItsDirectory_REQ_ACP_001`, `TestTheACPAgentsAreRead_REQ_ACP_001`, `TestAnACPAgentThatDoesNotStartKeepsItsThread_REQ_ACP_001` |
| REQ-ACP-002 | T-F2-07, T-F2-12 | `TestAnACPThreadStreamsLikeAnyOther_REQ_ACP_002`, `TestAnACPThreadIsItsPanesStateAuthority_REQ_ACP_002` (pins DD-012's resolver for a hosted thread's pane; no hosted thread owns a pane session before T-F2-08's terminals), `TestAnACPTurnStreamsItsReports_REQ_ACP_002`, `TestAHostedToolCallIsNamedFromACPsKinds_REQ_ACP_002`; live, T-F2-12: `TestLiveUSF2001_REQ_ORC_001` (tag `live`) |
| REQ-ACP-003 | T-F2-08 | `TestACPPermissionEntersTheApprovalQueue_REQ_ACP_003`, `TestACPNeverSelectsAllowAlways_REQ_ACP_003`, `TestACPKindsMapToRisks_REQ_ACP_003`, `TestACallsInputNamesItsTargets_REQ_ACP_003` |
| REQ-ACP-004 | T-F2-08 | `TestACPWriteOutsideTheWorktreeAsks_REQ_ACP_004`, `TestACPReadOutsideTheWorktreeAsks_REQ_ACP_004`, `TestACPReadInsideTheWorktreeIsReadOnly_REQ_ACP_004`, `TestACPWriteCoveredByAPermissionIsNotAskedTwice_REQ_ACP_004`, `TestACPFileRequestsArePerformedOnTheDecidedTarget_REQ_ACP_004`, `TestACPFilesAreMediatedInTheDaemon_REQ_ACP_004` |
| REQ-ACP-005 | T-F2-01, T-F2-08 | `TestACPTerminalIsAnAuditedBlock_REQ_ACP_005`, `TestACPTerminalsAreCapped_REQ_ACP_005`, `TestAnACPTerminalIsItsOwnThreadSession_REQ_ACP_005`, `TestAnACPTerminalServesItsOutputAndExit_REQ_ACP_005`, `TestATerminalsCommandLineIsQuoted_REQ_ACP_005`, `TestAnACPTerminalOutsideTheWriteRootAsks_REQ_ACP_005`, `TestATerminalsControlCharactersAreRefused_REQ_ACP_005`, `TestAnAgentCommandWithAControlCharacterIsRefused_REQ_ACP_005`, `TestATerminalRunsOnlyInAPOSIXShell_REQ_ACP_005`, `TestATerminalsPolicyLineKeepsItsWords_REQ_ACP_005`, `TestATerminalIsDecidedOnTheWordsTheShellSees_REQ_ACP_005`, `TestATerminalThatRunsAScriptIsFlagged_REQ_ACP_005`, `TestATerminalThatRunsAScriptAsks_REQ_ACP_005` |
| REQ-ACP-006 | T-F2-07 | `TestAnACPAgentCrashEndsTheTurn_REQ_ACP_006`, `TestASilentACPAgentEndsTheTurn_REQ_ACP_006`, `TestAnAgentWaitingOnUmbralIsNotSilent_REQ_ACP_006`, `TestAPendingApprovalPausesTheSilenceTimer_REQ_ACP_006`, `TestAnInvalidACPMessageEndsTheTurn_REQ_ACP_006`, `TestAnACPAgentErrorEndsTheTurnAndKeepsTheThread_REQ_ACP_006`, `TestAnACPAgentsErrorTextIsNotRepeated_REQ_ACP_006` |
| REQ-ACP-007 | T-F2-01, T-F2-08, T-F2-09, T-F2-12 | `TestAnACPTurnHasATrace_REQ_ACP_007`, `TestAnACPThreadIsMarkedUnobserved_REQ_ACP_007`, `TestACPToolCallsAreRecordedWithTheirDecision_REQ_ACP_007`, `TestAnUndecidablePermissionIsRecordedDenied_REQ_ACP_007`, `TestAnACPThreadDisclosesItsTrafficAtCreation_REQ_ACP_007`, `TestTheThreadListReadsAHostedThread_REQ_ACP_007`, `TestAgentPanelSaysAHostedThreadIsUnobserved_REQ_ACP_007`; live, T-F2-12: `TestLiveUSF2001_REQ_ORC_001` (tag `live`) |
| REQ-ACP-008 | T-F2-07, T-F2-09 | `TestACPCancelStopsWithin500ms_REQ_ACP_008`, `TestACancelledACPTurnSendsSessionCancel_REQ_ACP_008`, `TestACancelKillsTheACPTerminalsFirst_REQ_ACP_008`, `TestATerminalStartedDuringACancelIsKilled_REQ_ACP_008`, `TestAnUnresponsiveACPAgentIsKilledAfter5s_REQ_ACP_008` |
| REQ-ACP-009 | T-F2-07 | `TestAnAgentWithoutAManifestIsRefused_REQ_ACP_009`, `TestTheACPEnvironmentHoldsNoSecrets_REQ_ACP_009`, `TestTheOpenCodeManifestAsksForEveryPermission_REQ_ACP_009`, `TestARepositoryAgentConfigIsRefused_REQ_ACP_009`, `TestTheUsersGlobalAgentConfigIsNotRead_REQ_ACP_009`, `TestABadACPEntryStopsTheDaemon_REQ_ACP_009` |
| REQ-XAG-001 | T-F2-10 | `TestAPaneRunningOpenCodeIsLabelled_REQ_XAG_001`, `TestDetectionNeverSetsState_REQ_XAG_001`, `TestBuiltinManifestsCompile_REQ_XAG_001`, `TestBuiltinsRecogniseTheAgentsAndNothingElse_REQ_XAG_001`, `TestSubtreeFindsAnAgentUnderTheShell_REQ_XAG_001`, `TestSubtreeOfANonexistentProcessIsEmpty_REQ_XAG_001`, `TestAnAgentThatIsThePanesRootIsLabelled_REQ_XAG_001` |
| REQ-XAG-002 | T-F2-10 | `TestALocalManifestOverridesByName_REQ_XAG_002`, `TestAnInvalidManifestIsSkippedAndReported_REQ_XAG_002`, `TestManifestValidation_REQ_XAG_002`, `TestOverlayReplacesByName_REQ_XAG_002` |
| REQ-XAG-003 | T-F2-10 | `TestAgentExplainNamesTheRule_REQ_XAG_003`, `TestDetectionIsDeterministic_REQ_XAG_003`, `TestAgentExplainOverTheWire_REQ_XAG_003`, `TestUmbAgentExplain_REQ_XAG_003` |
| REQ-RTR-001 | T-F2-11 | `TestACandidateWithoutToolsIsDropped_REQ_RTR_001` |
| REQ-RTR-002 | T-F2-11 | `TestABreakerOpensAndProbes_REQ_RTR_002`, `TestRouterBreakerKeys_REQ_RTR_002` |
| REQ-RTR-003 | T-F2-11 | `TestRouterPoliciesOrderTheCandidates_REQ_RTR_003`, `TestModelRouteExplainsTheOrder_REQ_RTR_003`, `TestModelRouteOverTheWire_REQ_RTR_003` |
| REQ-FTU-001 | T-F2-20, T-F2-23 | `TestKeysAreValidated_REQ_FTU_001`, `TestSendKeysUsesTheEmulatorsEncoder_REQ_FTU_001`, `TestTerminalInteractDrivesAProgram_REQ_FTU_001`, `TestReadsNeverStartATerminal_REQ_FTU_001`, `TestAnInterruptKeepsTheAgentsNextBlock_REQ_FTU_001`; T-F2-23's named when it starts |
| REQ-FTU-002 | T-F2-20 | `TestTakeOverLocksTheAgentOut_REQ_FTU_002`, `TestTakenOverIsAResult_REQ_FTU_002`, `TestADecisionWhileTakenOverSendsNothing_REQ_FTU_002`, `TestTakeOverMethods_REQ_FTU_002`, `TestTheTUIChordTakesTheThreadsTerminal_REQ_FTU_002` |
| REQ-FTU-003 | T-F2-20 | `TestTheLineIsAssembledAcrossCalls_REQ_FTU_003`, `TestAssembledLineAsksOnEnter_REQ_FTU_003`, `TestProgramInputIsMatchedAsText_REQ_FTU_003`, `TestProgramTailIsTheLineNotYetEntered_REQ_FTU_003`, `TestTheDecisionHoldsForTheTerminalItSaw_REQ_FTU_003`, `TestAProgramsUnenteredInputNeverJoinsTheNextLine_REQ_FTU_003` |
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
| 2026-10-22 | T-F2-02 | done | `internal/worktrees` with `gitcli` and `sqlstore` adapters, and `[worktrees] commit_timeout`. The squash snapshots the worktree through a private index, then runs `cherry-pick --no-commit` and `commit` in the clean main copy, and rolls back with `reset --hard`. git 2.34 has no `merge-tree --write-tree`. Mutations that drop the rollback, the dirty check or the commit timeout turn the tests red. The signing-prompt case is killed at 2 s, not at the 30 s it would sleep. Delta `2026-10-worktree-engine`. |
| 2026-10-22 | T-F2-10 | done | Built-in manifests for five agents, local TOML manifests read strictly and laid over them by name, and a scan every 2 s of each pane's process tree — its root included, so a pane launched with the agent is labelled — read once per scan from `/proc` or `sysctl`, that labels a pane and never sets its state. `agent.explain` and `umb agent explain` name the manifest, the rule, the pid and the executable's basename, never the argv. Verified against a real daemon: a pane running `exec -a opencode` was labelled within one scan, its state stayed `unknown`, and an invalid local file was logged and listed. A skip reason names the field, key or line and never quotes a value; quoting one, or leaving the root out of the tree, turns a test red. Spec-guardian's review added the root process, argv kept out of memory, and the bounded read. Delta `2026-10-agent-detection`. |
| 2026-10-22 | T-F2-11 | done | The router drops candidates without tools, vision, structured output, reasoning or the window; skips a provider whose breaker is open; and orders what is left by `[router] policy`. `model.route` answers the same plan with each candidate's rank and reason, and sends nothing. Each of these mutations turns a test red: ignoring vision, local-first or the unpriced rule; never opening a breaker; skipping the cooldown; a failed probe counted as an ordinary failure; dropping the policy from the daemon's wiring; and, added after spec-guardian's review, two probes in flight at once, a late non-probe success closing an open breaker, success reported at the end of the answer instead of its first event, and dropped candidates in policy order. The F1 fallback test now raises the breaker threshold, because four failures of one provider in one call open it at the third. Delta `2026-10-policy-router`. |
| 2026-10-22 | T-F2-03 | done | `worktree.create`, `list`, `get`, `diff`, `merge`, `discard` and `close_group` over the engine's inbound port; `thread.create` takes `workspace` and `worktree`, and keeps the thread without isolation, naming `worktree_refused`, when git refuses its worktree (REQ-WT-008). A turn reads the thread's active worktree as it starts and runs there, the worktree its write root; the thread's PTY is replaced when its cwd changes. A worktree never changes under a running turn (`CONFLICT` `running`). Refusals carry `data.reason`, `paths` and `output`. `umb worktree merge` shows the diff and lands its revision. `CheckAll` runs at start. The real-daemon test was mutated (turns ignoring the worktree) and reddened. The spec-guardian review (FIX FIRST) found the running guard refusing just after `thread.turn_finished`, `umb worktree merge` cut at 10 s while the daemon landed the commit, `policy.explain` ignoring the worktree, a `close_group` able to discard before a later guard refused, `thread.create` withholding the thread against REQ-WT-008, `umb worktree merge|discard` missing from the destructive patterns, a landed merge answered as a failure when its clean-up failed, and a symlink able to move a turn's cwd out of its worktree; all are fixed with a test each, REQ-WT-001's path wording is amended (F2 PRD 0.5), and the traceability rows are filled in. Delta `2026-10-worktree-methods`. |
| 2026-10-23 | T-F2-04 | done | Turns of different threads run at once; `[agents] max_concurrent_turns` (3) holds a turn over it as `queued`, `running` and `working`, granted in arrival order through `internal/slots`, a new leaf semaphore; a second send `CONFLICT`s, a cancel drops it `cancelled` with no model call, it is never stall-checked while held, and the `worktree.*` guard refuses its thread. A provider's `max_parallel` (default 1 local, no limit remote) queues model calls before the first-token clock, so queue time is never a timeout or part of `first_token_ms`. Every new test was mutated and reddened (no limit, queued stalls, the end keeping `queued`, a local default of no limit, the clock started before the queue). The spec-guardian review (FIX FIRST) found a `config.reload` dropping a local provider's default of one call, a wait for the provider announced as a stall, a place freed before an abandoned request closed, and no test of a daemon stop with held turns; each is fixed with a mutated test (the router now yields its own `queued` and `sent` events, and the pump releases the place); a second pass found the compaction's call outside the exemption and the clock restarting too late, both fixed with mutated tests. Delta `2026-10-parallel-turns`. |
| 2026-10-23 | T-F2-05 | done | `thread.update` enters `full-auto` only with an active worktree, checked by the runtime and again in the store's write; `thread.create` refuses it. The flag sits beside the thread's own mode and migration 0008's triggers clear it when the worktree leaves `active`. A turn reads its cwd, write root and effective mode together (`agents.TurnPlace`), so `full-auto` always decides inside a worktree: writes and commands whose cwd is inside it run unasked, `Network`, destructive patterns and taint still ask; `policy.explain` shows it. The daemon now refuses a client inside an agent's terminal — found by the kernel's peer pid, its parents and its POSIX session — the methods that land, drop or approve the agent's work, with `PERMISSION_DENIED`. Every new test was mutated and reddened. The spec-guardian review (FIX FIRST) found a peer that exited after connecting let through, `pane.split` with a command and `session.input` as routes around the check (an automated security review then added `session.create` and `env` in a split or layout, so every process-starting method is refused), the test covering 8 of the methods, and an Exec with no cwd allowed; all are fixed with mutated tests. Open for the Tech Lead: the refused list reaches F1 surfaces (an approved `umb skill install`, `umb mcp add` or `umb worktree merge` in a turn now fails in every mode) and awaits ratification; an Exec's cwd is the turn's, not the shell's current one; `setsid` plus a fork, or a launcher outside the terminal, escapes the check. Delta `2026-10-full-auto`. |
| 2026-10-23 | T-F2-03 … T-F2-05 | reviewed | The Tech Lead reviewed the second batch and ratified decision 8 of delta `2026-10-full-auto`, the refused-method list as written, over narrowing it to the worktree methods. The `umb ai` flake found during T-F2-04 was assigned to T-HRD-03 and fixed in the same PR (`client.NewUnboundedStream`). |
| 2026-10-23 | T-F2-06 | done | The panel lists the focused workspace's threads from one `session.snapshot` (none focused: the threads of none), each with attention, `queued`, worktree, pending approvals and title; up and down switch the selected thread, which is viewed, shown, sent to and stopped; transcripts are kept per thread from this run; the panel's own thread is created in the focused workspace. Delta `2026-10-panel-threads`, self-approved under the standing F2 instruction, folded into Tech 1.62 §5.3e. A thread is running once its events arrive or a listing says so, whoever started the turn; listings carry their snapshot `seq` and an older one is dropped; views of unlisted threads are released. spec-guardian asked for fixes first (a turn started elsewhere unseen, the wire mapping untested); fixed, and sixteen mutations each reddened a test, one against a real daemon. |
| 2026-10-23 | T-F2-07 | done | `internal/acp` hosts `[[acp.agents]]` on acp-go-sdk: one process per thread, launched through the built-in OpenCode manifest (`"*"` and every named permission `ask`, no MCP, no plugin) in an isolated `acp/<thread>/` home under the runtime directory, with `PATH`, `LANG`, the isolation, `OPENCODE_CONFIG` and the entry's resolved `env_refs` only; `initialize` then `session/new` in the worktree or the thread's directory; `thread.send` sends `session/prompt`, chunks and tool calls are persisted and published like Umbral's own (`acp:<kind>`, `via = acp`, `unmediated`); an exit, a non-ACP line or `[acp] silence_timeout` ends the turn `agent_error` and the next send relaunches. `thread.create` takes `agent`, refuses it with `data.reason` before writing, and answers `agent_unavailable` when the agent does not start. Until T-F2-08 every permission, file and terminal request is refused. Delta `2026-10-acp-client`, self-approved under the standing F2 instruction, folded into API 1.48 and Tech 1.63. Twenty-two mutations each reddened a test, five of them against a real daemon. The spec-guardian pass (FIX FIRST) was applied: the agent's stderr is discarded, `OPENCODE_*` env refs stop the daemon, a hosted call's kind is ACP's closed set, an answer just before an exit is not `agent_error`, and PRD F2 0.6 amends REQ-ACP-009's `project_config` response. |
| 2026-10-24 | T-F2-08 | done | A hosted agent's permissions, `fs/*` and `terminal/create` are mediated by the running turn's sink in the agent runtime: permissions decided per target (locations, `raw_input` command/cwd/url) with the strictest verdict under `acp:<kind>`, answered `allow_once`/`reject_once` and recorded on the reported call's row; reads ask outside the write root, writes are covered per turn by an allowed permission's locations, both performed by Umbral with `read_file`'s and `write_file`'s guards; `terminal/create` is `run_command`'s decision on a POSIX-quoted line, asks outside the write root even under an allow rule, is capped by `[acp] max_terminals` and runs in `sessions.StartTerminal`'s thread-owned session (bash when installed) as a `via = acp` block, served as plain text. The `Mediator` port is gone. Delta `2026-10-acp-mediation`, self-approved under the standing F2 instruction, folded into API 1.49, Tech 1.64 and Data Model 1.26. Seventeen mutations each reddened a test; the one that first survived (the terminal's cwd check, redundant under full-auto) gained `TestAnACPTerminalOutsideTheWriteRootAsks_REQ_ACP_005`. The spec-guardian pass (FIX FIRST) was applied: `write_file`'s action now carries a content, without which the real registry refused every `edit` permission and `fs/*` request — the fakes accepted it, and a daemon test now covers it; a control character in a terminal or an agent command is refused, since the shell's line editor acts on it inside quotes; terminals run in bash or zsh, never fish; an undecidable permission is recorded `deny`; and the terminal's row ends `ok` only once its line is written. A security review then found the quoted line hiding its program from the destructive floor (`env 'A=x y' rm -rf /`); the policy now also decides a line with one token per shell word, and a terminal holding a script of its own (`sh -c`, `eval`, `env -S`) always asks. |
| 2026-10-24 | T-F2-09 | done | A cancelled hosted turn kills the commands of the agent's terminals, keeping their sessions, before `session/cancel`; on a real daemon a `sleep 30` terminal's block ended within 500 ms of the cancel, against 5006 ms before. The 5 s process kill is unchanged. Each reported tool call is a `tool.acp:<kind>` span and each mediated request a `tool.acp:<method>` span under the turn's. `umb thread create --agent` sends `agent` and says on stderr that the agent's own network traffic and file reads are not recorded, and why it did not start; the panel lists a hosted thread with its agent and `unobserved` and adds the notice while it is selected. Delta `2026-10-acp-cancel-traces`, API 1.50, Tech 1.65. |
| 2026-10-24 | T-F2-12 | done | US-F2-001 was run four times against a real daemon with two `gpt-oss:20b` threads and OpenCode 1.18.33 over ACP on the local Ollama, each thread in its own worktree (`docs/reports/usf2-2026-10-24.md`). In all four runs the main copy was untouched before merging, no thread's change reached another's worktree, every turn had a trace and every hosted write and command was decided by the policy engine. Run 1 passed every criterion: 3/3 `end_turn` and three clean merges. Run 2 failed on the model's work: the hosted rename did nothing, and fix ended `tool_error` with the test passing. Run 3 failed because fix also edited `go.mod`, approved by the simulated user. Run 4, with the hardened test, counted 0 `egress_log` rows and found no process left behind; it failed because the hosted rename again did nothing. The budgets were all met: worktree creation on 1 GiB took a p95 of 1.24 s against 2 s, an idle thread cost 0.16 MiB against 15 MiB, and ACP mediation took a p95 of 1.1 ms against 20 ms. The run found that the hosted agent is not offline: with an empty isolated cache OpenCode fetches its provider package from npm and a model catalogue at start, its own disclosed traffic. PRD F2 0.7 §9 and Q-F2-01 now say so (delta `2026-10-usf2-live`, self-approved). Two questions went to T-HRD-02: an offline manifest, and reads across sibling worktrees. A third question goes to the Tech Lead for the F2a close: what "US-F2-001 passes" means. The spec-guardian pass (FIX FIRST) was applied: the run's commands get their own home and no Go toolchain or module fetch, as in US-003; `egress_log` is asserted empty; no process may outlive the daemon; the unavailable-tool exemption covers `acp:other` rows only; and run 1 is stated as judged from its logged rows. With this, every F2a task is done. |
| 2026-10-24 | F2a | closed | The Tech Lead closed slice F2a (delta `2026-10-f2a-close`): "US-F2-001 passes" means the daemon's properties held in every run measured and at least one run completed every task within its files, which the four runs of 2026-10-24 meet. The F2a budgets stay `live` measurements, not `task perf` gates. PRD F2 0.8. F2b … F2f start from their own deltas. |
| 2026-10-26 | T-F2-20 | done | `terminal_interact` drives the thread's PTY: keys encoded by the emulator in the terminal's modes, the screen read, and waits on a regex, quiet or the program's end. At the prompt only text, `backspace` and a final `enter` or `ctrl+c` are accepted, the line is kept across calls and its `enter` is decided as `run_command`'s, so `rm -` then `rf /` asks; a program's input is matched as text, so `DROP TABLE` asks in `psql` and `SELECT *` does not. The tool recomputes its target at run time and the sessions module checks the state again under the input lock. A first run found the shell dropping the first byte after an interrupt and the kernel flushing what followed `^C` in the same write: `ctrl+c` at the prompt is now the call's last key and the next call starts with an empty line. `thread.take_over`/`hand_back` (API §5.43) and the TUI's `ctrl+]` hand the terminal to the user and back; a held PTY is never replaced. Every new test was mutated and reddened. The spec-guardian pass (FIX FIRST) was applied: a program's line is kept across calls and also read by the shell parser, because a program that leaves its input unread hands it to the shell (`rm -` then `rf ~` to `sleep`, or `x=rm; $x -rf ~`), and full-auto asks for program input; reads never start or replace a shell; a call decided while taken over writes nothing after the hand-back; the holder's close hands back; a hand-back types nothing and the agent's next write at the prompt starts with ctrl+e ctrl+u, after a ctrl+c lost the race with bash drawing its prompt; and the empty line after an interrupt is awaited so its prompt keeps the next line's claim. A verify pass then found a program's unentered input joining the agent's next prompt line, which the same discard now drops, and an allow rule covering a whole program line a shell would split, which must now cover every command. PRD F2 0.11 amends REQ-FTU-003. Delta `2026-10-terminal-interact`, self-approved under the standing F2 instruction, folded into API 1.54 and Tech 1.74. |
| — | — | — | — |
