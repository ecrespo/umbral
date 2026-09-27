# Tasks — Umbral F1 (Agent, models, MCP and security)

> Source specs: `specs/prd/umbral-mvp.md` · `specs/api/umbral-daemon-api-v1.md` · `specs/technical/umbral-architecture.md` · `specs/data-model/umbral-schema.md` · `specs/plans/umbral-mvp-plan.md`
> Plan phase covered: F1 · Generated: 2026-09-11

## Conventions for this file

Same as `umbral-f0-tasks.md`. Tests against real models use the `live` build tag and do not run in
default CI.

## Tasks

### [ ] T-F1-01 · Migration 0005 (agent, models, audit, MCP)
- **What:** Data Model tables §2.4c to §2.13 with their indexes, plus recovery §6 steps 3-4.
  `threads` (§2.5) is **not** created here: migration 0001 already created it (§5, finding A-01).
  The structure tables (§2.4b) are **not** created here either: migration 0003 owns them (T-F0-14).
  `messages` includes `client_msg_id` and its partial unique index `idx_messages_client_msg`.
- **REQ:** REQ-AGT-011, REQ-LLM-005, REQ-SEC-002
- **Files:** `internal/store/migrations/0005_agent.sql`, `internal/store/**`
- **Depends on:** F0 complete
- **Done:** `TestMigration0005Constraints` and `TestRecoveryExpiresPendingApprovals_REQ_AGT_011` green.

### [ ] T-F1-02 · Keyring and configuration loader
- **What:**
  - TOML loader with JSON Schema;
  - resolution of `keyring:<path>` with go-keyring;
  - rejection (`CONFIG_INVALID`) of plaintext API keys;
  - degraded start when the keyring is unavailable: the daemon does not abort, the providers whose
    credential is `keyring:<path>` are disabled, their models get `health = down` with reason
    `keyring_unavailable` and `umb status` shows that reason (REQ-SEC-008);
  - the `env:<VAR>` fallback, accepted only when the keyring is unavailable and
    `[secrets] allow_env = true`, reported as `degraded` with reason `env_secret` (REQ-SEC-012);
  - `config.get` and `config.reload`.
- **REQ:** REQ-SEC-004, REQ-SEC-008, REQ-SEC-012
- **Files:** `internal/config/**`, `internal/security/adapters/keyring/**`
- **Depends on:** T-F1-01
- **Done:** `TestPlaintextKeyRejected_REQ_SEC_004`, `TestKeyringUnavailableDisablesProviders_REQ_SEC_008` and `TestEnvFallbackOnlyWhenEnabled_REQ_SEC_012` green; with `allow_env = false` and no keyring, no provider starts with a credential.

### [ ] T-F1-03 · [P] Policy engine
- **What:** pure `Decide()` following DD-006 precedence and the Tech Design §5.3 table; destructive-pattern list; workspace computation; taint.
- **REQ:** REQ-AGT-009, REQ-AGT-013, REQ-AGT-014, REQ-SEC-005, REQ-SEC-006
- **Files:** `internal/security/domain/policy*.go`
- **Depends on:** T-F1-01
- **Done:** table tests `TestPolicy_*_REQ_AGT_009/013/014`, `TestDestructiveAlwaysAsk_REQ_SEC_005` and `TestTaintedRequiresAsk_REQ_SEC_006` green, with ≥ 90 % package coverage.

### [ ] T-F1-04 · [P] Secret redaction
- **What:** rules (AWS/GCP/GitHub/GitLab/OpenAI/Anthropic/HF keys, JWT, PEM private keys, `.env`) plus an entropy detector; replacement with `[REDACTED:<rule>]`.
- **REQ:** REQ-SEC-001
- **Files:** `internal/security/domain/redact*.go`, `testdata/redact/**`
- **Depends on:** T-F1-01
- **Done:** `TestRedactionCorpus_REQ_SEC_001` (corpus with ≥ 30 positives and ≥ 30 negatives) green.

