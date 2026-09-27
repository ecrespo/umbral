package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type fakeModels struct {
	items     []Model
	refreshes []bool
}

func (f *fakeModels) List(_ context.Context, refresh bool) ([]Model, error) {
	f.refreshes = append(f.refreshes, refresh)
	return f.items, nil
}

// TestModelListReachesTheWire_REQ_LLM_002: model.list answers API Spec §4's Model with the
// provider's reason when a model is not ok, passes `refresh` through, and is open to `umb`
// (§2's cli row names it).
func TestModelListReachesTheWire_REQ_LLM_002(t *testing.T) {
	t.Parallel()

	svc := &fakeModels{items: []Model{
		{ID: "ollama/qwen3:4b", Provider: "ollama", Local: true, Caps: ModelCaps{Tools: true, ContextWindow: 32768}, Health: "ok"},
		{ID: "hf/openai/gpt-oss-120b", Provider: "hf", Health: "down", Reason: "keyring_unavailable"},
	}}
	s := testServerWithConfig(t, Config{Models: svc})
	c := dial(t, s)
	if resp := c.hello(s.Token(), ClientCLI); resp.Error != nil {
		t.Fatalf("handshake: %+v", resp.Error)
	}

	resp := c.call(2, "model.list", nil)
	if resp.Error != nil {
		t.Fatalf("model.list from umb: %+v", resp.Error)
	}
	raw, _ := json.Marshal(resp.Result)
	for _, want := range []string{
		`"id":"ollama/qwen3:4b"`, `"context_window":32768`, `"health":"ok"`,
		`"reason":"keyring_unavailable"`, `"price_in_micro_usd_per_mtok":0`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("model.list = %s, want %s", raw, want)
		}
	}
	if strings.Contains(string(raw), `"health":"ok","reason"`) {
		t.Errorf("an ok model carries a reason: %s", raw)
	}

	_ = c.call(3, "model.list", map[string]any{"refresh": true})
	if len(svc.refreshes) != 2 || svc.refreshes[0] || !svc.refreshes[1] {
		t.Errorf("refresh passed as %v, want [false true]", svc.refreshes)
	}
	if resp := c.call(4, "model.list", []int{1}); resp.Error == nil || resp.Error.Code != codeValidationError {
		t.Errorf("model.list with an array = %+v, want VALIDATION_ERROR", resp.Error)
	}

	svc.items = nil
	resp = c.call(5, "model.list", nil)
	if raw, _ := json.Marshal(resp.Result); string(raw) != `{"items":[]}` {
		t.Errorf("an empty catalog = %s, want {\"items\":[]}", raw)
	}
}

// TestModelListWithoutAGatewayIsNotImplemented: a daemon with no catalog wired says so rather
// than pretending there are no models.
func TestModelListWithoutAGatewayIsNotImplemented(t *testing.T) {
	t.Parallel()

	s := testServerWithConfig(t, Config{})
	c := dial(t, s)
	if resp := c.hello(s.Token(), ClientTUI); resp.Error != nil {
		t.Fatalf("handshake: %+v", resp.Error)
	}
	if resp := c.call(2, "model.list", nil); resp.Error == nil || resp.Error.Code != codeNotImplemented {
		t.Errorf("model.list = %+v, want NOT_IMPLEMENTED", resp.Error)
	}
}
