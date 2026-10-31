//go:build unix

package api

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/ecrespo/umbral/internal/config"
)

// bind is the listening socket: the inherited one, or a fresh one at SocketPath.
func bind(ctx context.Context, cfg Config) (net.Listener, error) {
	if cfg.Listener != nil {
		return cfg.Listener, nil
	}
	if err := os.MkdirAll(filepath.Dir(cfg.SocketPath), config.RuntimeDirMode); err != nil {
		return nil, fmt.Errorf("api: create the runtime directory: %w", err)
	}
	// A socket left behind by a crashed daemon would make Listen fail with EADDRINUSE,
	// so it is removed. What makes that safe is the instance lock `cmd/umbrald` takes on
	// the runtime directory before it gets here: a live daemon holds it, so anything
	// still on disk belongs to a daemon that is gone. Nothing else may call New without
	// holding that lock — an earlier comment here claimed a database lock did the job,
	// and there is no such lock.
	if err := removeStaleSocket(cfg.SocketPath); err != nil {
		return nil, err
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "unix", cfg.SocketPath)
	if err != nil {
		return nil, fmt.Errorf("api: listen on %s: %w", cfg.SocketPath, err)
	}
	// net.Listen applies the umask, so the mode is set explicitly rather than assumed.
	// This is REQ-SEC-007 and it is checked by a test, not by inspection.
	if err := os.Chmod(cfg.SocketPath, socketMode); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("api: set socket permissions: %w", err)
	}
	return ln, nil
}
