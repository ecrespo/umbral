package domain

import "errors"

// Usage statuses (Data Model §2.11 `usage.status`).
const (
	UsageOK              = "ok"
	UsageError           = "error"
	UsageTimeout         = "timeout"
	UsageRateLimited     = "rate_limited"
	UsageInvalidToolCall = "invalid_tool_call"
)

// UsageRecord is one model call as the `usage` table keeps it (REQ-LLM-005): what it
// consumed, how long its first token took and what it cost. A call that failed is recorded
// too, with its status and error (REQ-LLM-003).
type UsageRecord struct {
	// ThreadID and TurnID are empty for a call made for no thread.
	ThreadID  string
	TurnID    string
	ModelID   string
	Provider  string
	Status    string
	InTokens  int64
	OutTokens int64
	// FirstTokenMS is nil when no token arrived.
	FirstTokenMS *int64
	// CostMicroUSD is micro-USD (Art. 6).
	CostMicroUSD int64
	Error        string
	// CreatedAt is epoch milliseconds (Art. 6).
	CreatedAt int64
}

// Cost is what a call cost at a model's prices, in micro-USD, rounded to the nearest unit.
// Prices are micro-USD per million tokens, so tokens × price / 10^6.
func (m Model) Cost(in, out int64) int64 {
	return (in*m.PriceInMicroUSDPerMTok + out*m.PriceOutMicroUSDPerMTok + 500_000) / 1_000_000
}

var (
	// ErrNoCandidate is a call no candidate of its class could serve: none configured, none
	// left after the offline, capability and health filters, or every one failed. The API
	// maps it to PROVIDER_UNAVAILABLE (REQ-LLM-004).
	ErrNoCandidate = errors.New("no candidate can serve the call")
	// ErrUnsupported is a request an adapter cannot carry as asked — a response schema or a
	// reasoning setting it would silently drop. The router moves on to the next candidate
	// without recording a call, since none was made.
	ErrUnsupported = errors.New("the adapter cannot carry this request")
	// ErrUnknownClass is a task class models.toml does not declare.
	ErrUnknownClass = errors.New("unknown task class")
)
