package api

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"

	"github.com/ecrespo/umbral/internal/config"
)

// dialDaemon reaches a test server as a client does: through its runtime directory's pipe.
func dialDaemon(ctx context.Context, _ *net.Dialer, socketPath string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, config.PipeName(socketPath))
}
