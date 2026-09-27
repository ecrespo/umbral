// Package ollama is the native Ollama adapter (REQ-LLM-001, DD-005): `/api/chat` with
// streaming, tools, `think`, `format`, `keep_alive` and — in every request — `num_ctx`
// (REQ-LLM-006); discovery through `/api/tags` (REQ-LLM-002). Ollama's OpenAI-compatible
// endpoint cannot carry num_ctx, which is why this adapter exists.
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ecrespo/umbral/internal/llmgw/adapters/fantasyconv"
	"github.com/ecrespo/umbral/internal/llmgw/domain"
	"github.com/ecrespo/umbral/internal/llmgw/ports"
)

// DefaultNumCtx is the context sent when models.toml configures none: long enough for an
// agent's prompt and tools, where Ollama's own default is not (DD-005). It is capped at the
// model's window once discovery knows it.
const DefaultNumCtx = 32768

// Config is one provider entry of models.toml.
type Config struct {
	ID      string
	BaseURL string
	// APIKey is sent as a bearer token when set: a local Ollama needs none, one behind a
	// proxy may.
	APIKey domain.APIKey
	// Options are `[providers.options]`: `num_ctx` and every other model option go in the
	// request's `options`; `keep_alive` goes at its top level.
	Options map[string]any
	// Egress records every request to a host that is not loopback (Art. 4).
	Egress     ports.EgressLog
	HTTPClient *http.Client
}

// Provider is one Ollama server.
type Provider struct {
	cfg    Config
	base   string
	local  bool
	client *http.Client

	mu sync.RWMutex
	// windows is each model's context window from the last discovery.
	windows map[string]int64
}

const discoveryTimeout = 15 * time.Second

// New builds the adapter. It does not contact the server.
func New(cfg Config) (*Provider, error) {
	if cfg.ID == "" {
		return nil, errors.New("ollama: a provider id is required")
	}
	if err := fantasyconv.CheckBaseURL(cfg.BaseURL); err != nil {
		return nil, err
	}
	if v, ok := cfg.Options["num_ctx"]; ok && !positiveInt(v) {
		// Zero or a string would bring back the short default DD-005 exists to avoid.
		return nil, fmt.Errorf("ollama: options.num_ctx must be a positive integer, not %v", v)
	}
	return &Provider{
		cfg: cfg, base: strings.TrimSuffix(cfg.BaseURL, "/"), local: fantasyconv.IsLocal(cfg.BaseURL),
		client: fantasyconv.Client(cfg.HTTPClient, cfg.ID, cfg.Egress), windows: map[string]int64{},
	}, nil
}

// Format keeps every verb from printing the provider's key.
func (p *Provider) Format(f fmt.State, _ rune) { _, _ = fmt.Fprintf(f, "ollama provider %q", p.cfg.ID) }

// ID implements ports.Provider.
func (p *Provider) ID() string { return p.cfg.ID }

// Local implements ports.Provider.
func (p *Provider) Local() bool { return p.local }

type tagList struct {
	Models []struct {
		Name    string `json:"name"`
		Details struct {
			ContextLength int64 `json:"context_length"`
		} `json:"details"`
		Capabilities []string `json:"capabilities"`
	} `json:"models"`
}

// Models implements ports.Provider. Ollama reports each model's capabilities; an older one
// that reports none is assumed to take tools, like any chat model.
func (p *Provider) Models(ctx context.Context) ([]domain.Model, error) {
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	var list tagList
	if err := fantasyconv.GetJSON(ctx, p.client, p.cfg.ID, p.base+"/api/tags", p.cfg.APIKey.Reveal(), &list); err != nil {
		return nil, err
	}
	out := make([]domain.Model, 0, len(list.Models))
	windows := make(map[string]int64, len(list.Models))
	for _, m := range list.Models {
		if m.Name == "" {
			continue
		}
		caps := m.Capabilities
		chat := len(caps) == 0 || slices.Contains(caps, "completion")
		out = append(out, domain.Model{
			ID: domain.QualifiedID(p.cfg.ID, m.Name), Provider: p.cfg.ID, Local: p.local,
			Caps: domain.Capabilities{
				Tools:         len(caps) == 0 || slices.Contains(caps, "tools"),
				Reasoning:     slices.Contains(caps, "thinking"),
				Vision:        slices.Contains(caps, "vision"),
				JSONSchema:    chat,
				ContextWindow: m.Details.ContextLength,
			},
		})
		windows[m.Name] = m.Details.ContextLength
	}
	p.mu.Lock()
	p.windows = windows
	p.mu.Unlock()
	return out, nil
}

