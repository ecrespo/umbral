package obs

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// scope names the instrumentation in every span and metric the daemon emits.
const scope = "github.com/ecrespo/umbral"

// metricInterval is how often metrics go to the collector when one is configured.
const metricInterval = 30 * time.Second

// TelemetryConfig is what the daemon's traces and metrics are built from.
type TelemetryConfig struct {
	// Endpoint is `[otel] endpoint`, an OTLP/HTTP collector's base URL. Empty exports
	// nothing (Art. 4); the config package has already checked that it is local.
	Endpoint string
	// Version is the daemon's, as the resource's service.version.
	Version string
	// Logger receives the exporter's own failures, which must not stop a turn.
	Logger *slog.Logger
	// Metrics are the counters the modules increment. Required.
	Metrics *Metrics
	// WaitsActive reads umbral_waits_active from the wait engine; nil exports nothing.
	WaitsActive func() int64
	// FramesRefused reads the run's frame refusals (REQ-OBS-005); nil exports nothing.
	FramesRefused func() (in, out uint64)

	// spanExporter and metricReader replace the OTLP ones in tests.
	spanExporter sdktrace.SpanExporter
	metricReader sdkmetric.Reader
}

// Telemetry is the daemon's OpenTelemetry: one trace per agent turn (REQ-OBS-001, Art. 7),
// the metrics of Tech §7.2 (REQ-OBS-002, REQ-OBS-004), and their OTLP export when an endpoint
// is configured (REQ-OBS-003).
//
// Spans are made with or without an endpoint: the trace id is what joins a turn's log lines,
// and that is worth having on a machine that exports nothing.
type Telemetry struct {
	tp     *sdktrace.TracerProvider
	mp     *sdkmetric.MeterProvider
	tracer trace.Tracer
	export bool

	tokens     metric.Int64Counter
	firstToken metric.Float64Histogram
}

// NewTelemetry builds the providers, and the OTLP exporters when cfg.Endpoint is set.
func NewTelemetry(ctx context.Context, cfg TelemetryConfig) (*Telemetry, error) {
	if cfg.Metrics == nil {
		return nil, errors.New("obs: telemetry needs its metrics")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	res := resource.NewSchemaless(
		attribute.String("service.name", "umbrald"),
		attribute.String("service.version", cfg.Version),
	)

	spans, reader := cfg.spanExporter, cfg.metricReader
	export := false
	if cfg.Endpoint != "" {
		var err error
		if spans, reader, err = otlp(ctx, cfg.Endpoint); err != nil {
			return nil, err
		}
		export = true
		// The exporter's failures — a collector that is down — are the daemon's warnings,
		// not lines on its stderr in another format.
		logger := cfg.Logger
		otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
			logger.Warn("telemetry export failed", slog.Any("error", err))
		}))
	}

	topts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res), sdktrace.WithSampler(sdktrace.AlwaysSample())}
	switch {
	case export:
		topts = append(topts, sdktrace.WithBatcher(spans))
	case spans != nil:
		topts = append(topts, sdktrace.WithSyncer(spans))
	}
	mopts := []sdkmetric.Option{sdkmetric.WithResource(res)}
	if reader != nil {
		mopts = append(mopts, sdkmetric.WithReader(reader))
	}
	t := &Telemetry{
		tp:     sdktrace.NewTracerProvider(topts...),
		mp:     sdkmetric.NewMeterProvider(mopts...),
		export: export,
	}
	t.tracer = t.tp.Tracer(scope)
	if err := t.instruments(cfg); err != nil {
		return nil, errors.Join(err, t.Shutdown(ctx))
	}
	return t, nil
}

// otlp builds the two OTLP/HTTP exporters for a collector's base URL.
//
// Every setting is given here, because the exporters read OTEL_EXPORTER_OTLP_* first and an
// option only replaces what it names: the URL, scheme and path, the headers, the TLS
// configuration, the compression and the timeout all come from `[otel] endpoint` and this
// function, never from the environment. Neither exporter goes through a proxy: the endpoint
// is local, and a proxy from the environment would send the telemetry somewhere the user did
// not name.
func otlp(ctx context.Context, endpoint string) (sdktrace.SpanExporter, sdkmetric.Reader, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, nil, fmt.Errorf("obs: otel endpoint: %w", err)
	}
	noProxy := func(*http.Request) (*url.URL, error) { return nil, nil }
	// nil for http clears any certificate the environment named, which the exporter would
	// refuse on a plain-text endpoint.
	var tlsCfg *tls.Config
	if u.Scheme == "https" {
		tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	const timeout = 10 * time.Second

	spans, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(endpoint+"/v1/traces"),
		otlptracehttp.WithHeaders(map[string]string{}),
		otlptracehttp.WithTLSClientConfig(tlsCfg),
		otlptracehttp.WithCompression(otlptracehttp.NoCompression),
		otlptracehttp.WithTimeout(timeout),
		otlptracehttp.WithProxy(noProxy),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("obs: trace exporter: %w", err)
	}
	metrics, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithEndpointURL(endpoint+"/v1/metrics"),
		otlpmetrichttp.WithHeaders(map[string]string{}),
		otlpmetrichttp.WithTLSClientConfig(tlsCfg),
		otlpmetrichttp.WithCompression(otlpmetrichttp.NoCompression),
		otlpmetrichttp.WithTimeout(timeout),
		otlpmetrichttp.WithProxy(noProxy),
	)
	if err != nil {
		return nil, nil, errors.Join(fmt.Errorf("obs: metric exporter: %w", err), spans.Shutdown(ctx))
	}
	return spans, sdkmetric.NewPeriodicReader(metrics, sdkmetric.WithInterval(metricInterval)), nil
}

