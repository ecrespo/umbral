package client

import (
	"net"

	"github.com/Microsoft/go-winio"

	"github.com/ecrespo/umbral/internal/config"
)

// listenFake serves a fake daemon on the pipe a client dials for socket.
func listenFake(socket string) (net.Listener, error) {
	return winio.ListenPipe(config.PipeName(socket), nil)
}
