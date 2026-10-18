package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ecrespo/umbral/internal/bus"
	intports "github.com/ecrespo/umbral/internal/integrations/ports"
	ntfdomain "github.com/ecrespo/umbral/internal/notify/domain"
	ntfports "github.com/ecrespo/umbral/internal/notify/ports"
	secports "github.com/ecrespo/umbral/internal/security/ports"
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

// ownNotifier is the sink as the daemon's own components reach it.
type ownNotifier interface {
	ShowOwn(ctx context.Context, req ntfdomain.Request) (ntfdomain.Result, error)
}

// notifyRuleRejections turns each `rules.update_rejected` into a user notification under the
// daemon's `umbral:rules` source (REQ-SEC-013's "notify the user"; delta
// `2026-10-signed-rule-bundles`). The subscription is taken before the rule manager starts, so
// a rejection at the first check is not lost while the sink is being built.
func notifyRuleRejections(ctx context.Context, sub *bus.Subscription, sink ownNotifier, logger *slog.Logger) {
	defer sub.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-sub.C():
			e, ok := ev.(secports.RuleUpdateRejected)
			if !ok {
				continue
			}
			res, err := sink.ShowOwn(ctx, ntfdomain.Request{
				Title:  "Rule update rejected",
				Body:   fmt.Sprintf("A signed rule bundle was discarded (%s); the rules in force stay. See umb rules status.", e.Reason),
				Source: "umbral:rules", Sound: ntfdomain.SoundRequest,
			})
			if err != nil {
				logger.WarnContext(ctx, "a rule rejection could not be notified", slog.Any("error", err))
				continue
			}
			logger.DebugContext(ctx, "rule rejection notified", slog.String("reason", string(res.Reason)))
		}
	}
}
