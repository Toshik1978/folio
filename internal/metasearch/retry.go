package metasearch

import (
	"context"
	"fmt"
	"time"
)

// RetryCovers calls fn up to attempts times, returning the first NON-EMPTY
// result. It retries when fn errors OR returns zero candidates (a transient
// anti-bot/interstitial response looks like an empty parse). Between attempts it
// waits backoff*attemptIndex, honoring ctx cancellation. If every attempt is
// empty-without-error it returns (nil, nil); if attempts errored it returns the
// last error.
func RetryCovers(
	ctx context.Context,
	attempts int,
	backoff time.Duration,
	fn func(context.Context) ([]CoverCandidate, error),
) ([]CoverCandidate, error) {
	var lastErr error

	for i := range attempts {
		if i > 0 {
			select {
			case <-time.After(backoff * time.Duration(i)):
			case <-ctx.Done():
				return nil, fmt.Errorf("retry cancelled: %w", ctx.Err())
			}
		}

		out, err := fn(ctx)
		if err != nil {
			lastErr = err

			continue
		}

		if len(out) > 0 {
			return out, nil
		}
	}

	return nil, lastErr
}
