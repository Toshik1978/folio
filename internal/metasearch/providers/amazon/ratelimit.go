package amazon

import (
	"context"
	"fmt"
	"time"
)

// rateLimiter serializes outbound fallback requests and enforces a minimum
// interval between them. A capacity-1 channel acts as the lock so a caller
// waiting to acquire it still observes ctx cancellation.
type rateLimiter struct {
	sem      chan struct{}
	last     time.Time
	interval time.Duration
}

// newRateLimiter builds a rateLimiter enforcing the given minimum interval.
func newRateLimiter(interval time.Duration) *rateLimiter {
	return &rateLimiter{sem: make(chan struct{}, 1), interval: interval}
}

// wait blocks until at least interval has elapsed since the previous call, or
// until ctx is cancelled. It is ctx-aware both while acquiring the lock and
// while sleeping out the interval.
func (r *rateLimiter) wait(ctx context.Context) error {
	select {
	case r.sem <- struct{}{}:
	case <-ctx.Done():
		return fmt.Errorf("rate limit wait: %w", ctx.Err())
	}
	defer func() { <-r.sem }()

	if d := r.interval - time.Since(r.last); d > 0 && !r.last.IsZero() {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			return fmt.Errorf("rate limit wait: %w", ctx.Err())
		}
	}
	r.last = time.Now()

	return nil
}
