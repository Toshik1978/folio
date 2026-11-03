package notify

import (
	"sync"
	"time"
)

// DeliveryStatus is the outcome of the most recent Telegram deliveries.
type DeliveryStatus struct {
	LastSuccessAt    *time.Time
	LastFailureAt    *time.Time
	LastFailureError string
}

// statusBox holds the DeliveryStatus behind a mutex. A failure never clears
// the last success, and a success never clears the last failure.
type statusBox struct {
	mu sync.Mutex
	st DeliveryStatus
}

func (b *statusBox) recordSuccess(t time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.st.LastSuccessAt = &t
}

func (b *statusBox) recordFailure(t time.Time, msg string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.st.LastFailureAt = &t
	b.st.LastFailureError = msg
}

// snapshot returns a copy; the caller may not mutate the box through it.
func (b *statusBox) snapshot() DeliveryStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.st
	if out.LastSuccessAt != nil {
		t := *out.LastSuccessAt
		out.LastSuccessAt = &t
	}
	if out.LastFailureAt != nil {
		t := *out.LastFailureAt
		out.LastFailureAt = &t
	}

	return out
}
