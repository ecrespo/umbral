package main

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/context/adapters/skills"
	"github.com/ecrespo/umbral/internal/integrations/adapters/manifests"
	"github.com/ecrespo/umbral/internal/plugins/adapters/pluginstore"
	plgports "github.com/ecrespo/umbral/internal/plugins/ports"
	"github.com/ecrespo/umbral/internal/store"
)

// newPlugins builds the plugin store (T-F2-60) in `plugins/` beside the database, over the MCP
// client and the skill store it adds a plugin's servers and skills through, and runs Data
// Model §6 step 10 before any client can install.
func newPlugins(ctx context.Context, db *store.Store, servers plgports.Servers, skillStore plgports.Skills,
	b *bus.Bus, logger *slog.Logger,
) (*pluginstore.Service, error) {
	svc, err := pluginstore.New(pluginstore.Config{
		DB: db, Dir: filepath.Join(filepath.Dir(db.Path()), "plugins"),
		Bundles: bundleReader{}, Detection: detectionCheck{}, Servers: servers, Skills: skillStore, Bus: b,
		NewID:  func() string { return store.NewID(store.PrefixPlugin) },
		Logger: logger.With(slog.String("module", "plugins")),
	})
	if err != nil {
		return nil, err
	}
	return svc, svc.Recover(ctx)
}

// bundleReader is the skill store's bundle reader, with every limit and refusal it applies.
type bundleReader struct{}

func (bundleReader) ReadFiles(path, root string) (map[string][]byte, error) {
	return skills.ReadFiles(path, root)
}

// detectionCheck parses a detection manifest as the detector would, without loading it.
type detectionCheck struct{}

func (detectionCheck) Check(raw []byte) error {
	_, err := manifests.Parse(raw, "")
	return err
}