### [ ] T-F1-05 · Gateway: ports, catalog and openai-compat adapters
- **What:**
  - `Provider` port (architecture §6) with normalized events;
  - `llamacpp`, `lmstudio`, `openrouter` and `openai-compat` adapters on top of Fantasy;
  - discovery through `/v1/models`;
  - `model.list` with `refresh`.
- **REQ:** REQ-LLM-001, REQ-LLM-002
- **Files:** `internal/llmgw/{ports,catalog,adapters/openaicompat,adapters/openrouter}/**`
- **Depends on:** T-F1-02
- **Done:** tests with a fake server `TestDiscoverModels_REQ_LLM_002` and `TestStreamNormalized_REQ_LLM_001` green.

### [ ] T-F1-06 · Native Ollama adapter
- **What:**
  - `/api/chat` with streaming, tools, `think`, `format` and `num_ctx`;
  - `keep_alive`;
  - discovery through `/api/tags`.
- **REQ:** REQ-LLM-001, REQ-LLM-006
- **Files:** `internal/llmgw/adapters/ollama/**`
- **Depends on:** T-F1-05
- **Done:** `TestOllamaSendsNumCtx_REQ_LLM_006` (fake) green; `TestOllamaLive` (tag `live`) green against `gpt-oss:20b`.

### [ ] T-F1-07 · Router, fallback, usage and egress hooks
- **What:**
  - candidates per class and offline filter;
  - capability filter;
  - health;
  - fallback on 429, 5xx and first-token timeout (30 s remote / 120 s local);
  - recording in `usage`;
  - redaction before sending;
  - `egress_log` for non-loopback hosts.
- **REQ:** REQ-LLM-003, REQ-LLM-004, REQ-LLM-005, REQ-SEC-001, REQ-SEC-002
- **Files:** `internal/llmgw/router/**`
- **Depends on:** T-F1-04, T-F1-06
- **Done:** tests `TestFallbackOn429_REQ_LLM_003`, `TestOfflineRejectsRemote_REQ_LLM_004`, `TestUsageRecorded_REQ_LLM_005` and `TestEgressLoggedForRemote_REQ_SEC_002` green.

### [ ] T-F1-08 · [P] HF router and OmniRoute presets
- **What:** commented configuration templates (`examples/models.toml`) and validation that both go through `openai-compat`.
- **REQ:** REQ-LLM-007
- **Files:** `examples/models.toml`, `internal/config/presets*.go`
- **Depends on:** T-F1-05
- **Done:** `TestPresetsLoad_REQ_LLM_007` green.

### [ ] T-F1-09 · Tool registry and built-in tools
- **What:**
  - microkernel registry with JSON Schema validation;
  - tools `read_file`, `write_file`, `edit_file` (with unified diff), `grep`, `glob`, `list_dir` and `fetch_url`;
  - `fetch_url` marks taint.
- **REQ:** REQ-AGT-002, REQ-AGT-012, REQ-AGT-018, REQ-SEC-006
- **Files:** `internal/tools/{ports,builtin}/**`
- **Depends on:** T-F1-03
- **Done:** `TestToolSchemasDeclared_REQ_AGT_002`, `TestEditFileProducesDiff_REQ_AGT_012`, `TestFetchMarksTaint_REQ_SEC_006` and `TestFetchUrlRefusesPrivateRanges_REQ_AGT_018` green, including the redirect-to-loopback case.

### [ ] T-F1-10 · `run_command` in the thread PTY
- **What:**
  - dedicated PTY per thread (session with `owner_thread_id`), with the `agent` lock;
  - block with `origin = agent`;
  - process group so it can be terminated.
- **REQ:** REQ-AGT-003, REQ-AGT-007
- **Files:** `internal/tools/builtin/runcommand*.go`, `internal/sessions/**`
- **Depends on:** T-F1-09
- **Done:** `TestRunCommandCreatesAgentBlock_REQ_AGT_003` green.

