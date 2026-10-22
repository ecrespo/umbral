package obs

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

// newTestTelemetry is telemetry whose spans land in memory and whose metrics are read on
// demand.
func newTestTelemetry(t *testing.T, cfg TelemetryConfig) (*Telemetry, *tracetest.InMemoryExporter, *sdkmetric.ManualReader) {
	t.Helper()
	spans := tracetest.NewInMemoryExporter()
	reader := sdkmetric.NewManualReader()
	cfg.spanExporter, cfg.metricReader = spans, reader
	if cfg.Metrics == nil {
		cfg.Metrics = NewMetrics()
	}
	tel, err := NewTelemetry(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	return tel, spans, reader
}

func attrs(kv []attribute.KeyValue) map[string]attribute.Value {
	out := map[string]attribute.Value{}
	for _, a := range kv {
		out[string(a.Key)] = a.Value
	}
	return out
}

// runTurn is the shape of a turn as the runtime and the router drive it: the turn's span,
// a model call inside it, a tool inside it, a second model call, and the turn's end.
func runTurn(tel *Telemetry) {
	ctx, endTurn := tel.Turn(context.Background(), "thr_1", "trn_1")
	start := time.Now()
	ft := int64(40)
	tel.ModelCall(ctx, ModelCall{
		Provider: "ollama", Model: "gpt-oss:20b", Status: "ok", InTokens: 120, OutTokens: 30,
		FirstTokenMS: &ft, Start: start, End: start.Add(time.Second),
	})
	_, endTool := tel.Tool(ctx, ToolCall{ID: "tc_1", Name: "read_file"})
	endTool(ToolEnd{Status: "ok", Risk: "ReadOnly"})
	tel.ModelCall(ctx, ModelCall{
		Provider: "ollama", Model: "gpt-oss:20b", Status: "error", Start: start, End: start.Add(2 * time.Second),
	})
	endTurn(TurnEnd{StopReason: "provider_error", Model: "ollama/gpt-oss:20b", InTokens: 120, OutTokens: 30})
}

// TestATurnIsOneTraceWithItsCalls: a turn is one trace, `agent.turn` its root, with an
// `llm.call` per model call carrying the GenAI attributes — model, provider, tokens — and a
// `tool.<name>` per tool, each a child of the turn (REQ-OBS-001, Tech §7.3).
func TestATurnIsOneTraceWithItsCalls(t *testing.T) {
	tel, spans, _ := newTestTelemetry(t, TelemetryConfig{})
	runTurn(tel)

	got := spans.GetSpans()
	if len(got) != 4 {
		t.Fatalf("%d spans, want the turn, two model calls and a tool", len(got))
	}
	byName := map[string][]tracetest.SpanStub{}
	for _, s := range got {
		byName[s.Name] = append(byName[s.Name], s)
	}
	turn := byName["agent.turn"]
	if len(turn) != 1 || turn[0].Parent.IsValid() {
		t.Fatalf("agent.turn spans %+v, want one root", turn)
	}
	root := turn[0].SpanContext
	ta := attrs(turn[0].Attributes)
	if ta["gen_ai.conversation.id"].AsString() != "thr_1" || ta["umbral.turn.id"].AsString() != "trn_1" ||
		ta["umbral.turn.stop_reason"].AsString() != "provider_error" || ta["gen_ai.usage.input_tokens"].AsInt64() != 120 {
		t.Errorf("agent.turn attributes %v", ta)
	}
	if turn[0].Status.Code != codes.Error {
		t.Errorf("a turn that ended in provider_error has status %v", turn[0].Status)
	}

	calls := byName["llm.call"]
	if len(calls) != 2 {
		t.Fatalf("%d llm.call spans, want 2", len(calls))
	}
	for _, c := range calls {
		if c.Parent.SpanID() != root.SpanID() || c.SpanContext.TraceID() != root.TraceID() {
			t.Errorf("llm.call is not a child of the turn: parent %v", c.Parent)
		}
	}
	ca := attrs(calls[0].Attributes)
	if ca["gen_ai.system"].AsString() != "ollama" || ca["gen_ai.request.model"].AsString() != "gpt-oss:20b" ||
		ca["gen_ai.usage.input_tokens"].AsInt64() != 120 || ca["gen_ai.usage.output_tokens"].AsInt64() != 30 ||
		ca["gen_ai.operation.name"].AsString() != "chat" {
		t.Errorf("llm.call attributes %v", ca)
	}
	if calls[0].EndTime.Sub(calls[0].StartTime) != time.Second {
		t.Errorf("llm.call lasted %v, want the call's own second", calls[0].EndTime.Sub(calls[0].StartTime))
	}
	if calls[1].Status.Code != codes.Error {
		t.Errorf("a failed model call has status %v", calls[1].Status)
	}

	tool := byName["tool.read_file"]
	if len(tool) != 1 || tool[0].Parent.SpanID() != root.SpanID() {
		t.Fatalf("tool spans %+v", tool)
	}
	tla := attrs(tool[0].Attributes)
	if tla["gen_ai.tool.name"].AsString() != "read_file" || tla["gen_ai.tool.call.id"].AsString() != "tc_1" ||
		tla["umbral.tool.status"].AsString() != "ok" || tla["umbral.tool.risk"].AsString() != "ReadOnly" {
		t.Errorf("tool attributes %v", tla)
	}
}

// TestATurnStartsItsOwnTrace: a turn is a root even when the context it starts from already
// holds a span — the daemon's, or an earlier turn's.
func TestATurnStartsItsOwnTrace(t *testing.T) {
	tel, spans, _ := newTestTelemetry(t, TelemetryConfig{})
	ctx, end1 := tel.Turn(context.Background(), "thr_1", "trn_1")
	_, end2 := tel.Turn(ctx, "thr_1", "trn_2")
	end2(TurnEnd{StopReason: "end_turn"})
	end1(TurnEnd{StopReason: "end_turn"})
	got := spans.GetSpans()
	if len(got) != 2 || got[0].Parent.IsValid() || got[1].Parent.IsValid() || got[0].SpanContext.TraceID() == got[1].SpanContext.TraceID() {
		t.Fatalf("two turns: %+v", got)
	}
}

// TestLogsCarryTheTraceID: a log line written inside a turn carries the turn's trace and
// span ids, so a trace and its logs can be joined (Art. 7); one written outside has none.
func TestLogsCarryTheTraceID(t *testing.T) {
	tel, spans, _ := newTestTelemetry(t, TelemetryConfig{})
	var buf bytes.Buffer
	logger := slog.New(LogHandler(slog.NewJSONHandler(&buf, nil))).With("module", "agents")

	ctx, end := tel.Turn(context.Background(), "thr_1", "trn_1")
	logger.WarnContext(ctx, "inside")
	end(TurnEnd{StopReason: "end_turn"})
	logger.InfoContext(context.Background(), "outside")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("log lines %q", lines)
	}
	var inside, outside map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &inside); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &outside); err != nil {
		t.Fatal(err)
	}
	sc := spans.GetSpans()[0].SpanContext
	if inside["trace_id"] != sc.TraceID().String() || inside["span_id"] != sc.SpanID().String() || inside["module"] != "agents" {
		t.Errorf("inside the turn: %v, want trace %s span %s", inside, sc.TraceID(), sc.SpanID())
	}
	if _, ok := outside["trace_id"]; ok {
		t.Errorf("outside any turn the line has a trace_id: %v", outside)
	}
}

