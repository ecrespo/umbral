package config

import (
	"net"

	"github.com/Microsoft/go-winio"
)

// ListenPipe listens on the named pipe name with go-winio, under security descriptor sddl
// ("" is the default one).
func ListenPipe(name, sddl string) (net.Listener, error) {
	return winio.ListenPipe(name, &winio.PipeConfig{
		SecurityDescriptor: sddl,
		InputBufferSize:    PipeBufferSize,
		OutputBufferSize:   PipeBufferSize,
	})
}
