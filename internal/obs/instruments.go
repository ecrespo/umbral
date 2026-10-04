package obs

import (
	"context"
	"errors"
	"math"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// instruments registers Tech §7.2's metrics on the meter provider. The counters the modules
// keep in Metrics are observed when a reader collects, so incrementing one stays a map write
// under a mutex and needs no OpenTelemetry in the module that counts.
func (t *Telemetry) instruments(cfg TelemetryConfig) error {
	meter := t.mp.Meter(scope)
	var errs []error
	observable := func(name, desc string, cb metric.Int64Callback) {
		_, err := meter.Int64ObservableCounter(name, metric.WithDescription(desc), metric.WithInt64Callback(cb))
		errs = append(errs, err)
	}

	observable("umbral_tool_calls_invalid_total", "Tool calls whose tool or arguments did not validate, by model (REQ-OBS-002).",
		func(_ context.Context, o metric.Int64Observer) error {
			for model, n := range cfg.Metrics.snapshot().invalid {
				o.Observe(clampInt64(n), metric.WithAttributes(attribute.String("model", model)))
			}
			return nil
		})
	observable("umbral_waits_stalled_total", "Turns marked stalled (REQ-AUT-008).",
		func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(clampInt64(cfg.Metrics.snapshot().stalled))
			return nil
		})
	observable("umbral_reports_rate_limited_total", "Reports discarded for exceeding their source's rate (REQ-AUT-005).",
		func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(clampInt64(cfg.Metrics.snapshot().reportsLimited))
			return nil
		})
	observable("umbral_rule_updates_rejected_total", "Rule bundles discarded, by reason (REQ-SEC-013).",
		func(_ context.Context, o metric.Int64Observer) error {
			for reason, n := range cfg.Metrics.snapshot().rulesRejected {
				o.Observe(clampInt64(n), metric.WithAttributes(attribute.String("reason", reason)))
			}
			return nil
		})
	if cfg.FramesRefused != nil {
		observable("umbral_frames_refused_total", "Frames over the frame limit, by direction (REQ-OBS-005).",
			func(_ context.Context, o metric.Int64Observer) error {
				in, out := cfg.FramesRefused()
				o.Observe(clampInt64(in), metric.WithAttributes(attribute.String("direction", "in")))
				o.Observe(clampInt64(out), metric.WithAttributes(attribute.String("direction", "out")))
				return nil
			})
	}
	if cfg.WaitsActive != nil {
		_, err := meter.Int64ObservableGauge("umbral_waits_active", metric.WithDescription("Waits open now (REQ-OBS-004)."),
			metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
				o.Observe(cfg.WaitsActive())
				return nil
			}))
		errs = append(errs, err)
	}

	var err error
	t.tokens, err = meter.Int64Counter("umbral_llm_tokens_total", metric.WithDescription("Model tokens, by direction and model."))
	errs = append(errs, err)
	t.firstToken, err = meter.Float64Histogram("umbral_llm_first_token_seconds",
		metric.WithDescription("Time to a model call's first event, by provider and model."), metric.WithUnit("s"))
	errs = append(errs, err)
	return errors.Join(errs...)
}

// clampInt64 is a counter's value as OpenTelemetry's int64; one that big is not reached.
func clampInt64(n uint64) int64 {
	if n > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(n)
}
