package integrations

import "time"

// The report rate of API §8: at most RateLimit kept reports per source in any RateWindow,
// `pane.report_state` and `pane.report_metadata` together (delta `2026-10-wait-monitoring`,
// decision 9).
const (
	RateLimit  = 10
	RateWindow = time.Second
)

// overRate reports whether source already has RateLimit reports kept inside the window, and
// counts the report it refuses with RateLimited. It runs once the report has been validated
// and its pane resolved, so an unknown pane is still NOT_FOUND and an agent's pane still
// PERMISSION_DENIED to a source past its rate. The caller holds s.mu, which also orders it
// with kept.
func (s *Service) overRate(source string) bool {
	now := s.cfg.Now()
	recent := s.rate[source]
	i := 0
	for i < len(recent) && now.Sub(recent[i]) >= RateWindow {
		i++
	}
	s.rate[source] = recent[i:]
	if len(s.rate[source]) < RateLimit {
		return false
	}
	if s.cfg.RateLimited != nil {
		s.cfg.RateLimited()
	}
	return true
}

// kept records a report that was written. A refused, stale or discarded report keeps nothing
// and uses up no place, so a flooding source is heard again as soon as its window slides.
func (s *Service) kept(source string) {
	s.rate[source] = append(s.rate[source], s.cfg.Now())
}