// collect reads every metric once, by name.
func collect(t *testing.T, reader *sdkmetric.ManualReader) map[string]metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

// sumOf is a sum metric's value at the point whose attributes include want.
func sumOf(t *testing.T, m metricdata.Metrics, want ...attribute.KeyValue) int64 {
	t.Helper()
	var points []metricdata.DataPoint[int64]
	switch d := m.Data.(type) {
	case metricdata.Sum[int64]:
		points = d.DataPoints
	case metricdata.Gauge[int64]:
		points = d.DataPoints
	default:
		t.Fatalf("%s is %T", m.Name, m.Data)
	}
	for _, p := range points {
		ok := true
		for _, kv := range want {
			if v, found := p.Attributes.Value(kv.Key); !found || v != kv.Value {
				ok = false
			}
		}
		if ok {
			return p.Value
		}
	}
	t.Fatalf("%s has no point with %v: %+v", m.Name, want, points)
	return 0
}

// TestInvalidCallsExported_REQ_OBS_002: umbral_tool_calls_invalid_total, counted in memory
// since T-F1-15, is exported through OpenTelemetry as a monotonic counter labelled by model.
func TestInvalidCallsExported_REQ_OBS_002(t *testing.T) {
	metrics := NewMetrics()
	_, _, reader := newTestTelemetry(t, TelemetryConfig{Metrics: metrics})
	metrics.ToolCallInvalid("ollama/a")
	metrics.ToolCallInvalid("ollama/a")
	metrics.ToolCallInvalid("ollama/b")

	m, ok := collect(t, reader)["umbral_tool_calls_invalid_total"]
	if !ok {
		t.Fatal("umbral_tool_calls_invalid_total is not exported")
	}
	if s, ok := m.Data.(metricdata.Sum[int64]); !ok || !s.IsMonotonic {
		t.Fatalf("umbral_tool_calls_invalid_total is %T, want a monotonic sum", m.Data)
	}
	if a, b := sumOf(t, m, attribute.String("model", "ollama/a")), sumOf(t, m, attribute.String("model", "ollama/b")); a != 2 || b != 1 {
		t.Fatalf("by model: ollama/a %d, ollama/b %d", a, b)
	}
}