### [ ] T-F1-11 · [P] Context: rules, attachments and git
- **What:**
  - rules-file lookup from the repo root down to the cwd;
  - `@file`, `@directory` and `@block:<id>` attachments with truncation at 256 KiB;
  - git context;
  - prompt templates.
- **REQ:** REQ-CTX-001, REQ-CTX-002, REQ-CTX-003, REQ-CTX-005
- **Files:** `internal/context/**`
- **Depends on:** T-F1-01
- **Done:** tests `…_REQ_CTX_001/002/003/005` green with fixture repos.

### [ ] T-F1-12 · Token budget and compaction
- **What:** token counting per family (Q-03), response reserve, compaction via summary with the `fast` class and the `context.compacted` notification.
- **REQ:** REQ-CTX-004
- **Files:** `internal/context/budget/**`
- **Depends on:** T-F1-11
- **Done:** `TestCompactionTriggeredOverWindow_REQ_CTX_004` green.

### [ ] T-F1-13 · Agent runtime and `thread.*`
- **What:**
  - per-turn loop that persists before notifying (DD-007);
  - methods `thread.create`, `send`, `get`, `list` and `update`;
  - notifications `thread.delta`, `thread.tool_call` and `thread.turn_finished`;
  - `max_steps` and budget;
  - model change on the next turn;
  - `ask` mode exposes only ReadOnly tools;
  - `thread.send` idempotency by `client_msg_id`: a repeated key returns the original
    `{turn_id, message_id}` without creating a turn and without re-running its tools, including
    while the first turn is still running (REQ-AGT-015).
- **REQ:** REQ-AGT-001, REQ-AGT-008, REQ-AGT-009, REQ-AGT-010, REQ-AGT-011, REQ-AGT-015
- **Files:** `internal/agents/**`, `internal/api/threads.go`
- **Depends on:** T-F1-07, T-F1-10, T-F1-12
- **Done:** tests `…_REQ_AGT_001/008/009/010/011` green with a scripted fake provider; `TestSendIdempotentByClientMsgID_REQ_AGT_015` green (two identical sends → one turn, and the tools run once).

### [ ] T-F1-14 · Approval flow
- **What:**
  - `approval.requested` pauses the turn and `approval.respond` resumes it;
  - rule persistence (`thread` / `always`); destructive patterns ignore `always`;
  - denial returns `denied_by_user`;
  - `approval.list`.
- **REQ:** REQ-AGT-004, REQ-AGT-005, REQ-SEC-005
- **Files:** `internal/agents/approval*.go`, `internal/api/approvals.go`
- **Depends on:** T-F1-13
- **Done:** `TestAskPausesTurn_REQ_AGT_004` and `TestDenyReturnsDeniedByUser_REQ_AGT_005` green.

### [ ] T-F1-15 · Repair of invalid tool calls
- **What:** one retry with a repair message; on the second failure, `stop_reason = tool_error`; increment `umbral_tool_calls_invalid_total{model}`.
- **REQ:** REQ-AGT-006, REQ-OBS-002
- **Files:** `internal/agents/repair*.go`, `internal/obs/metrics.go`
- **Depends on:** T-F1-13
- **Done:** `TestInvalidArgsRepairOnce_REQ_AGT_006` and `TestInvalidMetricIncrements_REQ_OBS_002` green.

### [ ] T-F1-16 · Cancellation
- **What:** `thread.cancel` cancels the turn's context; SIGTERM to the process group; SIGKILL after 300 ms.
- **REQ:** REQ-AGT-007
- **Files:** `internal/agents/cancel*.go`
- **Depends on:** T-F1-13
- **Done:** `TestCancelUnder500ms_REQ_AGT_007` (with `sleep 60` running) green.

