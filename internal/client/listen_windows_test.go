package client

import (
	"net"

	"github.com/ecrespo/umbral/internal/config"
)

// listenFake serves a fake daemon on the pipe a client dials for socket.
func listenFake(socket string) (net.Listener, error) {
	return config.ListenPipe(config.PipeName(socket), "")
}
