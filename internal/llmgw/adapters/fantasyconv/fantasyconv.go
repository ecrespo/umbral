// Package fantasyconv is what the adapters built on Fantasy share: the request translated
// into a fantasy.Call, the stream translated into the gateway's normalized events, errors
// into ProviderError, and the HTTP helpers model discovery needs.
package fantasyconv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"strings"

	"charm.land/fantasy"

	"github.com/ecrespo/umbral/internal/llmgw/domain"
)

// Call translates a request. The system prompt goes first as its own message.
func Call(req domain.Request) fantasy.Call {
	prompt := make(fantasy.Prompt, 0, len(req.Messages)+1)
	if req.System != "" {
		prompt = append(prompt, fantasy.NewSystemMessage(req.System))
	}
	for _, m := range req.Messages {
		prompt = append(prompt, message(m))
	}
	call := fantasy.Call{Prompt: prompt}
	for _, t := range req.Tools {
		call.Tools = append(call.Tools, fantasy.FunctionTool{
			Name: t.Name, Description: t.Description, InputSchema: t.InputSchema,
		})
	}
	if req.MaxOutputTokens > 0 {
		n := req.MaxOutputTokens
		call.MaxOutputTokens = &n
	}
	return call
}

func message(m domain.Message) fantasy.Message {
	switch m.Role {
	case domain.RoleAssistant:
		out := fantasy.Message{Role: fantasy.MessageRoleAssistant}
		if m.Text != "" {
			out.Content = append(out.Content, fantasy.TextPart{Text: m.Text})
		}
		for _, c := range m.ToolCalls {
			out.Content = append(out.Content, fantasy.ToolCallPart{ToolCallID: c.ID, ToolName: c.Name, Input: c.Input})
		}
		return out
	case domain.RoleTool:
		var output fantasy.ToolResultOutputContent = fantasy.ToolResultOutputContentText{Text: m.Text}
		if m.ToolError {
			output = fantasy.ToolResultOutputContentError{Error: errors.New(m.Text)}
		}
		return fantasy.Message{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{
			fantasy.ToolResultPart{ToolCallID: m.ToolCallID, Output: output},
		}}
	default:
		return fantasy.NewUserMessage(m.Text)
	}
}

// Stream opens the call on a Fantasy model and normalizes what it yields. Deltas pass through
// one by one; a tool call is yielded once, complete; the finish part becomes a usage event and
// a done event. A part of type error ends the stream with that error as a ProviderError.
func Stream(ctx context.Context, provider string, lm fantasy.LanguageModel, req domain.Request) (iter.Seq2[domain.Event, error], error) {
	parts, err := lm.Stream(ctx, Call(req))
	if err != nil {
		return nil, Error(provider, err)
	}
	return func(yield func(domain.Event, error) bool) {
		for part := range parts {
			ev, ok, err := event(part)
			if err != nil {
				yield(domain.Event{}, Error(provider, err))
				return
			}
			if !ok {
				continue
			}
			if part.Type == fantasy.StreamPartTypeFinish {
				if !yield(domain.Event{Kind: domain.EventUsage, Usage: usage(part.Usage)}, nil) {
					return
				}
			}
			if !yield(ev, nil) {
				return
			}
		}
	}, nil
}

func event(p fantasy.StreamPart) (domain.Event, bool, error) {
	switch p.Type {
	case fantasy.StreamPartTypeTextDelta:
		return domain.Event{Kind: domain.EventTextDelta, Text: p.Delta}, p.Delta != "", nil
	case fantasy.StreamPartTypeReasoningDelta:
		return domain.Event{Kind: domain.EventReasoningDelta, Text: p.Delta}, p.Delta != "", nil
	case fantasy.StreamPartTypeToolCall:
		return domain.Event{Kind: domain.EventToolCall, ToolCall: domain.ToolCall{
			ID: p.ID, Name: p.ToolCallName, Input: p.ToolCallInput,
		}}, true, nil
	case fantasy.StreamPartTypeFinish:
		return domain.Event{Kind: domain.EventDone, FinishReason: finishReason(p.FinishReason)}, true, nil
	case fantasy.StreamPartTypeError:
		if p.Error == nil {
			return domain.Event{}, false, errors.New("the provider reported an error")
		}
		return domain.Event{}, false, p.Error
	default:
		return domain.Event{}, false, nil
	}
}

// finishReason keeps Fantasy's vocabulary, except that OpenAI's `tool-calls` is spelt the way
// the rest of Umbral spells identifiers.
func finishReason(r fantasy.FinishReason) string {
	return strings.ReplaceAll(string(r), "-", "_")
}

func usage(u fantasy.Usage) domain.Usage {
	return domain.Usage{
		InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
		ReasoningTokens: u.ReasoningTokens, CacheReadTokens: u.CacheReadTokens,
	}
}

// Error wraps err as a ProviderError, keeping the HTTP status Fantasy saw.
func Error(provider string, err error) error {
	var pe *domain.ProviderError
	if errors.As(err, &pe) {
		return err
	}
	out := &domain.ProviderError{Provider: provider, Err: err}
	var fe *fantasy.ProviderError
	if errors.As(err, &fe) {
		out.Status = fe.StatusCode
	}
	return out
}

// IsLocal reports whether a base URL points at this machine: a loopback address or
// `localhost`. Anything else is remote, whatever network it is on (REQ-LLM-004).
func IsLocal(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	return isLoopbackHost(u.Hostname())
}

// CheckBaseURL refuses a base URL that is not absolute http(s).
func CheckBaseURL(baseURL string) error {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("base_url %q is not an http(s) URL", baseURL)
	}
	return nil
}

// maxDiscoveryBody bounds a model list; OpenRouter's, the largest known, is a few MiB.
const maxDiscoveryBody = 32 << 20

// GetJSON fetches url with an optional bearer key and decodes the body into out. A status
// other than 200 is a ProviderError with that status.
func GetJSON(ctx context.Context, client *http.Client, provider, rawURL, apiKey string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return &domain.ProviderError{Provider: provider, Err: err}
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return &domain.ProviderError{Provider: provider, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDiscoveryBody))
	if err != nil {
		return &domain.ProviderError{Provider: provider, Status: resp.StatusCode, Err: err}
	}
	if resp.StatusCode != http.StatusOK {
		return &domain.ProviderError{
			Provider: provider, Status: resp.StatusCode,
			Err: fmt.Errorf("model list: %s", http.StatusText(resp.StatusCode)),
		}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &domain.ProviderError{Provider: provider, Status: resp.StatusCode, Err: fmt.Errorf("model list: %w", err)}
	}
	return nil
}
