package workspaces

import (
	"context"
	"log/slog"
	"time"
)

// CaptureInterval is how often pane screens are stored while REQ-TERM-010 is on.
//
// The requirement sets it: "Capture happens every 10 s and on clean shutdown, so a crash
// loses at most the last window." The second half is why the number is in the requirement
// rather than left to taste — it bounds what a user loses, and a different interval would be
// a different promise.
const CaptureInterval = 10 * time.Second

// PrepareHistory brings the stored screens into line with the setting, once, at start.
//
// Turning pane history off deletes what was captured (Data Model §2.4d). That is a privacy
// promise and not housekeeping: a user who turns it off is asking for what was recorded to
// stop existing, and keeping the rows "in case they turn it back on" answers a question they
// did not ask.
func (s *Service) PrepareHistory(ctx context.Context) error {
	if s.paneHistory {
		return nil
	}
	return s.tree.ForgetScreens(ctx)
}

// CaptureScreens stores the current screen of every open pane (REQ-TERM-010).
//
// It is a no-op unless the setting is on, checked here rather than at the call site so that
// neither the ticker nor the shutdown path can forget.
func (s *Service) CaptureScreens(ctx context.Context) {
	if !s.paneHistory || s.screens == nil {
		return
	}

	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		s.log.Warn("could not read the tree to capture screens", slog.String("error", err.Error()))
		return
	}

	var stored int
	now := time.Now().UTC().UnixMilli()
	for _, pane := range snapshot.Panes {
		if pane.SessionID == "" {
			continue
		}
		screen, err := s.screens.Screen(ctx, pane.SessionID)
		if err != nil {
			// A pane whose session just exited is the ordinary case, not a fault.
			s.log.Debug("no screen to capture",
				slog.String("pane_id", pane.ID), slog.String("error", err.Error()))
			continue
		}
		if len(screen.Data) == 0 {
			continue
		}
		if err := s.tree.SaveScreen(ctx, pane.ID, screen.Data, screen.Rows, now); err != nil {
			s.log.Warn("could not store a pane's screen",
				slog.String("pane_id", pane.ID), slog.String("error", err.Error()))
			continue
		}
		stored++
	}
	s.log.Debug("pane screens captured", slog.Int("panes", stored))
}

// RunCapture drives the ticker until the context ends, then captures once more.
//
// The final capture is the "on clean shutdown" half of REQ-TERM-010, and it uses a context of
// its own: the one that just ended is the reason this function returned, and reusing it would
// cancel the very write the requirement asks for.
func (s *Service) RunCapture(ctx context.Context) {
	if !s.paneHistory || s.screens == nil {
		return
	}

	ticker := time.NewTicker(CaptureInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.CaptureScreens(ctx)
		case <-ctx.Done():
			final, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			s.CaptureScreens(final)
			return
		}
	}
}

// restoreScreen returns what a pane should show before its new shell writes anything.
func (s *Service) restoreScreen(ctx context.Context, paneID string) []byte {
	if !s.paneHistory {
		return nil
	}
	screen, err := s.tree.LoadScreen(ctx, paneID)
	if err != nil {
		s.log.Warn("could not read a pane's stored screen",
			slog.String("pane_id", paneID), slog.String("error", err.Error()))
		return nil
	}
	return screen
}
