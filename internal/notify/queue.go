package notify

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// sendTimeout caps a single delivery attempt, matching the default client
// timeout.
const sendTimeout = 10 * time.Second

// message is one queued notification.
type message struct {
	kind Kind
	text string
}

// enqueue puts m on its lane without blocking. A full lane drops m and records
// the failure; a closed intake drops it silently.
func (s *Service) enqueue(m message) {
	if s.closed.Load() {
		return
	}
	lane := s.batch
	if m.kind == KindCrash {
		lane = s.urgent
	}
	select {
	case lane <- m:
	default:
		s.dropMu.Lock()
		s.dropped++
		n := s.dropped
		s.dropMu.Unlock()
		s.status.recordFailure(s.now(), fmt.Sprintf("queue full: %d message(s) dropped", n))
	}
}

// sender delivers queued messages one at a time until intake is closed and
// both lanes are empty, or ctx is done.
func (s *Service) sender(ctx context.Context) {
	for {
		m, ok := s.next(ctx)
		if !ok {
			return
		}
		if !s.deliver(ctx, m) {
			continue
		}
		if !sleep(ctx, s.timing.Spacing) {
			return
		}
	}
}

// next returns the next message, always preferring the urgent lane. It reports
// false once intake is closed and both lanes are empty, or ctx is done.
func (s *Service) next(ctx context.Context) (message, bool) {
	select {
	case m := <-s.urgent:
		return m, true
	default:
	}
	select {
	case m := <-s.urgent:
		return m, true
	case m := <-s.batch:
		return m, true
	case <-ctx.Done():
		return message{}, false
	case <-s.closing:
		return s.leftover()
	}
}

// leftover takes a message still queued after intake closed, urgent first,
// without blocking.
func (s *Service) leftover() (message, bool) {
	select {
	case m := <-s.urgent:
		return m, true
	default:
	}
	select {
	case m := <-s.batch:
		return m, true
	default:
		return message{}, false
	}
}

// deliver sends m under the Config in force now and records the outcome. It
// reports whether a send was attempted.
func (s *Service) deliver(ctx context.Context, m message) bool {
	cfg := s.cfg.Load()
	if cfg == nil || !cfg.Enabled || !cfg.Ready() {
		return false
	}
	err := s.sendWithRetry(ctx, *cfg, m.text)
	switch {
	case err == nil:
		s.dropMu.Lock()
		s.dropped = 0
		s.dropMu.Unlock()
		s.status.recordSuccess(s.now())
	case ctx.Err() != nil:
		// Shutting down: not a delivery failure.
	default:
		s.log.Warn("telegram delivery failed", "kind", m.kind, "error", err.Error())
		s.status.recordFailure(s.now(), err.Error())
	}

	return true
}

// sendWithRetry makes up to 1+len(Backoff) attempts inside MessageBudget. It
// returns the last error when the message is dropped.
func (s *Service) sendWithRetry(ctx context.Context, cfg Config, text string) error {
	deadline := time.Now().Add(s.timing.MessageBudget)
	budgetCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	for i := 0; ; i++ {
		attemptCtx, cancelAttempt := context.WithTimeout(budgetCtx, sendTimeout)
		err := s.client.Send(attemptCtx, cfg, text)
		cancelAttempt()
		if err == nil || ctx.Err() != nil || i >= len(s.timing.Backoff) {
			return err
		}
		retry, retryAfter := retryable(err)
		if !retry || retryAfter > s.timing.MaxRetryAfter {
			return err
		}
		wait := s.timing.Backoff[i]
		if isRateLimit(err) {
			wait = max(retryAfter, wait)
		}
		if time.Until(deadline) < wait || !sleep(budgetCtx, wait) {
			return err
		}
	}
}

func isRateLimit(err error) bool {
	ae, ok := errors.AsType[*APIError](err)

	return ok && ae.Code == http.StatusTooManyRequests
}

// sleep waits d and reports false when ctx ends first.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