### [ ] T-F1-17 · MCP client
- **What:**
  - stdio and streamable HTTP connections with the official SDK;
  - `mcp_<server>_<tool>` prefix;
  - `mcp.server.add`/`remove`/`list`, with `trust = untrusted` by default;
  - 10 s timeout and exponential backoff (at most 5 attempts);
  - `ask` policy by default.
- **REQ:** REQ-MCP-001, REQ-MCP-002, REQ-MCP-003, REQ-MCP-004
- **Files:** `internal/mcp/client/**`, `internal/tools/mcptools/**`
- **Depends on:** T-F1-09
- **Done:** tests with a test MCP server written in Go `…_REQ_MCP_001/002/003/004` green.

### [ ] T-F1-18 · OTel observability
- **What:**
  - root span `agent.turn` with children `llm.call` and `tool.*`, with GenAI attributes;
  - `trace_id` in `slog` logs;
  - optional OTLP exporter.
- **REQ:** REQ-OBS-001, REQ-OBS-003, REQ-OBS-004
- **Files:** `internal/obs/**`
- **Depends on:** T-F1-13
- **Done:** `TestTurnTraceHasSpans_REQ_OBS_001` (in-memory exporter) and `TestOrchestrationMetricsExposed_REQ_OBS_004` green.

### [ ] T-F1-19 · `umb ai` with stdin
- **What:** ephemeral thread in `ask` mode; stdin as an attachment (at most 1 MiB, truncation noted); streaming to stdout; non-zero exit code if the turn ends in error.
- **REQ:** REQ-CLI-001
- **Files:** `cmd/umb/ai.go`
- **Depends on:** T-F1-13
- **Done:** `TestUmbAiPipesStdin_REQ_CLI_001` green.

### [ ] T-F1-20 · TUI: agent panel
- **What:**
  - thread panel with deltas;
  - toggle input between shell and agent with `ctrl+space`;
  - approval queue with `[a]pprove`, `[d]eny` and `a[l]ways`, plus a diff view;
  - "attach to agent" action on a block.
- **REQ:** REQ-TUI-001, REQ-TUI-002, REQ-TUI-003
- **Files:** `internal/tui/agent/**`
- **Depends on:** T-F1-14
- **Done:** teatest tests `…_REQ_TUI_002/003` green; `docs/qa/f1-tui.md` completed.

### [ ] T-F1-21 · Live US-003 E2E
- **What:**
  - Go fixture repo with a broken test;
  - script that runs 20 times: create an `auto-edit` thread, attach the failed block, "fix it", auto-approve `run_command go test`, with `router.offline = true`;
  - report with success rate, invalid tool call rate and latency.
- **REQ:** REQ-AGT-001, REQ-LLM-004, REQ-SEC-002 (PRD §4.1 goal)
- **Files:** `e2e/us003/**`, `docs/reports/us003-<date>.md`
- **Depends on:** T-F1-20
- **Done:** report with ≥ 14/20 successes and 0 rows in `egress_log` during the run.

### [ ] T-F1-22 · Hardening
- **What:** retention job (Data Model §4), E2E verification of recovery after `kill -9` of the daemon, user guide for provider configuration.
- **REQ:** REQ-AGT-011, Art. 6
- **Files:** `internal/store/retention*.go`, `docs/user/*.md`
- **Depends on:** T-F1-21
- **Done:** `TestRetentionPurgesRawChunks` and `TestCrashRecovery_REQ_AGT_011` green.

### [ ] T-F1-23 · Wait engine
- **What:**
  - `thread.wait` owned by the server and driven by events, pinning the current turn;
  - `wait` object inside `thread.send` as one ordered submission;
  - `block.wait_output` with RE2 over recent output;
  - typed `TIMEOUT` carrying the last observed state.
- **REQ:** REQ-AUT-001, REQ-AUT-002, REQ-AUT-003, REQ-AUT-004
- **Files:** `internal/agents/wait/**`, `internal/api/waits.go`, `cmd/umb/wait.go`
- **Depends on:** T-F1-13
- **Done:** tests `TestWaitPinsTurn_REQ_AUT_001`, `TestSendWaitRejectsBlocked_REQ_AUT_002`, `TestWaitOutputMatchesLine_REQ_AUT_003` and `TestWaitTimeoutReportsLastState_REQ_AUT_004` green.

