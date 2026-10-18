package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	intports "github.com/ecrespo/umbral/internal/integrations/ports"
	"github.com/ecrespo/umbral/internal/notify"
	ntfdomain "github.com/ecrespo/umbral/internal/notify/domain"
)

type recordingSink struct{ got []ntfdomain.Request }

func (r *recordingSink) Show(_ context.Context, req ntfdomain.Request) (ntfdomain.Result, error) {
	r.got = append(r.got, req)
	return ntfdomain.Result{Shown: true, Reason: ntfdomain.ReasonShown}, nil
}

type presentClient struct{}

func (presentClient) DeliverForeground(ntfdomain.Notification) bool { return true }

// TestABlockedPaneNotificationNamesWhoAsks_REQ_NTF_001 is decision 15's wording (delta
// `2026-10-notifications`, decision 10): the agent, or the source when no agent is named,
// "needs you"; the report's message is the body, or the pane when there is none; the sound
// is `request`; and the source is the reporter's own.
func TestABlockedPaneNotificationNamesWhoAsks_REQ_NTF_001(t *testing.T) {
	t.Parallel()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	for name, tc := range map[string]struct {
		notice intports.BlockedNotice
		want   ntfdomain.Request
	}{
		"agent and message": {
			intports.BlockedNotice{PaneID: "w1:p2", Source: "claude-code", Agent: "claude", Message: "approve the edit?"},
			ntfdomain.Request{Title: "claude needs you", Body: "approve the edit?", Source: "claude-code", Sound: ntfdomain.SoundRequest},
		},
		"neither agent nor message": {
			intports.BlockedNotice{PaneID: "w1:p2", Source: "codex"},
			ntfdomain.Request{Title: "codex needs you", Body: "pane w1:p2 is waiting for you", Source: "codex", Sound: ntfdomain.SoundRequest},
		},
	} {
		sink := &recordingSink{}
		blockedNotifier{sink: sink, logger: quiet}.NotifyBlocked(t.Context(), tc.notice)
		if len(sink.got) != 1 || sink.got[0] != tc.want {
			t.Errorf("%s: sent %+v, want [%+v]", name, sink.got, tc.want)
		}
	}
}

// TestABlockedPaneSpendsTheReportersBudget_REQ_NTF_002: a hook that keeps blocking spends its
// own 5 a minute, and another source is still heard.
func TestABlockedPaneSpendsTheReportersBudget_REQ_NTF_002(t *testing.T) {
	t.Parallel()
	svc, err := notify.New(notify.Config{Enabled: true})
	if err != nil {
		t.Fatalf("notify.New: %v", err)
	}
	svc.SetDeliverer(presentClient{})
	b := blockedNotifier{sink: svc, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for range ntfdomain.RateLimit {
		b.NotifyBlocked(t.Context(), intports.BlockedNotice{PaneID: "w1:p1", Source: "chatty-hook"})
	}
	if res, _ := svc.Show(t.Context(), ntfdomain.Request{Title: "again", Source: "chatty-hook"}); res.Reason != ntfdomain.ReasonRateLimited {
		t.Errorf("the reporter after %d blocked notices: %+v, want rate_limited", ntfdomain.RateLimit, res)
	}
	if res, _ := svc.Show(t.Context(), ntfdomain.Request{Title: "hi", Source: "other-hook"}); !res.Shown {
		t.Errorf("another source: %+v, want shown", res)
	}
}
