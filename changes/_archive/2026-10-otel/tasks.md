# Tasks — delta `2026-10-otel`

### [x] 2026-10-03 T-F1-18 · OTel observability
- **What:** decisions 1–9 in `internal/obs/**`, `agents/ports.Tracer`, `llmgw/ports.Tracer`,
  `waits.Service.Active`, `config.Settings.OTelEndpoint`, `api.Server.Frames`, `config.get`'s
  `otel_endpoint` and `cmd/umbrald/telemetry.go`.
- **REQ:** REQ-OBS-001, REQ-OBS-002, REQ-OBS-003, REQ-OBS-004

### [x] 2026-10-03 Ratification
- The Tech Lead ratifies decisions 1–9 — 4 (local collector only) and 6 ("expose" = OTLP) are
  the two with an alternative — and the delta is folded into the Tech Design, the API and the
  F1 tasks file, and moved to `changes/_archive/`.
