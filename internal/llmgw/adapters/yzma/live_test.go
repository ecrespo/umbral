//go:build live

package yzma

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/llmgw/domain"
)

// TestLiveYzmaAnswersFromAGGUF_REQ_LLM_009 loads a real GGUF through llama.cpp in process
// (`go test -tags live`): discovery lists it, and a streamed call answers with text, usage
// and done. UMBRAL_YZMA_LIB is llama.cpp's library directory (`task deps:llama` installs
// it) and UMBRAL_YZMA_GGUF a small chat model; the test is skipped without the model.
func TestLiveYzmaAnswersFromAGGUF_REQ_LLM_009(t *testing.T) {
	lib := os.Getenv("UMBRAL_YZMA_LIB")
	if lib == "" {
		home, _ := os.UserHomeDir()
		lib = filepath.Join(home, ".local", "llama-cpp", "b11899", "llama-b11899")
	}
	gguf := os.Getenv("UMBRAL_YZMA_GGUF")
	if gguf == "" {
		t.Skip("UMBRAL_YZMA_GGUF names no model")
	}
	dir := t.TempDir()
	if err := os.Symlink(gguf, filepath.Join(dir, "small.gguf")); err != nil {
		t.Fatal(err)
	}
	p, err := New(Config{ID: "embedded", LibPath: lib, ModelsDir: dir, Options: map[string]any{"temperature": 0.0}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	models, err := p.Models(ctx)
	if err != nil || len(models) != 1 || models[0].ID != "embedded/small" {
		t.Fatalf("discovery: %v %v", models, err)
	}
	start := time.Now()
	stream, err := p.Stream(ctx, domain.Request{
		Model: "small", System: "Answer with one short line.", MaxOutputTokens: 48,
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "Reply with the single word: pong"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	var usage domain.Usage
	done := ""
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
			done = ev.FinishReason
		}
	}
	t.Logf("%s in %v: %q (%d in, %d out)", done, time.Since(start), text.String(), usage.InputTokens, usage.OutputTokens)
	if !strings.Contains(strings.ToLower(text.String()), "pong") || usage.InputTokens == 0 || usage.OutputTokens == 0 || done == "" {
		t.Errorf("answer %q, usage %+v, done %q", text.String(), usage, done)
	}
}
