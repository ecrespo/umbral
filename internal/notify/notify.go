// Package notify is the notification sink of Tech Design §5.2: it normalizes what a
// component asks to show, holds each source to REQ-NTF-002's rate, and hands the result to
// the foreground client (REQ-NTF-001, API Spec §5.35). It imports no other module.
package notify

import (
	"context"
	"sync"
	"time"

	"github.com/ecrespo/umbral/internal/notify/domain"
	"github.com/ecrespo/umbral/internal/notify/ports"
)

// Config is what the service needs.
type Config struct {
	// Enabled is `[notifications] enabled`. Off, every request answers `disabled`.
	Enabled bool
	Now     func() time.Time
}

// Service implements ports.Notifier.
type Service struct {
	cfg Config

	// mu guards the windows and the deliverer, so a burst from one source is counted
	// exactly and never shows a sixth notification.
	mu        sync.Mutex
	windows   map[string][]time.Time
	deliverer ports.Deliverer
}

var _ ports.Notifier = (*Service)(nil)

// New builds the service.
func New(cfg Config) (*Service, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, windows: map[string][]time.Time{}}, nil
}

// SetDeliverer gives the service its way to the clients. It is a setter because the API
// server, which implements the deliverer, is built after the service it serves.
func (s *Service) SetDeliverer(d ports.Deliverer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deliverer = d
}

// Show implements ports.Notifier.
func (s *Service) Show(ctx context.Context, req domain.Request) (domain.Result, error) {
	n, err := req.Build()
	if err != nil {
		return domain.Result{}, err
	}
	return s.show(n), nil
}

// show applies the checks in their order: disabled, then the source's rate, then whether a
// client is there to receive it. A request that reaches the rate check counts against its
// source whether or not a client receives it — the limit is on what a source asks for.
//
// The count is settled under the lock and the delivery made after it, so a client slow to
// take a notification never holds up another source (REQ-NTF-002).
func (s *Service) show(n domain.Notification) domain.Result {
	if !s.cfg.Enabled {
		return domain.Result{Reason: domain.ReasonDisabled}
	}
	deliverer, admitted := s.admit(n.Source)
	if !admitted {
		return domain.Result{Reason: domain.ReasonRateLimited}
	}
	if deliverer == nil || !deliverer.DeliverForeground(n) {
		return domain.Result{Reason: domain.ReasonNoClient}
	}
	return domain.Result{Shown: true, Reason: domain.ReasonShown}
}

// admit counts one request against its source's sliding window, unless the window is full,
// and returns the deliverer as it stands.
func (s *Service) admit(source string) (ports.Deliverer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.cfg.Now()
	recent := s.windows[source][:0]
	for _, at := range s.windows[source] {
		if now.Sub(at) < domain.RateWindow {
			recent = append(recent, at)
		}
	}
	if len(recent) >= domain.RateLimit {
		s.windows[source] = recent
		return nil, false
	}
	s.windows[source] = append(recent, now)
	s.forgetIdle(now)
	return s.deliverer, true
}

// forgetIdle drops sources with nothing left in the window, so a daemon that hears from
// many short-lived sources does not keep a key for each one forever.
func (s *Service) forgetIdle(now time.Time) {
	for source, times := range s.windows {
		if len(times) == 0 || now.Sub(times[len(times)-1]) >= domain.RateWindow {
			delete(s.windows, source)
		}
	}
}
