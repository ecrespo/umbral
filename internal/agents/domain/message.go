package domain

import "encoding/json"

// Role is a message's role (`messages.role`).
type Role string

// Roles. A system_note is the daemon's own record — a compaction summary — and is not a turn
// of the conversation.
const (
	RoleUser       Role = "user"
	RoleAssistant  Role = "assistant"
	RoleSystemNote Role = "system_note"
)

// Attachment is what `attachments_json` records of an attachment.
type Attachment struct {
	Kind           string `json:"kind"`
	Ref            string `json:"ref"`
	Bytes          int64  `json:"bytes"`
	TruncatedBytes int64  `json:"truncated_bytes"`
}

// Message is API §4's Message.
type Message struct {
	ID          string
	ThreadID    string
	TurnID      string
	Role        Role
	Content     string
	ClientMsgID string
	Attachments []Attachment
	Tainted     bool
	CreatedAt   int64
}

// ToolStatus is a tool call's status (API §4 ToolCall).
type ToolStatus string

// Tool call statuses.
const (
	ToolPending        ToolStatus = "pending"
	ToolOK             ToolStatus = "ok"
	ToolError          ToolStatus = "error"
	ToolDeniedByUser   ToolStatus = "denied_by_user"
	ToolDeniedByPolicy ToolStatus = "denied_by_policy"
	ToolInvalidArgs    ToolStatus = "invalid_args"
)

// ToolCall is API §4's ToolCall, with the result the model reads.
type ToolCall struct {
	ID            string
	ThreadID      string
	MessageID     string
	Tool          string
	Risk          string
	Args          json.RawMessage
	Status        ToolStatus
	ResultSummary string
	// Result is what the model reads back; Tainted marks it untrusted (REQ-SEC-006).
	Result    string
	Tainted   bool
	BlockID   string
	StartedAt int64
	EndedAt   *int64
}

// Usage is what a turn consumed (`thread.turn_finished`'s usage).
type Usage struct {
	InTokens     int64
	OutTokens    int64
	CostMicroUSD int64
}
