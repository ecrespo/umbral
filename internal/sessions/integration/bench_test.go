package integration_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/sessions"
	"github.com/ecrespo/umbral/internal/sessions/adapters/ghostty"
	"github.com/ecrespo/umbral/internal/sessions/adapters/pty"
	"github.com/ecrespo/umbral/internal/sessions/adapters/shellinteg"
	"github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/store"
)

// sessionCreateBudget is the ceiling REQ-TERM-001 puts on `session.create`.
const sessionCreateBudget = 300 * time.Millisecond

// perfProbeEnv names the environment variable scripts/perf_selftest.sh sets to inject an
// artificial regression into the timed region. It is duplicated in internal/api's
// latency_test.go because Go test helpers do not cross a package boundary; the two are
// kept in step by the selftest, which asserts that both benchmarks turn red.
const perfProbeEnv = "UMBRAL_PERF_PROBE_DELAY"

// perfProbe reports the injected regression, or zero when there is none. See the twin in
// internal/api for why a malformed value is fatal rather than ignored.
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

// percentile returns the sample at the given fraction.
func percentile(samples []time.Duration, fraction float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	sorted := slices.Clone(samples)
	slices.Sort(sorted)
	return sorted[int(float64(len(sorted)-1)*fraction)]
}

// BenchmarkSessionCreate_REQ_TERM_001 is the CI gate for the 300 ms budget (T-F0-13).
//
// It measures the service call and not the round trip, and the gap is worth naming rather
// than glossing over. REQ-TERM-001 is written from the client's side — "WHEN an
// authenticated client invokes `session.create` … and reply with a `session_id`" — so the
// handshake, the JSON-RPC decode, the reply marshal and the socket write all sit inside
// the requirement and outside this benchmark, and nothing else bounds them. REQ-TERM-006
// cannot be borrowed for the job: it bounds the notification fan-out, which is a different
// path with a different shape.
//
// What is measured is where the time actually goes — the fork, the PTY and the SQLite
// insert — with `handleSessionCreate` around it being an unmarshal, this call and a
// marshal. The margin is wide enough that the untimed part cannot plausibly close it, but
// "cannot plausibly" is an argument and not a measurement, so the scope is recorded as a
// decision in T-F0-13's Result instead of being left for a reader to infer from here.
//
// It reports p95 rather than relying on ns/op because the requirement is a percentile, and
// because a fork that is usually fast and occasionally slow has a mean that describes
// neither case.
func BenchmarkSessionCreate_REQ_TERM_001(b *testing.B) {
	probe := perfProbe(b)

	shell, err := exec.LookPath("bash")
	if err != nil {
		b.Skipf("bash is not installed: %v", err)
	}

	db, err := store.Open(b.Context(), store.Options{Path: filepath.Join(b.TempDir(), "umbral.db")})
	if err != nil {
		b.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	eventBus := bus.New()
	defer eventBus.Close()

	service, err := sessions.New(sessions.Config{
		Store: db, Bus: eventBus, NewPTY: pty.Open, NewEmu: ghostty.NewEmulator,
		Bootstrap: shellinteg.Adapter{},
	})
	if err != nil {
		b.Fatalf("sessions.New: %v", err)
	}
	defer service.Shutdown()

	cwd := b.TempDir()
	var samples []time.Duration

	b.ResetTimer()
	for b.Loop() {
		started := time.Now()
		session, err := service.Create(b.Context(), domain.CreateParams{
			Shell: shell, CWD: cwd, Size: testSize, ShellIntegration: true,
		})
		if probe > 0 {
			time.Sleep(probe)
		}
		elapsed := time.Since(started)
		if err != nil {
			b.Fatalf("Create: %v", err)
		}
		samples = append(samples, elapsed)

		// Closing is teardown, not part of what the requirement bounds.
		b.StopTimer()
		_ = service.Close(b.Context(), session.ID)
		b.StartTimer()
	}
	b.StopTimer()

	if len(samples) == 0 {
		b.Skip("no samples")
	}
	p95 := percentile(samples, 0.95)
	b.ReportMetric(float64(p95.Microseconds())/1000, "p95_ms")
	b.ReportMetric(float64(percentile(samples, 0.50).Microseconds())/1000, "p50_ms")
	b.ReportMetric(float64(percentile(samples, 1.0).Microseconds())/1000, "max_ms")

	if p95 > sessionCreateBudget {
		b.Errorf("session.create p95 = %v over %d samples, want under %v (REQ-TERM-001)",
			p95, len(samples), sessionCreateBudget)
	}
}
