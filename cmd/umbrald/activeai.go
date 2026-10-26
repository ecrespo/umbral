package main

import (
	"context"
	"iter"
	"log/slog"
	"strings"

	"github.com/ecrespo/umbral/internal/activeai"
	aaidomain "github.com/ecrespo/umbral/internal/activeai/domain"
	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/config"
	"github.com/ecrespo/umbral/internal/llmgw"
	llmdomain "github.com/ecrespo/umbral/internal/llmgw/domain"
	"github.com/ecrespo/umbral/internal/sessions"
	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
)

// suggestionClass is the model class Active AI asks (REQ-AAI-001).
const suggestionClass = "fast"

// newActiveAI builds Active AI (T-F2-22) over the router, local only, and the block reader. It
// returns nil while `[active_ai] enabled` is false.
func newActiveAI(settings config.ActiveAI, b *bus.Bus, router *llmgw.Router, blocks *sessions.Reader, logger *slog.Logger) (*activeai.Service, error) {
	return activeai.New(activeai.Config{Enabled: settings.Enabled, PerMinute: settings.PerMinute, Logger: logger},
		b, localSuggester{router: router}, blockOutput{reader: blocks})
}

// streamer is the router as localSuggester calls it.
type streamer interface {
	Stream(ctx context.Context, call llmgw.Call) (iter.Seq2[llmdomain.Event, error], error)
}

// localSuggester asks the `fast` class with LocalOnly set, so the router drops every remote
// candidate before anything is sent (REQ-AAI-003).
type localSuggester struct {
	router streamer
}

func (s localSuggester) Suggest(ctx context.Context, system, user string) (string, string, error) {
	events, err := s.router.Stream(ctx, llmgw.Call{
		Class: suggestionClass, LocalOnly: true,
		Request: llmdomain.Request{
			System:          system,
			Messages:        []llmdomain.Message{{Role: llmdomain.RoleUser, Text: user}},
			MaxOutputTokens: aaidomain.MaxTokens,
			Reasoning:       "off",
		},
	})
	if err != nil {
		return "", "", err
	}
	// The stream is read to its end, 64 tokens at most, so its usage row is written.
	var text strings.Builder
	var model string
	for ev, err := range events {
		if err != nil {
			return "", "", err
		}
		switch ev.Kind {
		case llmdomain.EventTextDelta:
			text.WriteString(ev.Text)
		case llmdomain.EventUsage:
			model = ev.Usage.Model
		}
	}
	return text.String(), model, nil
}

// blockOutput reads a block's plain output for its prompt.
type blockOutput struct {
	reader *sessions.Reader
}

func (b blockOutput) Output(ctx context.Context, blockID, sessionID string) (string, error) {
	_, out, err := b.reader.Get(ctx, blockID, sessionID, sessdomain.IncludePlain)
	if err != nil {
		return "", err
	}
	return out.Plain, nil
}
