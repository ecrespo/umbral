//go:build live

package ollama

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/llmgw/domain"
)

// TestOllamaLive runs the adapter against a real Ollama (`go test -tags live`): discovery
// finds gpt-oss:20b, and a streamed call answers with text, usage and done. UMBRAL_OLLAMA_URL
// overrides the default endpoint.
func TestOllamaLive(t *testing.T) {
	base := os.Getenv("UMBRAL_OLLAMA_URL")
	if base == "" {
		base = "http://127.0.0.1:11434"
	}
	p, err := New(Config{ID: "ollama", BaseURL: base, Options: map[string]any{"num_ctx": int64(8192), "keep_alive": "5m"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	models, err := p.Models(ctx)
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	found := false
	for _, m := range models {
		found = found || m.ID == "ollama/gpt-oss:20b"
	}
	if !found {
		t.Fatalf("gpt-oss:20b is not pulled here (%d models)", len(models))
	}

	stream, err := p.Stream(ctx, domain.Request{
		Model: "gpt-oss:20b", Reasoning: "low", MaxOutputTokens: 256,
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "Reply with the single word: pong"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	var usage domain.Usage
	done := false
	for ev, err := range stream {
		if err != nil {
			t.Fatal(err)
		}
		switch ev.Kind {
		case domain.EventTextDelta:
			text.WriteString(ev.Text)
		case domain.EventUsage:
			usage = ev.Usage
		case domain.EventDone:
			done = true
		}
	}
	if !done || usage.InputTokens == 0 || usage.OutputTokens == 0 || !strings.Contains(strings.ToLower(text.String()), "pong") {
		t.Errorf("text %q, usage %+v, done %v", text.String(), usage, done)
	}
}
