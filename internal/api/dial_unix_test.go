//go:build unix

package api

import (
	"context"
	"net"
)

// dialDaemon reaches a test server as a client does.
func dialDaemon(ctx context.Context, d *net.Dialer, socketPath string) (net.Conn, error) {
	return d.DialContext(ctx, "unix", socketPath)
}
