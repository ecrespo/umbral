// Package ports is the notification sink's published interfaces (Tech Design §5.2: `notify`
// is a sink and imports no other module).
package ports

import (
	"context"

	"github.com/ecrespo/umbral/internal/notify/domain"
)

// Notifier is the inbound port, served as `notification.show` (API Spec §5.35) and called by
// the daemon's own components on behalf of a source. A request that cannot be shown however
// the daemon is configured answers domain.ErrValidation; every other outcome is a Result.
type Notifier interface {
	Show(ctx context.Context, req domain.Request) (domain.Result, error)
}

// Deliverer hands a notification to the foreground client: the interactive connection
// that last sent a request. It reports false when there is none.
type Deliverer interface {
	DeliverForeground(n domain.Notification) bool
}
