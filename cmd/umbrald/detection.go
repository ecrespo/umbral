package main

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/integrations"
	"github.com/ecrespo/umbral/internal/integrations/adapters/manifests"
	"github.com/ecrespo/umbral/internal/integrations/adapters/proctree"
	"github.com/ecrespo/umbral/internal/integrations/domain"
	intports "github.com/ecrespo/umbral/internal/integrations/ports"
)

// agentsDir is REQ-XAG-002's local manifest directory, beside config.toml:
// `$XDG_CONFIG_HOME/umbral/agents/`.
func agentsDir(settingsPath string) string {
	return filepath.Join(filepath.Dir(settingsPath), "agents")
}

// startDetection builds third-party agent detection (T-F2-10) over the built-in manifests with
// the installed plugins' and then the local ones laid over them by name, and scans every live pane in the background. The
// manifests are read once, here; a skipped file is logged by the detector and answered by
// every agent.explain.
func startDetection(ctx context.Context, settingsPath string, pluginFiles []string, labels *integrations.AgentLabels,
	shells intports.Shells, panes intports.Panes, b *bus.Bus, logger *slog.Logger,
) (*integrations.Detector, error) {
	// Built-in, then installed plugins', then the local files, each overriding the layer before
	// by name (delta `2026-10-plugins`, decision 7).
	fromPlugins, skippedPlugins := manifests.LoadFiles(pluginFiles)
	local, skipped := manifests.Load(agentsDir(settingsPath))
	detector, err := integrations.NewDetector(integrations.DetectorConfig{
		Manifests: domain.Overlay(domain.Overlay(domain.Builtins(), fromPlugins), local),
		Skipped:   append(skippedPlugins, skipped...),
		Labels:    labels,
		Shells:    shells,
		Processes: proctree.Tree{},
		Panes:     panes,
		Bus:       b,
		Logger:    logger.With(slog.String("module", "integrations")),
	})
	if err != nil {
		return nil, err
	}
	go detector.Run(ctx, integrations.DetectionInterval)
	return detector, nil
}