### [ ] T-F1-24 · Attention state and thread resume
- **What:** `attention_state` and `seen_at` on threads; `done` until a client focuses it; `thread.attention_changed` notification; restore threads after a restart with their history and `stopped` turns. The two columns are added by the `ALTER TABLE` statements of Data Model §2.5 inside migration **0005**, because 0001 created `threads` and is already applied (Art. 6). It was 0004 until delta `2026-09-restore-semantics` gave that number to the restore migration.
- **REQ:** REQ-AGT-016, REQ-AGT-017
- **Files:** `internal/agents/attention*.go`, `internal/store/migrations/0005_agent.sql`, `internal/store/**`
- **Depends on:** T-F1-13
- **Done:** `TestDoneUntilFocused_REQ_AGT_016` and `TestThreadsResumeAfterRestart_REQ_AGT_017` green.

### [ ] T-F1-25 · Integration surface
- **What:**
  - injection of `UMBRAL_*` variables with Umbral's values winning;
  - `pane.report_state` and `pane.release_state` with `source` and `seq`;
  - the rule that Umbral's own agent owns the panes of its threads.
- **REQ:** REQ-INT-001, REQ-INT-002, REQ-INT-003, REQ-INT-005, REQ-INT-006
- **Files:** `internal/integrations/**`, `internal/api/panes.go`, `cmd/umb/pane.go`
- **Depends on:** T-F1-13
- **Done:** tests `TestEnvInjectedAndAuthoritative_REQ_INT_001`, `TestExternalReportDrivesRollup_REQ_INT_002`, `TestStaleSeqIgnored_REQ_INT_003` and `TestReleaseRestoresOwnDetection_REQ_INT_005` and `TestOwnAgentCannotBeDisplaced_REQ_INT_006` green.

### [ ] T-F1-26 · Display metadata and tokens
- **What:** `pane.report_metadata` with normalization, 80-character cap, TTL, 16 keys per report and 32 per pane; strict separation from semantic state.
- **REQ:** REQ-INT-004
- **Files:** `internal/integrations/metadata*.go`
- **Depends on:** T-F1-25
- **Done:** `TestMetadataNeverChangesWaits_REQ_INT_004` green, including the limit and expiry cases.

### [ ] T-F1-27 · Notifications
- **What:** `notification.show` with normalization, delivery to the foreground client, typed reasons and a rate limit of 5 per source per 60 s.
- **REQ:** REQ-NTF-001, REQ-NTF-002
- **Files:** `internal/notify/**`, `internal/api/notifications.go`
- **Depends on:** T-F1-24
- **Done:** `TestNotificationNormalizesAndReports_REQ_NTF_001` and `TestNotificationRateLimited_REQ_NTF_002` green.

### [ ] T-F1-28 · Policy explain
- **What:** `policy.explain` returning the decision and the ordered trace of DD-006, read-only; `umb policy explain` on the CLI.
- **REQ:** REQ-SEC-009
- **Files:** `internal/security/explain*.go`, `cmd/umb/policy.go`
- **Depends on:** T-F1-03
- **Done:** `TestExplainNamesDecidingRule_REQ_SEC_009` green with one case per precedence step.

### [ ] T-F1-29 · Rule overrides and optional updates
- **What:** load redaction rules and destructive patterns from `$XDG_CONFIG_HOME/umbral/rules/`; precedence over the built-in ones; invalid files ignored with a warning; optional remote fetch disabled by default and logged in `egress_log`.
- **REQ:** REQ-SEC-010, REQ-SEC-011
- **Files:** `internal/security/rules/**`, `internal/config/**`
- **Depends on:** T-F1-04
- **Done:** `TestLocalRulesWin_REQ_SEC_010` and `TestRuleUpdateOptInAndLogged_REQ_SEC_011` green; with `rules_check = false` no request leaves the machine.

