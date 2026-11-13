package apiserver

import "net/http"

// Long-lived sessions each hold a goroutine set and, for event streams, poll
// the single SQLite connection. Bound them so a runaway client cannot starve
// the appliance. The limits are generous for one learner.
const (
	defaultMaxConsoleSessions = 16
	defaultMaxEventStreams    = 32
)

// sessionLimit is a non-blocking counting semaphore.
type sessionLimit chan struct{}

func newSessionLimit(n int) sessionLimit { return make(sessionLimit, n) }

// acquire reserves a slot or writes a 429 RequestLimitExceeded response.
// The returned release must be called exactly once when acquire succeeds.
func (l sessionLimit) acquire(w http.ResponseWriter, kind, resourceID string) (release func(), ok bool) {
	select {
	case l <- struct{}{}:
		return func() { <-l }, true
	default:
		writeAPIError(w, http.StatusTooManyRequests, "RequestLimitExceeded",
			"too many concurrent "+kind+" sessions; close one and retry", resourceID)
		return nil, false
	}
}

// WithSessionLimits overrides the concurrent console and event-stream limits.
func WithSessionLimits(consoles, events int) Option {
	return func(s *server) {
		if consoles > 0 {
			s.consoleSessions = newSessionLimit(consoles)
		}
		if events > 0 {
			s.eventStreams = newSessionLimit(events)
		}
	}
}
