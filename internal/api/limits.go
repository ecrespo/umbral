package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ecrespo/umbral/internal/config"
)

// limitsResult is `limits.get`'s answer (API Spec §5): the counters and the limit a new
// connection is greeted with, which is also what the settings file holds once `limits.set`
// has written it.
type limitsResult struct {
	Frames                    Frames `json:"frames"`
	ConfiguredMaxMessageBytes int64  `json:"configured_max_message_bytes"`
}

type setLimitsParams struct {
	MaxMessageBytes int `json:"max_message_bytes"`
}

// setLimitsResult says what changed and for whom: only connections opened afterwards.
type setLimitsResult struct {
	MaxMessageBytes int64  `json:"max_message_bytes"`
	AppliesTo       string `json:"applies_to"`
}

// appliesToNewConnections is the only scope a limit change has. Changing a framing limit
// under a reader that has already sized its buffer is how the T-F0-27 class of bug starts.
const appliesToNewConnections = "new_connections"

// Frames is the run's frame counters (REQ-OBS-005), for the metrics exporter.
func (s *Server) Frames() Frames { return s.framesNow() }

// framesNow is the counters with the limit a new connection would get.
func (s *Server) framesNow() Frames {
	return s.frames.snapshot(s.maxMessageBytes.Load())
}

func handleLimitsGet(_ context.Context, c *conn, _ json.RawMessage) (any, error) {
	return limitsResult{
		Frames:                    c.server.framesNow(),
		ConfiguredMaxMessageBytes: c.server.maxMessageBytes.Load(),
	}, nil
}

// handleLimitsSet persists `[api] max_message_bytes` and applies it to new connections
// (REQ-CLI-007). The daemon owns both the check and the write, so an invalid value never
// reaches the file and `umb` has nothing to roll back.
func handleLimitsSet(_ context.Context, c *conn, raw json.RawMessage) (any, error) {
	var params setLimitsParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, ValidationError("limits.set parameters are not an object")
	}
	if err := config.ValidMaxMessageBytes(params.MaxMessageBytes); err != nil {
		return nil, ValidationError(err.Error(), ErrorField{
			Field: "max_message_bytes", Issue: "bytes, between 1048576 (1 MiB) and 67108864 (64 MiB)",
		})
	}

	s := c.server
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()

	if err := config.WriteMaxMessageBytes(s.cfg.SettingsPath, params.MaxMessageBytes); err != nil {
		if errors.Is(err, config.ErrSettingsInvalid) {
			return nil, fmt.Errorf("%w: %w", ErrConfigInvalid, err)
		}
		return nil, fmt.Errorf("persist the frame limit: %w", err)
	}
	s.maxMessageBytes.Store(int64(params.MaxMessageBytes))
	return setLimitsResult{
		MaxMessageBytes: int64(params.MaxMessageBytes),
		AppliesTo:       appliesToNewConnections,
	}, nil
}

// settingsWritable is limits.set's `available`: the daemon has a settings file to write.
func settingsWritable(cfg Config) bool { return cfg.SettingsPath != "" }
