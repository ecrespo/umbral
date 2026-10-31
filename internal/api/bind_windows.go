package api

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"

	"github.com/ecrespo/umbral/internal/config"
)

// bind serves the protocol on the runtime directory's named pipe, whose security descriptor
// admits the current user alone: Windows's equivalent of the 0600 socket (REQ-WIN-001,
// REQ-SEC-007; delta `2026-10-windows-port`, decision 3). A pipe has no file to leave behind,
// so there is nothing stale to remove; the instance lock still makes this daemon the only one.
func bind(_ context.Context, cfg Config) (net.Listener, error) {
	if cfg.Listener != nil {
		return cfg.Listener, nil
	}
	if err := os.MkdirAll(filepath.Dir(cfg.SocketPath), config.RuntimeDirMode); err != nil {
		return nil, fmt.Errorf("api: create the runtime directory: %w", err)
	}
	sddl, err := ownerOnlySDDL()
	if err != nil {
		return nil, err
	}
	name := config.PipeName(cfg.SocketPath)
	ln, err := winio.ListenPipe(name, &winio.PipeConfig{
		SecurityDescriptor: sddl,
		InputBufferSize:    config.PipeBufferSize,
		OutputBufferSize:   config.PipeBufferSize,
	})
	if err != nil {
		return nil, fmt.Errorf("api: listen on %s: %w", name, err)
	}
	return flushingListener{ln}, nil
}

// flushWithin bounds how long a closing connection waits for its client to read what was
// written last.
const flushWithin = time.Second

// flushingListener hands out connections that let their client read the daemon's last words.
// Closing a pipe's server end discards what the client has not read yet, so the UNAUTHORIZED
// or VALIDATION_ERROR sent just before a hang-up (API Spec §1, REQ-SEC-003) would never
// arrive; on a Unix socket it stays readable after the close.
type flushingListener struct{ net.Listener }

func (l flushingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if p, ok := c.(winio.PipeConn); ok {
		return &flushingConn{PipeConn: p}, nil
	}
	return c, nil
}

// flushingConn closes without blocking its caller: the flush, bounded by flushWithin so a
// client that stopped reading cannot hold it (REQ-API-006), then the close, run on a goroutine
// of their own.
type flushingConn struct {
	winio.PipeConn
	once sync.Once
}

func (c *flushingConn) Close() error {
	c.once.Do(func() {
		go func() {
			flushed := make(chan struct{})
			go func() {
				_ = c.PipeConn.Flush()
				close(flushed)
			}()
			select {
			case <-flushed:
			case <-time.After(flushWithin):
			}
			_ = c.PipeConn.Close()
		}()
	})
	return nil
}

// ownerOnlySDDL grants generic access to the current user's SID and nobody else, with the DACL
// protected from inheritance.
func ownerOnlySDDL() (string, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("api: read the current user: %w", err)
	}
	return "D:P(A;;GA;;;" + u.User.Sid.String() + ")", nil
}

// Fd is the pipe's handle, which peerPID reads the client's pid from.
func (c *flushingConn) Fd() uintptr {
	if f, ok := c.PipeConn.(interface{ Fd() uintptr }); ok {
		return f.Fd()
	}
	return 0
}