// TestOrchestrationMetricsExposed_REQ_OBS_004: the four orchestration metrics are exposed —
// the active waits read live from the wait engine, the three counters labelled by reason
// where one applies — and each one moves with what it counts.
func TestOrchestrationMetricsExposed_REQ_OBS_004(t *testing.T) {
	metrics := NewMetrics()
	var mu sync.Mutex
	active := int64(3)
	_, _, reader := newTestTelemetry(t, TelemetryConfig{
		Metrics: metrics,
		WaitsActive: func() int64 {
			mu.Lock()
			defer mu.Unlock()
			return active
		},
	})

	got := collect(t, reader)
	for _, name := range []string{"umbral_waits_active", "umbral_waits_stalled_total", "umbral_reports_rate_limited_total"} {
		if _, ok := got[name]; !ok {
			t.Errorf("%s is not exposed before anything happened", name)
		}
	}
	if v := sumOf(t, got["umbral_waits_active"]); v != 3 {
		t.Errorf("umbral_waits_active = %d, want 3", v)
	}

	mu.Lock()
	active = 1
	mu.Unlock()
	metrics.WaitStalled()
	metrics.ReportRateLimited()
	metrics.ReportRateLimited()
	metrics.RuleUpdateRejected("bad_signature")
	metrics.RuleUpdateRejected("downgrade")
	metrics.RuleUpdateRejected("downgrade")

	got = collect(t, reader)
	if v := sumOf(t, got["umbral_waits_active"]); v != 1 {
		t.Errorf("umbral_waits_active = %d, want 1", v)
	}
	if v := sumOf(t, got["umbral_waits_stalled_total"]); v != 1 {
		t.Errorf("umbral_waits_stalled_total = %d", v)
	}
	if v := sumOf(t, got["umbral_reports_rate_limited_total"]); v != 2 {
		t.Errorf("umbral_reports_rate_limited_total = %d", v)
	}
	rules := got["umbral_rule_updates_rejected_total"]
	if a, b := sumOf(t, rules, attribute.String("reason", "bad_signature")), sumOf(t, rules, attribute.String("reason", "downgrade")); a != 1 || b != 2 {
		t.Errorf("umbral_rule_updates_rejected_total by reason: bad_signature %d, downgrade %d", a, b)
	}
}

// TestFramesAndModelCallsAreExported: the frame refusals REQ-OBS-005 counts, and the tokens
// and first-token time of each model call (Tech §7.2), go out with the rest.
func TestFramesAndModelCallsAreExported(t *testing.T) {
	tel, _, reader := newTestTelemetry(t, TelemetryConfig{
		FramesRefused: func() (in, out uint64) { return 1, 4 },
	})
	runTurn(tel)

	got := collect(t, reader)
	frames := got["umbral_frames_refused_total"]
	if in, out := sumOf(t, frames, attribute.String("direction", "in")), sumOf(t, frames, attribute.String("direction", "out")); in != 1 || out != 4 {
		t.Errorf("umbral_frames_refused_total: in %d, out %d", in, out)
	}
	tokens := got["umbral_llm_tokens_total"]
	model := attribute.String("model", "ollama/gpt-oss:20b")
	if in, out := sumOf(t, tokens, model, attribute.String("direction", "input")), sumOf(t, tokens, model, attribute.String("direction", "output")); in != 120 || out != 30 {
		t.Errorf("umbral_llm_tokens_total: input %d, output %d", in, out)
	}
	h, ok := got["umbral_llm_first_token_seconds"].Data.(metricdata.Histogram[float64])
	if !ok || len(h.DataPoints) != 1 || h.DataPoints[0].Count != 1 || h.DataPoints[0].Sum != 0.04 {
		t.Errorf("umbral_llm_first_token_seconds = %+v, want one call at 0.04 s", got["umbral_llm_first_token_seconds"].Data)
	}
}

// collector is an OTLP/HTTP receiver: it keeps every span name it is sent.
type collector struct {
	mu      sync.Mutex
	paths   []string
	names   []string
	headers []http.Header
}

