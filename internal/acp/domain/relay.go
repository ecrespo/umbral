package domain

// Upstream is the one model endpoint a hosted thread's relay forwards to (REQ-SBX-009, delta
// `2026-11-model-relay` decision 1): the provider of `models.toml` and its base URL.
type Upstream struct {
	Provider string
	BaseURL  string
}

// Egress is one request a relay forwarded to an endpoint off the machine (REQ-SBX-009,
// Art. 4): what an `egress_log` row holds. The body is neither stored nor redacted; only its
// size and SHA-256 are kept.
type Egress struct {
	ThreadID      string
	Provider      string
	Host          string
	Bytes         int64
	PayloadSHA256 string
	// CreatedAt is epoch milliseconds (Art. 6).
	CreatedAt int64
}
