package ports

import (
	"context"

	llmdomain "github.com/ecrespo/umbral/internal/llmgw/domain"
)

// RuleEgress records every request the rule fetcher makes (REQ-SEC-011, Art. 4): the same
// `egress_log` the model gateway writes.
type RuleEgress interface {
	Record(ctx context.Context, rec llmdomain.EgressRecord) error
}

// RuleUpdater is handed every bundle the fetcher brought back, with its signature, and decides
// whether it is applied: signature, trust store and version (REQ-SEC-011, REQ-SEC-013). The
// fetcher itself never applies anything.
type RuleUpdater interface {
	Offer(ctx context.Context, bundle, signature []byte)
}