func (c *collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body := io.Reader(r.Body)
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body = gz
	}
	raw, err := io.ReadAll(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paths = append(c.paths, r.URL.Path)
	c.headers = append(c.headers, r.Header.Clone())
	if r.URL.Path == "/v1/traces" {
		var req collectortrace.ExportTraceServiceRequest
		if err := proto.Unmarshal(raw, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for _, rs := range req.GetResourceSpans() {
			for _, ss := range rs.GetScopeSpans() {
				for _, s := range ss.GetSpans() {
					c.names = append(c.names, s.GetName())
				}
			}
		}
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
}

// TestTracesExportThroughOTLP_REQ_OBS_003: with an endpoint configured, the turn's spans and
// the metrics reach an OTLP/HTTP collector there, at the latest when the daemon shuts down.
func TestTracesExportThroughOTLP_REQ_OBS_003(t *testing.T) {
	c := &collector{}
	srv := httptest.NewServer(c)
	defer srv.Close()

	tel, err := NewTelemetry(t.Context(), TelemetryConfig{Endpoint: srv.URL, Metrics: NewMetrics()})
	if err != nil {
		t.Fatal(err)
	}
	runTurn(tel)
	if err := tel.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	names := strings.Join(c.names, ",")
	for _, want := range []string{"agent.turn", "llm.call", "tool.read_file"} {
		if !strings.Contains(names, want) {
			t.Errorf("the collector got spans %q, without %s", names, want)
		}
	}
	if paths := strings.Join(c.paths, ","); !strings.Contains(paths, "/v1/metrics") {
		t.Errorf("the collector got %q, no metrics", paths)
	}
}

// TestNoEndpointSendsNothing: without an endpoint the spans are still made — the logs carry
// their ids — but nothing is exported (Art. 4: no remote telemetry unless the user enables
// it).
func TestNoEndpointSendsNothing(t *testing.T) {
	tel, err := NewTelemetry(t.Context(), TelemetryConfig{Metrics: NewMetrics()})
	if err != nil {
		t.Fatal(err)
	}
	if tel.exporting() {
		t.Fatal("telemetry with no endpoint has an exporter")
	}
	ctx, end := tel.Turn(context.Background(), "thr_1", "trn_1")
	var buf bytes.Buffer
	slog.New(LogHandler(slog.NewJSONHandler(&buf, nil))).InfoContext(ctx, "x")
	end(TurnEnd{StopReason: "end_turn"})
	if !strings.Contains(buf.String(), `"trace_id"`) {
		t.Fatalf("no trace_id without an endpoint: %s", buf.String())
	}
	if err := tel.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestTheEnvironmentCannotRedirectTheExport: what `[otel] endpoint` says is all the exporters
// use. The OTEL_EXPORTER_OTLP_* variables the OpenTelemetry exporters read — another path,
// another scheme, headers — change nothing: the daemon's telemetry goes where its config file
// says and carries nothing the environment adds (Art. 4).
func TestTheEnvironmentCannotRedirectTheExport(t *testing.T) {
	c := &collector{}
	srv := httptest.NewServer(c)
	defer srv.Close()
	for k, v := range map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT":         "http://192.0.2.1:4318",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT":  srv.URL + "/elsewhere",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT": srv.URL + "/elsewhere",
		"OTEL_EXPORTER_OTLP_HEADERS":          "x-from-env=leak",
		"OTEL_EXPORTER_OTLP_COMPRESSION":      "gzip",
		"OTEL_EXPORTER_OTLP_CERTIFICATE":      selfSignedCA(t),
		"OTEL_EXPORTER_OTLP_INSECURE":         "false",
	} {
		t.Setenv(k, v)
	}

	tel, err := NewTelemetry(t.Context(), TelemetryConfig{Endpoint: srv.URL, Metrics: NewMetrics()})
	if err != nil {
		t.Fatal(err)
	}
	runTurn(tel)
	if err := tel.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if paths := strings.Join(c.paths, ","); !strings.Contains(paths, "/v1/traces") || !strings.Contains(paths, "/v1/metrics") || strings.Contains(paths, "elsewhere") {
		t.Errorf("the collector was sent %q", paths)
	}
	for _, h := range c.headers {
		if h.Get("X-From-Env") != "" || h.Get("Content-Encoding") == "gzip" {
			t.Errorf("a request carried what the environment set: %v", h)
		}
	}
}

// selfSignedCA writes a CA certificate the exporters can load, and returns its path.
func selfSignedCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