### [ ] T-F1-30 · Signed rule bundles, trust store and recovery
- **What:**
  - bundle format (rules + version + Ed25519 detached signature) and verification against `trust_keys`;
  - monotonic version check and rejection with a logged reason plus `rules.update_rejected`;
  - `rules.key.add|list|remove|rotate` with fingerprint confirmation and protection of the last key;
  - fail-closed after three failures or with no valid key;
  - `rules.rollback` and `rules.reset`, both offline and without needing a key;
  - `rules.status` reporting the active bundle, the previous one, the keys and the last rejection.
- **REQ:** REQ-SEC-011, REQ-SEC-013, REQ-SEC-014, REQ-SEC-015, REQ-SEC-016
- **Files:** `internal/security/rules/bundle*.go`, `internal/security/rules/trust*.go`, `internal/api/rules.go`, `cmd/umb/rules.go`
- **Depends on:** T-F1-29
- **Done:** tests `TestBundleRequiresValidSignature_REQ_SEC_011`, `TestRejectsUnknownKeyAndDowngrade_REQ_SEC_013`, `TestKeyLifecycleRequiresFingerprint_REQ_SEC_014`, `TestFailClosedAfterThreeFailures_REQ_SEC_015` and `TestRollbackAndResetWorkOffline_REQ_SEC_016` green; the recovery case (every key removed with `--force`, then `rules.reset`) is covered end to end.

### [ ] T-F1-31 · Wait monitoring and lifecycle
- **What:**
  - enforcement of the concurrency and rate limits with the responses from REQ-AUT-005;
  - `wait.list` with age and stalled flag;
  - `wait.cancel` that does not touch the observed turn;
  - stall detection per turn with a configurable window and the `thread.stalled` notification;
  - `umb wait ls` and `umb wait cancel` on the CLI.
- **REQ:** REQ-AUT-005, REQ-AUT-006, REQ-AUT-007, REQ-AUT-008
- **Files:** `internal/waits/**`, `internal/api/waits.go`, `cmd/umb/wait.go`
- **Depends on:** T-F1-23
- **Done:** tests `TestLimitsDegradeWithoutDisconnect_REQ_AUT_005`, `TestWaitListReportsAgeAndStalled_REQ_AUT_006`, `TestCancelLeavesTurnRunning_REQ_AUT_007` and `TestStalledTurnNotifiedNotKilled_REQ_AUT_008` green; a script that leaks 100 waits does not bring the connection down.

## Traceability matrix (F1)

