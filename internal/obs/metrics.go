// Package obs is the daemon's observability (Tech Design §7): the counters the modules
// increment through ports of their own, and the OpenTelemetry that traces each agent turn
// and exports those counters (T-F1-18). Nothing but `cmd/*` imports it.
package obs

import (
	"maps"
	"sync"
)

// Metrics are the daemon's counters. The zero value is not usable; use NewMetrics.
type Metrics struct {
	mu             sync.Mutex
	invalid        map[string]uint64 // model → umbral_tool_calls_invalid_total
	stalled        uint64            // umbral_waits_stalled_total
	reportsLimited uint64            // umbral_reports_rate_limited_total
	rulesRejected  map[string]uint64 // reason → umbral_rule_updates_rejected_total
}

// NewMetrics returns counters that start at zero.
func NewMetrics() *Metrics {
	return &Metrics{invalid: map[string]uint64{}, rulesRejected: map[string]uint64{}}
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

// WaitStalled counts a turn marked stalled (REQ-AUT-008, REQ-OBS-004). T-F1-31 calls it.
func (m *Metrics) WaitStalled() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stalled++
}

// ReportRateLimited counts a report discarded for exceeding its source's rate (REQ-AUT-005,
// REQ-OBS-004). T-F1-31 calls it.
func (m *Metrics) ReportRateLimited() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reportsLimited++
}

// RuleUpdateRejected counts a rule bundle discarded, by why (REQ-SEC-013, REQ-OBS-004).
// T-F1-30 calls it.
func (m *Metrics) RuleUpdateRejected(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rulesRejected[reason]++
}

// snapshot is every counter at one moment, for the exporter.
type snapshot struct {
	invalid, rulesRejected  map[string]uint64
	stalled, reportsLimited uint64
}

func (m *Metrics) snapshot() snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return snapshot{
		invalid: maps.Clone(m.invalid), rulesRejected: maps.Clone(m.rulesRejected),
		stalled: m.stalled, reportsLimited: m.reportsLimited,
	}
}
