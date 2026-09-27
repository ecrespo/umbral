package domain

import (
	"errors"
	"fmt"
)

// Role is who wrote a message.
type Role string

// Roles.
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall is a call the model asked for; Input is its JSON arguments.
type ToolCall struct {
	ID    string
	Name  string
	Input string
}

// Message is one turn of the conversation sent to a model. A tool message carries the result
// of ToolCallID in Text; an assistant message may carry the tool calls it made.
type Message struct {
	Role       Role
	Text       string
	ToolCalls  []ToolCall
	ToolCallID string
	// ToolError marks a tool message whose Text is an error.
	ToolError bool
}

// ToolSpec is a tool offered to the model.
type ToolSpec struct {
	Name        string
	Description string
	// InputSchema is a JSON Schema object.
	InputSchema map[string]any
}

// Request is one model call, provider-neutral. Model is the provider's name for it, without
// the catalog's provider prefix.
type Request struct {
	Model           string
	System          string
	Messages        []Message
	Tools           []ToolSpec
	MaxOutputTokens int64
}

// EventKind is the type of a normalized stream event.
type EventKind string

// The normalized events every adapter produces (docs/ARCHITECTURE.md §6).
const (
	EventTextDelta      EventKind = "text_delta"
	EventReasoningDelta EventKind = "reasoning_delta"
	EventToolCall       EventKind = "tool_call"
	EventUsage          EventKind = "usage"
	EventDone           EventKind = "done"
)

// Usage is what a call consumed, in tokens (REQ-LLM-005).
type Usage struct {
	InputTokens     int64
	OutputTokens    int64
	ReasoningTokens int64
	CacheReadTokens int64
}

// Event is one normalized stream event. Text is set for the two deltas, ToolCall for a
// complete tool call, Usage for usage, and FinishReason for done.
type Event struct {
	Kind         EventKind
	Text         string
	ToolCall     ToolCall
	Usage        Usage
	FinishReason string
}

// ProviderError is a call a provider refused or could not complete. Status is the HTTP status
// when there was one, 0 for a transport failure.
type ProviderError struct {
	Provider string
	Status   int
	Err      error
}

func (e *ProviderError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("provider %s: HTTP %d: %v", e.Provider, e.Status, e.Err)
	}
	return fmt.Sprintf("provider %s: %v", e.Provider, e.Err)
}

func (e *ProviderError) Unwrap() error { return e.Err }

// Retryable reports whether the next candidate should be tried: a 429, a 5xx or a transport
// failure (REQ-LLM-003). Anything else — a 400, a 401 — would fail the same way anywhere.
func (e *ProviderError) Retryable() bool {
	return e.Status == 0 || e.Status == 429 || e.Status >= 500
}

// ErrNoSuchProvider is a model whose provider is not configured.
var ErrNoSuchProvider = errors.New("no such provider")
