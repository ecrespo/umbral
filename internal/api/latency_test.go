package api

import (
	"encoding/base64"
	"slices"
	"testing"
	"time"
)

// BenchmarkOutputLatency_REQ_TERM_006 measures what the requirement measures: the latency
// the daemon itself adds between a PTY chunk being published and the corresponding
// notification reaching the client's socket.
//
// It is a benchmark so T-F0-13 can gate on it, and it reports p95 as its own metric rather
// than relying on ns/op, because REQ-TERM-006 is a percentile and an average would hide
// exactly the tail the budget exists to bound.
func BenchmarkOutputLatency_REQ_TERM_006(b *testing.B) {
	sessions := newFakeSessions()
	s := benchServer(b, sessions)
	c := benchClient(b, s)

	chunk := []byte("the quick brown fox jumps over the lazy dog\r\n")
	latencies := make([]time.Duration, 0, b.N)

	b.ResetTimer()
	var seq uint64
	for b.Loop() {
		seq++
		start := time.Now()
		s.dispatchTestOutput(fakeSessionID, seq, chunk)
		if !c.awaitOutput(b, 5*time.Second) {
			b.Fatalf("no notification arrived for seq %d", seq)
		}
		latencies = append(latencies, time.Since(start))
	}
	b.StopTimer()

	if len(latencies) == 0 {
		b.Skip("no samples")
	}
	slices.Sort(latencies)
	p95 := latencies[int(float64(len(latencies))*0.95)]
	if p95 >= time.Duration(len(latencies)) {
		p95 = latencies[len(latencies)-1]
	}
	p50 := latencies[len(latencies)/2]

	b.ReportMetric(float64(p95.Microseconds()), "p95_us")
	b.ReportMetric(float64(p50.Microseconds()), "p50_us")
	b.ReportMetric(float64(latencies[len(latencies)-1].Microseconds()), "max_us")

	if p95 > 5*time.Millisecond {
		b.Errorf("added latency p95 = %v over %d samples, want under 5ms (REQ-TERM-006)", p95, len(latencies))
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

	slices.Sort(latencies)
	p95 := latencies[int(float64(samples)*0.95)]
	t.Logf("added latency: p50 %v, p95 %v, max %v over %d samples",
		latencies[samples/2], p95, latencies[samples-1], samples)

	if p95 > 5*time.Millisecond {
		t.Errorf("added latency p95 = %v, want under 5ms (REQ-TERM-006)", p95)
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
