package api

import (
	"encoding/base64"
	"os"
	"slices"
	"testing"
	"time"
)

// outputLatencyBudget is what REQ-TERM-006 allows the daemon to add between a PTY chunk
// and the notification reaching a subscribed client.
const outputLatencyBudget = 5 * time.Millisecond

// perfProbeEnv names the environment variable scripts/perf_selftest.sh sets to inject an
// artificial regression into the timed region.
//
// It exists for the same reason scripts/arch_selftest.sh does: a budget check that stopped
// checking would pass silently forever, and nothing else in the suite would notice. The
// selftest sets it, asserts the benchmark turns red, and unsets it.
const perfProbeEnv = "UMBRAL_PERF_PROBE_DELAY"

// perfProbe reports the injected regression, or zero when there is none. A malformed value
// is a fatal setup error rather than a silent zero: a selftest whose injection quietly did
// nothing would report the gate as broken when it is fine, or as fine when it is broken.
func perfProbe(tb testing.TB) time.Duration {
	tb.Helper()

	raw := os.Getenv(perfProbeEnv)
	if raw == "" {
		return 0
	}
	delay, err := time.ParseDuration(raw)
	if err != nil {
		tb.Fatalf("%s=%q is not a duration: %v", perfProbeEnv, raw, err)
	}
	if delay > 0 {
		tb.Logf("artificial regression injected: %v per sample (%s)", delay, perfProbeEnv)
	}
	return delay
}

// percentile returns the sample at the given fraction. REQ-TERM-001 and REQ-TERM-006 are
// both written as percentiles, and the mean of a path that is usually fast and
// occasionally slow describes neither half.
func percentile(samples []time.Duration, fraction float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	sorted := slices.Clone(samples)
	slices.Sort(sorted)
	return sorted[int(float64(len(sorted)-1)*fraction)]
}

// BenchmarkOutputLatency_REQ_TERM_006 measures what the requirement measures: the latency
// the daemon itself adds between a PTY chunk being published and the corresponding
// notification reaching the client's socket.
//
// It is a benchmark so T-F0-13 can gate on it, and it reports p95 as its own metric rather
// than relying on ns/op, because REQ-TERM-006 is a percentile and an average would hide
// exactly the tail the budget exists to bound.
func BenchmarkOutputLatency_REQ_TERM_006(b *testing.B) {
	probe := perfProbe(b)
	sessions := newFakeSessions()
	s := benchServer(b, sessions)
	c := benchClient(b, s)

	chunk := []byte("the quick brown fox jumps over the lazy dog\r\n")
	var latencies []time.Duration

	b.ResetTimer()
	var seq uint64
	for b.Loop() {
		seq++
		start := time.Now()
		s.dispatchTestOutput(fakeSessionID, seq, chunk)
		if !c.awaitOutput(b, 5*time.Second) {
			b.Fatalf("no notification arrived for seq %d", seq)
		}
		if probe > 0 {
			time.Sleep(probe)
		}
		latencies = append(latencies, time.Since(start))
	}
	b.StopTimer()

	if len(latencies) == 0 {
		b.Skip("no samples")
	}
	p95 := percentile(latencies, 0.95)

	b.ReportMetric(float64(p95.Microseconds()), "p95_us")
	b.ReportMetric(float64(percentile(latencies, 0.50).Microseconds()), "p50_us")
	b.ReportMetric(float64(percentile(latencies, 1.0).Microseconds()), "max_us")

	if p95 > outputLatencyBudget {
		b.Errorf("added latency p95 = %v over %d samples, want under %v (REQ-TERM-006)",
			p95, len(latencies), outputLatencyBudget)
	}
}

// TestOutputLatencyUnder5ms_REQ_TERM_006 is the same measurement as a test, so a
// regression fails `task test` rather than waiting for someone to run the benchmarks.
func TestOutputLatencyUnder5ms_REQ_TERM_006(t *testing.T) {
	t.Parallel()

	sessions := newFakeSessions()
	s := testServerWithSessions(t, sessions)
	c := dial(t, s)
	if resp := c.hello(s.Token(), ClientTUI); resp.Error != nil {
		t.Fatalf("handshake: %+v", resp.Error)
	}
	if resp := c.call(2, "session.subscribe", map[string]any{"session_id": fakeSessionID}); resp.Error != nil {
		t.Fatalf("subscribe: %+v", resp.Error)
	}

	const samples = 200
	chunk := []byte("the quick brown fox jumps over the lazy dog\r\n")
	latencies := make([]time.Duration, 0, samples)

	for seq := uint64(1); seq <= samples; seq++ {
		start := time.Now()
		s.dispatchTestOutput(fakeSessionID, seq, chunk)
		if !c.awaitOutputT(t, 5*time.Second) {
			t.Fatalf("no notification arrived for seq %d", seq)
		}
		latencies = append(latencies, time.Since(start))
	}

	p95 := percentile(latencies, 0.95)
	t.Logf("added latency: p50 %v, p95 %v, max %v over %d samples",
		percentile(latencies, 0.50), p95, percentile(latencies, 1.0), samples)

	if p95 > outputLatencyBudget {
		t.Errorf("added latency p95 = %v, want under %v (REQ-TERM-006)", p95, outputLatencyBudget)
	}
}

// awaitOutputT waits for one session.output notification.
func (c *client) awaitOutputT(t *testing.T, within time.Duration) bool {
	t.Helper()
	return c.awaitOutputUntil(within, func(format string, args ...any) { t.Fatalf(format, args...) })
}

func (c *client) awaitOutput(b *testing.B, within time.Duration) bool {
	b.Helper()
	return c.awaitOutputUntil(within, func(format string, args ...any) { b.Fatalf(format, args...) })
}

func (c *client) awaitOutputUntil(within time.Duration, fail func(string, ...any)) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if err := c.conn.SetReadDeadline(deadline); err != nil {
			fail("set read deadline: %v", err)
			return false
		}
		var msg struct {
			Method string `json:"method"`
			Params struct {
				DataB64 string `json:"data_b64"`
			} `json:"params"`
		}
		if err := c.dec.Decode(&msg); err != nil {
			return false
		}
		if msg.Method != "session.output" {
			continue
		}
		if _, err := base64.StdEncoding.DecodeString(msg.Params.DataB64); err != nil {
			fail("notification data is not base64: %v", err)
			return false
		}
		return true
	}
	return false
}