func positiveInt(v any) bool {
	switch n := v.(type) {
	case int:
		return n > 0
	case int64:
		return n > 0
	case float64:
		return n > 0 && n == float64(int64(n))
	}
	return false
}

// numCtx is REQ-LLM-006's value for a model: the configured one, else DefaultNumCtx capped at
// the model's window when discovery knows it.
func (p *Provider) numCtx(model string) any {
	if v, ok := p.cfg.Options["num_ctx"]; ok {
		return v
	}
	p.mu.RLock()
	window := p.windows[model]
	p.mu.RUnlock()
	if window > 0 && window < DefaultNumCtx {
		return window
	}
	return DefaultNumCtx
}

type chatMessage struct {
	Role      string         `json:"role"`
	Content   string         `json:"content"`
	Thinking  string         `json:"thinking,omitempty"`
	ToolCalls []chatToolCall `json:"tool_calls,omitempty"`
	ToolName  string         `json:"tool_name,omitempty"`
}

type chatToolCall struct {
	ID       string `json:"id,omitempty"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type chatTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description,omitempty"`
		Parameters  map[string]any `json:"parameters,omitempty"`
	} `json:"function"`
}

type chatRequest struct {
	Model     string         `json:"model"`
	Messages  []chatMessage  `json:"messages"`
	Tools     []chatTool     `json:"tools,omitempty"`
	Stream    bool           `json:"stream"`
	Think     any            `json:"think,omitempty"`
	Format    map[string]any `json:"format,omitempty"`
	KeepAlive any            `json:"keep_alive,omitempty"`
	Options   map[string]any `json:"options"`
}

