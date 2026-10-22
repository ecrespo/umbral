# Delta — where the spans are made, what they carry, and where they go

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-10-03` — approved by E. Crespo as written, decisions 4 and 6 with their proposed options. Folded into Tech Design 1.36, API 1.28 and the F1 tasks file |
| **Date** | 2026-10-03 |
| **Task** | T-F1-18 |
| **Raised by** | T-F1-18: REQ-OBS-001…004, Art. 4 and Art. 7, and Tech §7, which leave these points open |

## Evidence

- **REQ-OBS-001** asks for one trace per turn, "one span per model call and one per tool", with
  the GenAI attributes. It does not say:
  - which module makes the spans, given that §5.2 lets only `cmd/*` import `obs`;
  - what a "model call" is when the router falls back.
- **REQ-OBS-003** says "WHERE `otel.endpoint` is configured" and stops there. It does not
  address Art. 4: "every request that leaves the machine SHALL … be recorded in `egress_log`",
  and "no remote telemetry unless the user enables it explicitly".
- **REQ-OBS-004** asks the system to "expose" four metrics. Three of them count things no task
  has built yet:
  - a stalled turn and a rate-limited report: T-F1-31;
  - a rejected rule bundle: T-F1-30.
- **Tech §7.1** puts `trace_id` and `span_id` in every log line. Most lines are logged outside
  any turn.
- **Tech §7.2** lists six metrics; the task names only one of them.
- **API §1:** an `INTERNAL_ERROR` carries the `connection_id` in `trace_id` "until tracing
  exists (T-F1-18)".
- **T-F1-18's Done** asks for `TestTurnTraceHasSpans_REQ_OBS_001` with an in-memory exporter.

## Decisions

1. **The modules trace through ports of their own; `obs` implements them.**
   - The agent runtime has `agents/ports.Tracer`:
     - `Turn` opens a turn's span;
     - `Tool` opens a span for each tool call.
   - The router has `llmgw/ports.Tracer` with one method, `ModelCall`.
   - Both are required: a daemon wired without one does not start, as with `Metrics`.
   - `cmd/umbrald` adapts them to `internal/obs`. `obs` is the only package that imports
     OpenTelemetry, and only `cmd/*` imports `obs`; Tech §5.2's `obs` row already says so.
   - The span lives in the context each method returns. That context is how a turn's model
     calls, tools and log lines join its trace.
   - *Alternative:* every module imports the OpenTelemetry API directly. Every service would
     then depend on a vendor library that §5.2 keeps out of them.
2. **One `llm.call` per call the router makes, the calls `usage` records.**
   - A fallback's failed attempts are calls too: 429, 5xx, a timeout, a stream that ends
     empty.
   - A candidate the adapter refuses to carry (`ErrUnsupported`) is not a call. REQ-LLM-005
     does not count it either.
   - The span is recorded when the call ends, with the start and end times of the call
     itself.
   - **Attributes:**
     - `gen_ai.operation.name = chat`;
     - `gen_ai.system`, the provider entry's id;
     - `gen_ai.request.model`, the model's name at the provider;
     - `gen_ai.usage.input_tokens` and `gen_ai.usage.output_tokens`;
     - `umbral.usage.status`, `umbral.usage.cost_micro_usd` and `umbral.first_token_ms`.
   - A status other than `ok` marks the span as an error.
   - **No error text:** a provider's message can echo what was sent (Art. 7).
3. **`agent.turn` is a root; `tool.<name>` is its child.**
   - Each turn starts a new trace, whatever span the context it runs on holds.
   - **`agent.turn` attributes:**
     - `gen_ai.operation.name = invoke_agent`;
     - `gen_ai.conversation.id`, the thread;
     - `umbral.turn.id`;
     - `umbral.turn.stop_reason`, set at the end;
     - the turn's tokens and cost;
     - `umbral.model`.
   - The `provider_error`, `tool_error`, `storage_error` and `context_overflow` stop reasons
     are errors. A cancel, a limit reached and a finished answer are not.
   - **`tool.<name>` attributes:**
     - `gen_ai.operation.name = execute_tool`;
     - `gen_ai.tool.name` and `gen_ai.tool.call.id`;
     - `umbral.tool.status` and `umbral.tool.risk`. The risk comes at the end, because it is
       known only once the call is classified.
   - A tool span that ends with `error` or `invalid_args` is an error.
   - **A tool the registry does not have is `tool.unknown`,** with `gen_ai.tool.name = unknown`.
     Its name is text the model invented, and putting that in a span name or a label would be
     unbounded.
   - A call that stops before it records a status ends as `error`, so no span is left open.
4. **Spans are always made; they are exported only to a local collector.**
   - The spans exist with or without an endpoint: their trace ids join a turn's log lines.
   - `[otel] endpoint` in `config.toml` must be an `http://` or `https://` base URL:
     - with no path, query, fragment or credentials;
     - whose host is `localhost` or a loopback address.
   - Anything else is `ErrSettingsInvalid`, and the daemon refuses to start, as for the other
     keys. The message never repeats the value.
   - The exporters use OTLP/HTTP with protobuf.
   - **The environment changes nothing.** The OpenTelemetry exporters read the
     `OTEL_EXPORTER_OTLP_*` variables first, so the daemon sets every option itself:
     - the URL comes from `[otel] endpoint`, with `/v1/traces` and `/v1/metrics`;
     - the scheme decides TLS: TLS 1.2 or later for `https`, none for `http`;
     - no headers;
     - no compression;
     - a 10 s timeout;
     - no proxy.
   - So none of those variables can redirect the export, add a header or change its TLS.
     Without `[otel] endpoint` no exporter exists for them to configure.
   - Traces are batched; metrics go out every 30 s. Shutdown flushes both, bounded at 5 s.
   - An export failure is a `warn` line and does not affect the turn.
   - `config.reload` does not apply the key; it needs a restart, as `pane_history` does. So
     `config.get` reports the endpoint the daemon started with, not the one a reloaded file
     names.
   - With no endpoint, nothing leaves the process.
   - *Why local only:* a remote collector would make each export an egress that Art. 4
     requires to be redacted and recorded in `egress_log`. A local collector forwards wherever
     the user configures it to.
   - *Alternative:* remote endpoints, with a row in `egress_log` for each export request.
     Deferred.
5. **Logs.**
   - A line logged with a turn's context carries `trace_id` and `span_id`. That covers the
     runtime's lines about a turn and the router's line when a `usage` write fails.
   - A line logged outside any turn carries neither: there is no trace for it to name.
   - Tech §7.1's field list applies to the lines that have a trace.
6. **Metrics, and what "expose" means.**
   - **Exported** through the same OTLP endpoint:
     - `umbral_tool_calls_invalid_total{model}`;
     - `umbral_frames_refused_total{direction}`;
     - `umbral_waits_active`, a gauge read live from the wait engine;
     - `umbral_waits_stalled_total`;
     - `umbral_reports_rate_limited_total`;
     - `umbral_rule_updates_rejected_total{reason}`;
     - `umbral_llm_tokens_total{direction, model}`;
     - `umbral_llm_first_token_seconds{provider, model}`.
   - **Counters with nothing to count yet.** `obs.Metrics` gains `WaitStalled`,
     `ReportRateLimited` and `RuleUpdateRejected(reason)`. They stay at zero until T-F1-31 and
     T-F1-30 call them, and those tasks' Done lines say so.
   - **Not exported yet:** `umbral_session_output_latency_seconds` and
     `umbral_approvals_total`. No REQ asks for them and no task owns them, so Tech §7.2 says
     so.
   - "Expose" means exported to the configured collector. There is no local scrape endpoint and
     no API method.
   - *Alternative:* a `metrics` object in `system.status`, readable with no collector at all.
7. **`INTERNAL_ERROR`'s `trace_id` keeps the `connection_id`.**
   - Requests are not traced; only turns are, and a turn's failure is a stop reason, never an
     `INTERNAL_ERROR`.
   - API §1's "until tracing exists" becomes "when the failing work ran outside a turn", which
     today is every case.
8. **`config.get`'s `settings` gains `otel_endpoint?`.** It is absent when nothing is
   exported (API §5.28).
9. **The tests.**
   - `TestTurnTraceHasSpans_REQ_OBS_001` runs a real daemon, with `[otel] endpoint` pointing at
     an OTLP collector served on loopback. It checks:
     - the trace's shape;
     - the attributes;
     - the failed turn's log line carrying the trace id the collector holds.
   - The in-memory exporter tests are in `internal/obs`: `TestATurnIsOneTraceWithItsCalls`
     and the rest.
   - REQ-OBS-003 gets `TestTracesExportThroughOTLP_REQ_OBS_003`.

## What changes in the specs on ratification

- **Tech Design:**
  - §5.1, Configuration: `[otel] endpoint`;
  - §7.1: decision 5;
  - §7.2: decision 6, with each metric's labels and the two left unexported;
  - §7.3: decisions 1–3.
- **API:**
  - §1: decision 7;
  - §5.28: decision 8.
- **`specs/tasks/umbral-f1-tasks.md`:**
  - T-F1-18's Files:
    - `internal/obs/**`;
    - the two `Tracer` ports;
    - `waits.Service.Active`;
    - `config.Settings.OTelEndpoint`;
    - `api.Server.Frames` and `internal/api/config.go`;
    - `cmd/umbrald/telemetry.go`;
  - T-F1-18's Done, after decision 9;
  - T-F1-30's and T-F1-31's Done: the counters of decision 6;
  - the matrix:
    - REQ-OBS-001 adds `TestTheRuntimeTracesEachTurn_REQ_OBS_001` and
      `TestEveryModelCallIsTraced_REQ_OBS_001`;
    - REQ-OBS-003 gets `TestTracesExportThroughOTLP_REQ_OBS_003`;
    - REQ-OBS-004 adds `TestActiveCountsTheOpenWaits_REQ_OBS_004`.
