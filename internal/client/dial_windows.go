package client

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"

	"github.com/ecrespo/umbral/internal/config"
)

// dialSocket reaches the daemon's named pipe for the runtime directory socketPath names (delta
// `2026-10-windows-port`, decision 3). A pipe nobody serves is os.ErrNotExist, which autostart
// reads as a daemon that is down, as a missing socket is on Unix. A pipe another user's process
// serves is refused before the handshake, so the token never reaches it.
func dialSocket(ctx context.Context, _ *net.Dialer, socketPath string) (net.Conn, error) {
	conn, err := winio.DialPipeContext(ctx, config.PipeName(socketPath))
	if err != nil {
		return nil, err
	}
	if err := config.VerifyPipeServer(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return pipeConn{conn}, nil
}

// pipeConn reports a pipe this side closed as net.ErrClosed, as a closed socket is: go-winio
// returns an error of its own, which the stream would otherwise take for a protocol error
// rather than the hang-up it is.
type pipeConn struct{ net.Conn }

func (p pipeConn) Read(b []byte) (int, error) {
	n, err := p.Conn.Read(b)
	return n, closedAsNet(err)
}

func (p pipeConn) Write(b []byte) (int, error) {
	n, err := p.Conn.Write(b)
	return n, closedAsNet(err)
}

func (p pipeConn) Close() error { return closedAsNet(p.Conn.Close()) }

func closedAsNet(err error) error {
	if errors.Is(err, winio.ErrFileClosed) {
		return fmt.Errorf("%w: %w", net.ErrClosed, err)
	}
	return err
}
