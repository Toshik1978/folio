package amazon

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// rateLimiter serializes outbound fallback requests and enforces a minimum
// interval between them, so the multi-hop fallback stays polite. Holding the
// mutex across the wait is intentional: it serializes concurrent callers.
type rateLimiter struct {
	mu       sync.Mutex
	last     time.Time
	interval time.Duration
}

// wait blocks until at least interval has elapsed since the previous call, or
// until ctx is cancelled.
func (r *rateLimiter) wait(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

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