// exporting reports whether anything leaves the process.
func (t *Telemetry) exporting() bool { return t.export }

// Shutdown flushes what is pending to the collector and stops both providers.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	return errors.Join(t.tp.Shutdown(ctx), t.mp.Shutdown(ctx))
}

// TurnEnd is how a turn ended, for its span.
type TurnEnd struct {
	StopReason string
	// Model is the catalog id that served the turn's last model call.
	Model                             string
	InTokens, OutTokens, CostMicroUSD int64
}

// Turn starts a turn's span, `agent.turn`, as the root of a trace of its own. The context it
// returns carries it, so the turn's model calls, tools and log lines join it; the function
// ends it.
func (t *Telemetry) Turn(ctx context.Context, threadID, turnID string) (context.Context, func(TurnEnd)) {
	ctx, span := t.tracer.Start(ctx, "agent.turn", trace.WithNewRoot(), trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "invoke_agent"),
			attribute.String("gen_ai.conversation.id", threadID),
			attribute.String("umbral.turn.id", turnID),
		))
	return ctx, func(end TurnEnd) {
		span.SetAttributes(
			attribute.String("umbral.turn.stop_reason", end.StopReason),
			attribute.Int64("gen_ai.usage.input_tokens", end.InTokens),
			attribute.Int64("gen_ai.usage.output_tokens", end.OutTokens),
			attribute.Int64("umbral.usage.cost_micro_usd", end.CostMicroUSD),
		)
		if end.Model != "" {
			span.SetAttributes(attribute.String("umbral.model", end.Model))
		}
		if failedStop(end.StopReason) {
			span.SetStatus(codes.Error, end.StopReason)
		}
		span.End()
	}
}

// failedStop says which stop reasons are failures. A cancel, a limit reached or a finished
// answer is how a turn is meant to be able to end.
func failedStop(reason string) bool {
	switch reason {
	case "provider_error", "tool_error", "storage_error", "context_overflow":
		return true
	}
	return false
}

// ToolCall is the tool call a span is for.
type ToolCall struct {
	ID, Name string
}

// ToolEnd is how a tool call ended. Risk is known once the call is classified, so it comes
// with the end; it is empty for a call that never was.
type ToolEnd struct {
	Status, Risk string
}

// Tool starts a tool's span, `tool.<name>`, inside the turn ctx carries; the function ends it
// with the call's status and risk.
func (t *Telemetry) Tool(ctx context.Context, call ToolCall) (context.Context, func(ToolEnd)) {
	ctx, span := t.tracer.Start(ctx, "tool."+call.Name, trace.WithAttributes(
		attribute.String("gen_ai.operation.name", "execute_tool"),
		attribute.String("gen_ai.tool.name", call.Name),
		attribute.String("gen_ai.tool.call.id", call.ID),
	))
	return ctx, func(end ToolEnd) {
		span.SetAttributes(attribute.String("umbral.tool.status", end.Status))
		if end.Risk != "" {
			span.SetAttributes(attribute.String("umbral.tool.risk", end.Risk))
		}
		if end.Status == "error" || end.Status == "invalid_args" {
			span.SetStatus(codes.Error, end.Status)
		}
		span.End()
	}
}

// ModelCall is one call to one model, as the router records it in `usage`: a fallback is a
// second call.
type ModelCall struct {
	// Provider is the provider entry's id; Model the model's name at that provider.
	Provider, Model string
	// Status is the usage row's: ok, error, rate_limited or timeout.
	Status                            string
	InTokens, OutTokens, CostMicroUSD int64
	FirstTokenMS                      *int64
	Start, End                        time.Time
}

// ModelCall records a finished model call as an `llm.call` span inside the turn ctx carries,
// with the GenAI attributes, timed from the call's own start and end (REQ-OBS-001); and its
// tokens and first-token time as metrics. It never carries the error's text: a provider's
// message can echo what was sent.
func (t *Telemetry) ModelCall(ctx context.Context, call ModelCall) {
	_, span := t.tracer.Start(ctx, "llm.call", trace.WithTimestamp(call.Start), trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "chat"),
			attribute.String("gen_ai.system", call.Provider),
			attribute.String("gen_ai.request.model", call.Model),
			attribute.Int64("gen_ai.usage.input_tokens", call.InTokens),
			attribute.Int64("gen_ai.usage.output_tokens", call.OutTokens),
			attribute.Int64("umbral.usage.cost_micro_usd", call.CostMicroUSD),
			attribute.String("umbral.usage.status", call.Status),
		))
	if call.FirstTokenMS != nil {
		span.SetAttributes(attribute.Int64("umbral.first_token_ms", *call.FirstTokenMS))
	}
	if call.Status != "ok" {
		span.SetStatus(codes.Error, call.Status)
	}
	span.End(trace.WithTimestamp(call.End))

	model := attribute.String("model", call.Provider+"/"+call.Model)
	t.tokens.Add(ctx, call.InTokens, metric.WithAttributes(model, attribute.String("direction", "input")))
	t.tokens.Add(ctx, call.OutTokens, metric.WithAttributes(model, attribute.String("direction", "output")))
	if call.FirstTokenMS != nil {
		t.firstToken.Record(ctx, float64(*call.FirstTokenMS)/1000, metric.WithAttributes(
			attribute.String("provider", call.Provider), model))
	}
}
