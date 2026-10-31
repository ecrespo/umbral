package client

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"

	"github.com/ecrespo/umbral/internal/config"
)

// dialSocket reaches the daemon's named pipe for the runtime directory socketPath names (delta
// `2026-10-windows-port`, decision 3). A pipe nobody serves is os.ErrNotExist, which autostart
// reads as a daemon that is down, as a missing socket is on Unix.
func dialSocket(ctx context.Context, _ *net.Dialer, socketPath string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, config.PipeName(socketPath))
}
