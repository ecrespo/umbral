package main

import (
	"context"
	"log/slog"

	intports "github.com/ecrespo/umbral/internal/integrations/ports"
	ntfdomain "github.com/ecrespo/umbral/internal/notify/domain"
	ntfports "github.com/ecrespo/umbral/internal/notify/ports"
)

// blockedNotifier turns an external `blocked` into a user notification (delta
// `2026-10-integration-surface`, decision 15). It is the composition root's adapter between
// two modules that may not import each other: integrations names what happened, notify
// normalizes and rate-limits it under the reporter's own source (REQ-NTF-002).
type blockedNotifier struct {
	sink   ntfports.Notifier
	logger *slog.Logger
}

func (b blockedNotifier) NotifyBlocked(ctx context.Context, n intports.BlockedNotice) {
	who := n.Agent
	if who == "" {
		who = n.Source
	}
	body := n.Message
	if body == "" {
		body = "pane " + n.PaneID + " is waiting for you"
	}
	res, err := b.sink.Show(ctx, ntfdomain.Request{
		Title: who + " needs you", Body: body, Source: n.Source, Sound: ntfdomain.SoundRequest,
	})
	if err != nil {
		b.logger.WarnContext(ctx, "a blocked pane could not be notified", slog.Any("error", err))
		return
	}
	b.logger.DebugContext(ctx, "blocked pane notified",
		slog.String("pane_id", n.PaneID), slog.String("reason", string(res.Reason)))
}
