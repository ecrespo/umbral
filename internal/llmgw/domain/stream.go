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
	// ToolName is the tool a tool message answers; Ollama matches results by name.
	ToolName string
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
	// Reasoning asks a reasoning model to think: "" leaves the model's default, "on" and "off"
	// switch it, "low", "medium" and "high" set its effort where the model takes one.
	Reasoning string
	// ResponseSchema, when set, is a JSON Schema the answer must follow (structured output).
	ResponseSchema map[string]any
	// Vision says the request carries an image, so only a model with vision may take it
	// (REQ-RTR-001). The router reads it; no adapter sends images yet.
	Vision bool
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
	// EventQueued is the router's own, never an adapter's: the call waits behind other calls
	// for its provider (REQ-ORC-005). It carries nothing, comes before the call's first event,
	// and is followed by EventSent, or by the error that ended the wait.
	EventQueued EventKind = "queued"
	// EventSent is the router's own too: a call that said EventQueued holds its place and is
	// sent now. Its first-token wait starts here.
	EventSent EventKind = "sent"
)

// Usage is what a call consumed, in tokens (REQ-LLM-005).
type Usage struct {
	InputTokens     int64
	OutputTokens    int64
	ReasoningTokens int64
	CacheReadTokens int64
	// Model and CostMicroUSD are set by the router: the catalog id that served the call and
	// what it cost, as `usage` records it.
	Model        string
	CostMicroUSD int64
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
	// Permanent marks a failure no other candidate would avoid — a request that could not be
	// built — although it has no status.
	Permanent bool
}

func (e *ProviderError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("provider %s: HTTP %d: %v", e.Provider, e.Status, e.Err)
	}
	return fmt.Sprintf("provider %s: %v", e.Provider, e.Err)
}

func (e *ProviderError) Unwrap() error { return e.Err }

// Retryable reports whether the next candidate should be tried: a 429, a 5xx or a transport
// failure (REQ-LLM-003). Anything else — a 400, a 401, a request that could not be built, one
// the adapter cannot carry — would fail the same way on another try.
func (e *ProviderError) Retryable() bool {
	if e.Permanent || errors.Is(e.Err, ErrUnsupported) {
		return false
	}
	return e.Status == 0 || e.Status == 429 || e.Status >= 500
}

// ErrMalformedToolCall is a model's output its provider could not read as a tool call — Ollama
// answers it with an HTTP 500 "error parsing tool call" before any event. It is the model's
// invalid call, which the agent runtime retries once (REQ-AGT-006, delta
// `2026-10-usf2-002-live`); another candidate may still be tried first.
var ErrMalformedToolCall = errors.New("the provider could not read the model's tool call")

// ErrNoSuchProvider is a model whose provider is not configured.
var ErrNoSuchProvider = errors.New("no such provider")
