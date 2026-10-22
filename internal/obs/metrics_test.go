package obs

import (
	"sync"
	"testing"
)

// TestToolCallsInvalidCountsPerModel_REQ_OBS_002: umbral_tool_calls_invalid_total is a counter
// labelled by model, safe to increment from every turn's goroutine at once.
func TestToolCallsInvalidCountsPerModel_REQ_OBS_002(t *testing.T) {
	m := NewMetrics()
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() { m.ToolCallInvalid("ol/a") })
	}
	wg.Go(func() { m.ToolCallInvalid("ol/b") })
	wg.Wait()

	got := m.ToolCallsInvalid()
	if got["ol/a"] != 50 || got["ol/b"] != 1 || len(got) != 2 {
		t.Fatalf("counted %v, want ol/a 50 and ol/b 1", got)
	}
	got["ol/a"] = 0
	if m.ToolCallsInvalid()["ol/a"] != 50 {
		t.Fatal("the snapshot shares the counter's map")
	}
}
