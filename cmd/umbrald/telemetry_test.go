package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// otlpCollector is an OTLP/HTTP receiver that keeps the spans it is sent.
type otlpCollector struct {
	mu    sync.Mutex
	spans []*tracepb.Span
}

func (c *otlpCollector) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		if r.URL.Path == "/v1/traces" {
			var req collectortrace.ExportTraceServiceRequest
			if err := proto.Unmarshal(raw, &req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			c.mu.Lock()
			for _, rs := range req.GetResourceSpans() {
				for _, ss := range rs.GetScopeSpans() {
					c.spans = append(c.spans, ss.GetSpans()...)
				}
			}
			c.mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// spanAttrs flattens a span's attributes to strings.
func spanAttrs(s *tracepb.Span) map[string]string {
	out := map[string]string{}
	for _, kv := range s.GetAttributes() {
		switch v := kv.GetValue().GetValue().(type) {
		case *commonpb.AnyValue_StringValue:
			out[kv.GetKey()] = v.StringValue
		case *commonpb.AnyValue_IntValue:
			out[kv.GetKey()] = strconv.FormatInt(v.IntValue, 10)
		}
	}
	return out
}

// TestTurnTraceHasSpans_REQ_OBS_001: a real daemon with `[otel] endpoint` set exports each
// turn as one trace to the collector there: an `agent.turn` root, an `llm.call` per model call
// with the GenAI attributes for model, provider and tokens, and a `tool.read_file` span, all
// in the turn's trace (REQ-OBS-003's export with them). A turn that fails logs its failure
// with the trace id of the trace the collector holds for it (Art. 7).
func TestTurnTraceHasSpans_REQ_OBS_001(t *testing.T) {
	ollama := &scriptedOllama{replies: [][]string{
		{`{"model":"m","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"read_file","arguments":{"path":"notes.txt"}}}]},"done":true,"done_reason":"stop","prompt_eval_count":40,"eval_count":8}`},
		{`{"model":"m","message":{"role":"assistant","content":"They say hello."},"done":true,"done_reason":"stop","prompt_eval_count":60,"eval_count":5}`},
		// The second turn's model call gets nothing back: the turn fails and says so in the log.
	}}
	srv := ollama.server(t)
	collector := &otlpCollector{}
	otel := collector.server(t)
	logs := &lockedBuffer{}
	stream, ctx, stop := startAgentDaemon(t, srv.URL, daemonOpts{
		settings: "[otel]\nendpoint = \"" + otel.URL + "\"\n",
		logs:     logs,
	})

	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, "notes.txt"), "hello\n")
	var thread struct{ ID string }
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": cwd}, &thread); err != nil {
		t.Fatal(err)
	}
	ended := func() string {
		for {
			select {
			case n, ok := <-stream.Notifications():
				if !ok {
					t.Fatalf("the stream ended: %v", stream.Err())
				}
				if n.Method == "thread.turn_finished" {
					var f struct {
						StopReason string `json:"stop_reason"`
					}
					_ = n.Decode(&f)
					return f.StopReason
				}
			case <-ctx.Done():
				t.Fatal("the turn did not finish")
			}
		}
	}
	for i, want := range []string{"end_turn", "provider_error"} {
		if err := stream.Call(ctx, "thread.send", map[string]any{"thread_id": thread.ID, "text": "what do the notes say?"}, nil); err != nil {
			t.Fatal(err)
		}
		if got := ended(); got != want {
			t.Fatalf("turn %d ended %s, want %s", i+1, got, want)
		}
	}
	// The daemon flushes what it has not sent yet on its way out.
	stop(os.Interrupt)

	collector.mu.Lock()
	spans := append([]*tracepb.Span(nil), collector.spans...)
	collector.mu.Unlock()
	var roots []*tracepb.Span
	for _, s := range spans {
		if s.GetName() == "agent.turn" {
			roots = append(roots, s)
		}
	}
	if len(roots) != 2 {
		t.Fatalf("%d agent.turn spans exported, want 2; got %d spans", len(roots), len(spans))
	}
	first, failed := roots[0], roots[1]
	if spanAttrs(first)["umbral.turn.stop_reason"] != "end_turn" {
		first, failed = failed, first
	}
	if len(first.GetParentSpanId()) != 0 || spanAttrs(first)["gen_ai.conversation.id"] != thread.ID {
		t.Fatalf("the turn's span is not a root of the thread: %v", spanAttrs(first))
	}

	var calls, tools []*tracepb.Span
	for _, s := range spans {
		if !bytes.Equal(s.GetTraceId(), first.GetTraceId()) {
			continue
		}
		switch s.GetName() {
		case "llm.call":
			calls = append(calls, s)
		case "tool.read_file":
			tools = append(tools, s)
		}
		if s != first && hex.EncodeToString(s.GetParentSpanId()) != hex.EncodeToString(first.GetSpanId()) {
			t.Errorf("%s is not a child of the turn", s.GetName())
		}
	}
	if len(calls) != 2 || len(tools) != 1 {
		t.Fatalf("the first turn's trace has %d llm.call and %d tool.read_file spans, want 2 and 1", len(calls), len(tools))
	}
	inTokens := map[string]bool{}
	for _, c := range calls {
		a := spanAttrs(c)
		if a["gen_ai.system"] != "ollama" || a["gen_ai.request.model"] != "m" || a["umbral.usage.status"] != "ok" {
			t.Errorf("llm.call attributes %v", a)
		}
		inTokens[a["gen_ai.usage.input_tokens"]] = true
	}
	if !inTokens["40"] || !inTokens["60"] {
		t.Errorf("the model calls' input tokens are %v, want 40 and 60", inTokens)
	}
	if a := spanAttrs(tools[0]); a["umbral.tool.status"] != "ok" || a["umbral.tool.risk"] != "ReadOnly" {
		t.Errorf("tool.read_file attributes %v", a)
	}

	// The failed turn's log line names the trace the collector holds for it.
	want := hex.EncodeToString(failed.GetTraceId())
	found := false
	sc := bufio.NewScanner(strings.NewReader(logs.String()))
	for sc.Scan() {
		var line map[string]any
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		if line["msg"] == "a turn stopped: the model call failed" {
			found = true
			if line["trace_id"] != want {
				t.Errorf("the failure was logged with trace_id %v, want %s", line["trace_id"], want)
			}
		}
	}
	if !found {
		t.Fatalf("the failed turn logged nothing:\n%s", logs.String())
	}
}
