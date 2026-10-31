//go:build unix

package client

import (
	"context"
	"net"
)

// dialSocket reaches the daemon's Unix socket.
func dialSocket(ctx context.Context, d *net.Dialer, socketPath string) (net.Conn, error) {
	return d.DialContext(ctx, "unix", socketPath)
}
