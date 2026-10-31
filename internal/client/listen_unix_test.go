//go:build unix

package client

import (
	"context"
	"net"
)

// listenFake serves a fake daemon where a client dials socket.
func listenFake(socket string) (net.Listener, error) {
	var lc net.ListenConfig
	return lc.Listen(context.Background(), "unix", socket)
}
