// Package obs holds the daemon's metrics (Tech Design §7.2). The counters live here, in
// memory; exporting them through OpenTelemetry is T-F1-18's.
package obs

import (
	"maps"
	"sync"
)

// Metrics are the daemon's counters. The zero value is not usable; use NewMetrics.
type Metrics struct {
	mu      sync.Mutex
	invalid map[string]uint64 // model → umbral_tool_calls_invalid_total
}

// NewMetrics returns counters that start at zero.
func NewMetrics() *Metrics {
	return &Metrics{invalid: map[string]uint64{}}
}

// ToolCallInvalid counts one invalid tool call made by model (REQ-OBS-002). It implements the
// agents module's Metrics port.
func (m *Metrics) ToolCallInvalid(model string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.invalid[model]++
}

// ToolCallsInvalid is umbral_tool_calls_invalid_total by model, as a copy.
func (m *Metrics) ToolCallsInvalid() map[string]uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return maps.Clone(m.invalid)
}
