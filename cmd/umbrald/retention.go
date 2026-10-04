package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/ecrespo/umbral/internal/config"
	"github.com/ecrespo/umbral/internal/store"
)

// retentionInterval is how often Data Model §4's maintenance job runs: daily.
const retentionInterval = 24 * time.Hour

// storeRetention hands the store the windows `[retention]` set.
func storeRetention(r config.Retention) store.Retention {
	return store.Retention{
		RawOutputDays: r.RawOutputDays, PlainOutputDays: r.PlainOutputDays,
		ClosedStructureDays: r.ClosedStructureDays, AuditDays: r.AuditDays,
	}
}

// runRetention applies retention once when the daemon starts and then every interval, until
// ctx ends (Data Model §4, T-F1-22). It runs beside the daemon rather than before it serves:
// the first run after a long pause can have a lot to purge, and a client should not wait for
// it. A failed run is logged and the next one tries again; nothing it removes is needed to
// serve.
func runRetention(ctx context.Context, db *store.Store, r store.Retention, interval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		started := time.Now()
		report, err := db.ApplyRetention(ctx, started, r)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			logger.Error("retention failed", slog.Any("error", err))
		default:
			logger.Info("retention applied",
				slog.Int64("raw_chunks", report.RawChunks),
				slog.Int64("transcripts", report.Transcripts),
				slog.Int64("ephemeral_threads", report.EphemeralThreads),
				slog.Int64("closed_structure", report.ClosedStructure),
				slog.Int64("pane_metadata", report.PaneMetadata),
				slog.Int64("audit_rows", report.AuditRows),
				slog.Duration("took", time.Since(started)))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
