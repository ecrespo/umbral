package main

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/client"
	"github.com/ecrespo/umbral/internal/config"
	"github.com/ecrespo/umbral/internal/remote"
	"github.com/ecrespo/umbral/internal/remote/adapters/hoststore"
	"github.com/ecrespo/umbral/internal/remote/adapters/sshtunnel"
	remoteports "github.com/ecrespo/umbral/internal/remote/ports"
	"github.com/ecrespo/umbral/internal/store"
)

// startRemotes links the remote hosts the user added (T-F2-41): their forwarded sockets live in
// `remotes/` beside this daemon's own, and every link ends with ctx.
func startRemotes(ctx context.Context, settings config.Remote, runtimeDir string, db *store.Store,
	eventBus *bus.Bus, logger *slog.Logger,
) (*remote.Manager, error) {
	hosts, err := hoststore.New(db)
	if err != nil {
		return nil, err
	}
	m, err := remote.New(remote.Options{
		Store: hosts, Tunnels: sshtunnel.New(settings.SSH, settings.Umb), Publisher: eventBus,
		Dir: filepath.Join(runtimeDir, "remotes"), Logger: logger.With(slog.String("module", "remote")),
		Probe: probeHost,
	})
	if err != nil {
		return nil, err
	}
	if err := m.Start(ctx); err != nil {
		return nil, err
	}
	return m, nil
}

// probeHost completes a handshake with a host's daemon through its forward and hangs up: ssh
// opens the remote socket only when a connection asks for it, so this is what says the daemon is
// there and takes this token (REQ-RMT-001, REQ-RMT-002).
func probeHost(ctx context.Context, link remoteports.Link) error {
	c, err := client.DialAs(ctx, link.Socket, link.Token, client.ClientKindCLI)
	if err != nil {
		return err
	}
	return c.Close()
}