| REQ | Tasks | Tests citing it |
|---|---|---|
| REQ-AGT-001 | T-F1-13, T-F1-21 | TestSendStreamsDeltas_REQ_AGT_001, e2e us003 |
| REQ-AGT-002 | T-F1-09 | TestToolSchemasDeclared_REQ_AGT_002 |
| REQ-AGT-003 | T-F1-10 | TestRunCommandCreatesAgentBlock_REQ_AGT_003 |
| REQ-AGT-004 | T-F1-14 | TestAskPausesTurn_REQ_AGT_004 |
| REQ-AGT-005 | T-F1-14 | TestDenyReturnsDeniedByUser_REQ_AGT_005 |
| REQ-AGT-006 | T-F1-15 | TestInvalidArgsRepairOnce_REQ_AGT_006 |
| REQ-AGT-007 | T-F1-10, T-F1-16 | TestCancelUnder500ms_REQ_AGT_007 |
| REQ-AGT-008 | T-F1-13 | TestStopsAtMaxSteps_REQ_AGT_008 |
| REQ-AGT-009 | T-F1-03, T-F1-13 | TestAskModeReadOnlyTools_REQ_AGT_009 |
| REQ-AGT-010 | T-F1-13 | TestModelSwitchNextTurn_REQ_AGT_010 |
| REQ-AGT-011 | T-F1-01, T-F1-13, T-F1-22 | TestPersistBeforeNotify_REQ_AGT_011, TestCrashRecovery_REQ_AGT_011 |
| REQ-AGT-013 | T-F1-03 | TestPolicyAutoEditWorkspace_REQ_AGT_013 |
| REQ-AGT-014 | T-F1-03 | TestPolicyNormalDefaultAsk_REQ_AGT_014 |
| REQ-CTX-001 | T-F1-11 | TestRulesFilesPrecedence_REQ_CTX_001 |
| REQ-CTX-002 | T-F1-11 | TestAttachments_REQ_CTX_002 |
| REQ-CTX-003 | T-F1-11 | TestGitContext_REQ_CTX_003 |
| REQ-CTX-004 | T-F1-12 | TestCompactionTriggeredOverWindow_REQ_CTX_004 |
| REQ-CTX-005 | T-F1-11 | TestAttachmentTruncated_REQ_CTX_005 |
| REQ-LLM-001 | T-F1-05, T-F1-06 | TestStreamNormalized_REQ_LLM_001 |
| REQ-LLM-002 | T-F1-05 | TestDiscoverModels_REQ_LLM_002 |
| REQ-LLM-003 | T-F1-07 | TestFallbackOn429_REQ_LLM_003 |
| REQ-LLM-004 | T-F1-07, T-F1-21 | TestOfflineRejectsRemote_REQ_LLM_004 |
| REQ-LLM-005 | T-F1-01, T-F1-07 | TestUsageRecorded_REQ_LLM_005 |
| REQ-LLM-006 | T-F1-06 | TestOllamaSendsNumCtx_REQ_LLM_006 |
| REQ-SEC-001 | T-F1-04, T-F1-07 | TestRedactionCorpus_REQ_SEC_001 |
| REQ-SEC-002 | T-F1-01, T-F1-07, T-F1-21 | TestEgressLoggedForRemote_REQ_SEC_002 |
| REQ-SEC-004 | T-F1-02 | TestPlaintextKeyRejected_REQ_SEC_004 |
| REQ-SEC-005 | T-F1-03, T-F1-14 | TestDestructiveAlwaysAsk_REQ_SEC_005 |
| REQ-SEC-006 | T-F1-03, T-F1-09 | TestTaintedRequiresAsk_REQ_SEC_006, TestFetchMarksTaint_REQ_SEC_006 |
| REQ-MCP-001 | T-F1-17 | TestMcpToolsPrefixed_REQ_MCP_001 |
| REQ-MCP-002 | T-F1-17 | TestMcpAddMidThread_REQ_MCP_002 |
| REQ-MCP-003 | T-F1-17 | TestMcpReconnectBackoff_REQ_MCP_003 |
| REQ-MCP-004 | T-F1-17 | TestMcpDefaultAsk_REQ_MCP_004 |
| REQ-CLI-001 | T-F1-19 | TestUmbAiPipesStdin_REQ_CLI_001 |
| REQ-TUI-001 | T-F1-20 (+ T-F0-12) | TestTUIAgentPanel_REQ_TUI_001 |
| REQ-TUI-002 | T-F1-20 | TestModeToggle_REQ_TUI_002 |
| REQ-TUI-003 | T-F1-20 | TestAttachBlock_REQ_TUI_003 |
| REQ-OBS-001 | T-F1-18 | TestTurnTraceHasSpans_REQ_OBS_001 |
| REQ-OBS-002 | T-F1-15 | TestInvalidMetricIncrements_REQ_OBS_002 |
| REQ-AUT-001 | T-F1-23 | TestWaitPinsTurn_REQ_AUT_001 |
| REQ-AUT-002 | T-F1-23 | TestSendWaitRejectsBlocked_REQ_AUT_002 |
| REQ-AUT-003 | T-F1-23 | TestWaitOutputMatchesLine_REQ_AUT_003 |
| REQ-AUT-004 | T-F1-23 | TestWaitTimeoutReportsLastState_REQ_AUT_004 |
| REQ-AGT-016 | T-F1-24 | TestDoneUntilFocused_REQ_AGT_016 |
| REQ-AGT-017 | T-F1-24 | TestThreadsResumeAfterRestart_REQ_AGT_017 |
| REQ-INT-001 | T-F1-25 | TestEnvInjectedAndAuthoritative_REQ_INT_001 |
| REQ-INT-002 | T-F1-25 | TestExternalReportDrivesRollup_REQ_INT_002 |
| REQ-INT-003 | T-F1-25 | TestStaleSeqIgnored_REQ_INT_003 |
| REQ-INT-004 | T-F1-26 | TestMetadataNeverChangesWaits_REQ_INT_004 |
| REQ-INT-005 | T-F1-25 | TestReleaseRestoresOwnDetection_REQ_INT_005 |
| REQ-NTF-001 | T-F1-27 | TestNotificationNormalizesAndReports_REQ_NTF_001 |
| REQ-NTF-002 | T-F1-27 | TestNotificationRateLimited_REQ_NTF_002 |
| REQ-SEC-009 | T-F1-28 | TestExplainNamesDecidingRule_REQ_SEC_009 |
| REQ-SEC-010 | T-F1-29 | TestLocalRulesWin_REQ_SEC_010 |
| REQ-SEC-011 | T-F1-29, T-F1-30 | TestBundleRequiresValidSignature_REQ_SEC_011 |
| REQ-SEC-012 | T-F1-02 | TestEnvFallbackOnlyWhenEnabled_REQ_SEC_012 |
| REQ-SEC-013 | T-F1-30 | TestRejectsUnknownKeyAndDowngrade_REQ_SEC_013 |
| REQ-SEC-014 | T-F1-30 | TestKeyLifecycleRequiresFingerprint_REQ_SEC_014 |
| REQ-SEC-015 | T-F1-30 | TestFailClosedAfterThreeFailures_REQ_SEC_015 |
| REQ-SEC-016 | T-F1-30 | TestRollbackAndResetWorkOffline_REQ_SEC_016 |
| REQ-SEC-008 | T-F1-02 | TestKeyringUnavailableDisablesProviders_REQ_SEC_008 |
| REQ-AGT-015 | T-F1-13 | TestSendIdempotentByClientMsgID_REQ_AGT_015 |
| REQ-AGT-018 | T-F1-09 | TestFetchUrlRefusesPrivateRanges_REQ_AGT_018 |
| REQ-INT-006 | T-F1-25 | TestOwnAgentCannotBeDisplaced_REQ_INT_006 |
| REQ-AUT-005 | T-F1-31 | TestLimitsDegradeWithoutDisconnect_REQ_AUT_005 |
| REQ-AUT-006 | T-F1-31 | TestWaitListReportsAgeAndStalled_REQ_AUT_006 |
| REQ-AUT-007 | T-F1-31 | TestCancelLeavesTurnRunning_REQ_AUT_007 |
| REQ-AUT-008 | T-F1-31 | TestStalledTurnNotifiedNotKilled_REQ_AUT_008 |
| REQ-OBS-004 | T-F1-18 | TestOrchestrationMetricsExposed_REQ_OBS_004 |

**SHOULD/COULD covered or deferred:**

| REQ | Status |
|---|---|
| REQ-AGT-012 | SHOULD, covered by T-F1-09 |
| REQ-LLM-007 | SHOULD, covered by T-F1-08 |
| REQ-OBS-003 | SHOULD, covered by T-F1-18 |
| REQ-LLM-008 | COULD, deferred to F2 |
| REQ-BLK-008 | SHOULD, deferred to F2 |

## Execution log

| Date | Tasks | Result | Notes |
|---|---|---|---|
| — | — | — | — |
