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

// maxFloodPause caps how long one 429 can pause the sender. A longer flood
// ban is not lost: the next send gets a fresh 429 with the time remaining.
const maxFloodPause = 10 * time.Minute

// message is one queued notification.
type message struct {
	kind Kind
	text string
}

// enqueue puts m on its lane without blocking and reports whether it did. A
// full lane drops m and records the failure; a closed intake drops it silently.
func (s *Service) enqueue(m message) bool {
	if s.closed.Load() {
		return false
	}
	lane := s.batch
	if m.kind == KindCrash {
		lane = s.urgent
	}
	select {
	case lane <- m:
		return true
	default:
		s.dropMu.Lock()
		s.dropped++
		n := s.dropped
		s.dropMu.Unlock()
		s.status.recordFailure(s.now(), fmt.Sprintf("queue full: %d message(s) dropped", n))

		return false
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
		attempted, err := s.deliver(ctx, m)
		if !attempted {
			continue
		}
		if s.drained() {
			// Nothing left to space out: on shutdown the gap would only eat
			// into Stop's drain budget.
			return
		}
		if !sleep(ctx, s.gapAfter(err)) {
			return
		}
	}
}

// gapAfter is the wait before the next send. After a message dropped on a 429
// it is Telegram's retry_after, so the queued messages wait out the flood ban
// in their (bounded) lanes instead of each being sent into it, dropped, and
// prolonging the ban; otherwise it is Spacing.
func (s *Service) gapAfter(err error) time.Duration {
	ae, ok := errors.AsType[*APIError](err)
	if !ok || ae.Code != http.StatusTooManyRequests {
		return s.timing.Spacing
	}

	return max(s.timing.Spacing, min(ae.RetryAfter, maxFloodPause))
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

// drained reports whether intake is closed and both lanes are empty, so no
// message can follow: enqueue refuses once closed is set (a send racing that
// store is as lost as one made after Stop).
func (s *Service) drained() bool {
	return s.closed.Load() && len(s.urgent) == 0 && len(s.batch) == 0
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
// reports whether a send was attempted and, when it failed, the final error.
func (s *Service) deliver(ctx context.Context, m message) (attempted bool, err error) {
	cfg := s.cfg.Load()
	if cfg == nil || !cfg.Enabled || !cfg.Ready() {
		return false, nil
	}
	err = s.sendWithRetry(ctx, *cfg, m.text)
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

	return true, err
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
