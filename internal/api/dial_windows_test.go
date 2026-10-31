package api

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"

	"github.com/ecrespo/umbral/internal/config"
)

// dialDaemon reaches a test server as a client does: through its runtime directory's pipe.
func dialDaemon(ctx context.Context, _ *net.Dialer, socketPath string) (net.Conn, error) {
	conn, err := winio.DialPipeContext(ctx, config.PipeName(socketPath))
	if err != nil {
		return nil, err
	}
	if err := config.VerifyPipeServer(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}