func (p *Provider) request(req domain.Request) (chatRequest, error) {
	out := chatRequest{Model: req.Model, Stream: true, Format: req.ResponseSchema, Options: map[string]any{}}
	for k, v := range p.cfg.Options {
		if k == "keep_alive" {
			out.KeepAlive = v
			continue
		}
		out.Options[k] = v
	}
	out.Options["num_ctx"] = p.numCtx(req.Model)
	if req.MaxOutputTokens > 0 {
		out.Options["num_predict"] = req.MaxOutputTokens
	}
	switch req.Reasoning {
	case "":
	case "on":
		out.Think = true
	case "off":
		// omitempty drops only a nil interface, so false is sent.
		out.Think = false
	default:
		out.Think = req.Reasoning
	}
	if req.System != "" {
		out.Messages = append(out.Messages, chatMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		cm := chatMessage{Role: string(m.Role), Content: m.Text}
		if m.Role == domain.RoleTool {
			cm.ToolName = m.ToolName
		}
		for _, c := range m.ToolCalls {
			var tc chatToolCall
			tc.ID, tc.Function.Name = c.ID, c.Name
			args := strings.TrimSpace(c.Input)
			if args == "" {
				args = "{}"
			}
			if !json.Valid([]byte(args)) {
				return chatRequest{}, fmt.Errorf("tool call %s: arguments are not JSON", c.ID)
			}
			tc.Function.Arguments = json.RawMessage(args)
			cm.ToolCalls = append(cm.ToolCalls, tc)
		}
		out.Messages = append(out.Messages, cm)
	}
	for _, t := range req.Tools {
		var ct chatTool
		ct.Type = "function"
		ct.Function.Name, ct.Function.Description, ct.Function.Parameters = t.Name, t.Description, t.InputSchema
		out.Tools = append(out.Tools, ct)
	}
	return out, nil
}

type chatChunk struct {
	Message         chatMessage `json:"message"`
	Done            bool        `json:"done"`
	DoneReason      string      `json:"done_reason"`
	PromptEvalCount int64       `json:"prompt_eval_count"`
	EvalCount       int64       `json:"eval_count"`
	Error           string      `json:"error"`
}

// maxLine bounds one NDJSON line; a tool call's arguments are the largest thing in one.
const maxLine = 16 << 20

// Stream implements ports.Provider.
func (p *Provider) Stream(ctx context.Context, req domain.Request) (iter.Seq2[domain.Event, error], error) {
	body, err := p.request(req)
	if err != nil {
		return nil, &domain.ProviderError{Provider: p.cfg.ID, Err: err}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, &domain.ProviderError{Provider: p.cfg.ID, Err: err}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+"/api/chat", bytes.NewReader(raw))
	if err != nil {
		return nil, &domain.ProviderError{Provider: p.cfg.ID, Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if !p.cfg.APIKey.IsZero() {
		httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey.Reveal())
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, &domain.ProviderError{Provider: p.cfg.ID, Err: err}
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var e struct {
			Error string `json:"error"`
		}
		detail := strings.TrimSpace(string(msg))
		if json.Unmarshal(msg, &e) == nil && e.Error != "" {
			detail = e.Error
		}
		return nil, &domain.ProviderError{Provider: p.cfg.ID, Status: resp.StatusCode, Err: errors.New(detail)}
	}
	return func(yield func(domain.Event, error) bool) {
		defer func() { _ = resp.Body.Close() }()
		p.read(resp.Body, yield)
	}, nil
}

// read turns the NDJSON stream into events. Ollama gives each tool call whole, and ends with
// `done: true` and the eval counts; a stream that ends without it was cut.
func (p *Provider) read(body io.Reader, yield func(domain.Event, error) bool) {
	fail := func(err error) { yield(domain.Event{}, &domain.ProviderError{Provider: p.cfg.ID, Err: err}) }
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	calls := 0
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var c chatChunk
		if err := json.Unmarshal(line, &c); err != nil {
			fail(fmt.Errorf("malformed stream line: %w", err))
			return
		}
		if c.Error != "" {
			fail(errors.New(c.Error))
			return
		}
		if c.Message.Thinking != "" && !yield(domain.Event{Kind: domain.EventReasoningDelta, Text: c.Message.Thinking}, nil) {
			return
		}
		if c.Message.Content != "" && !yield(domain.Event{Kind: domain.EventTextDelta, Text: c.Message.Content}, nil) {
			return
		}
		for _, tc := range c.Message.ToolCalls {
			calls++
			id := tc.ID
			if id == "" {
				id = fmt.Sprintf("call_%d", calls)
			}
			args := string(tc.Function.Arguments)
			if args == "" || args == "null" {
				args = "{}"
			}
			if !yield(domain.Event{Kind: domain.EventToolCall, ToolCall: domain.ToolCall{ID: id, Name: tc.Function.Name, Input: args}}, nil) {
				return
			}
		}
		if c.Done {
			reason := c.DoneReason
			if calls > 0 && (reason == "" || reason == "stop") {
				reason = "tool_calls"
			}
			if !yield(domain.Event{Kind: domain.EventUsage, Usage: domain.Usage{InputTokens: c.PromptEvalCount, OutputTokens: c.EvalCount}}, nil) {
				return
			}
			yield(domain.Event{Kind: domain.EventDone, FinishReason: reason}, nil)
			return
		}
	}
	if err := sc.Err(); err != nil {
		fail(err)
		return
	}
	fail(errors.New("the stream ended before done"))
}
