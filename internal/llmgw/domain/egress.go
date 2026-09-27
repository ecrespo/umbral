package domain

import (
	"context"
	"fmt"
)

// EgressRecord is one request that left the machine (Art. 4, REQ-SEC-002; Data Model
// `egress_log`). Only a hash of the payload is kept, never the payload.
type EgressRecord struct {
	// ThreadID is the thread the request was made for; empty for the daemon's own requests,
	// such as model discovery.
	ThreadID      string
	Provider      string
	Host          string
	Bytes         int64
	PayloadSHA256 string
	// CreatedAt is epoch milliseconds (Art. 6).
	CreatedAt int64
}

type threadKey struct{}

// WithThread marks a context as working for a thread, so the egress record of every request
// made under it names the thread.
func WithThread(ctx context.Context, threadID string) context.Context {
	return context.WithValue(ctx, threadKey{}, threadID)
}

// ThreadOf is the thread WithThread put in ctx, or "".
func ThreadOf(ctx context.Context) string {
	id, _ := ctx.Value(threadKey{}).(string)
	return id
}

// APIKey holds a provider's key inside the gateway. Like the security module's Secret it
// prints as a placeholder under every verb, so an adapter's configuration that reaches a log
// by accident reaches it without the key (Tech Design §5.1).
type APIKey struct{ value string }

// NewAPIKey wraps a key.
func NewAPIKey(value string) APIKey { return APIKey{value: value} }

// Reveal returns the key, for the one call that puts it in a header.
func (k APIKey) Reveal() string { return k.value }

// IsZero reports whether there is no key.
func (k APIKey) IsZero() bool { return k.value == "" }

const redactedKey = "[REDACTED]"

// String keeps %v, %s and %q from printing the key.
func (k APIKey) String() string { return redactedKey }

// GoString keeps %#v from printing it.
func (k APIKey) GoString() string { return redactedKey }

// Format covers the verbs String and GoString do not.
func (k APIKey) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(redactedKey)) }
