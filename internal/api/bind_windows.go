package api

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"

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
	return ln, nil
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
