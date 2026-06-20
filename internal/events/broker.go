package events

import "sync"

const (
	// maxPending caps a subscriber's buffer. Reliable events are infrequent and
	// progress events coalesce to one entry per library, so only a genuinely stuck
	// client hits this; on overflow the broker recycles the subscription.
	maxPending = 64
	// maxSubscribers caps concurrent streams so a buggy client cannot open them
	// without bound.
	maxSubscribers = 128
)

// Subscription is one client's bounded, coalescing event buffer. The SSE handler
// owns it: it waits on C(), Drains(), and writes to the wire. The broker never
// spawns goroutines of its own.
type Subscription struct {
	mu      sync.Mutex
	pending []Event
	wake    chan struct{}
	done    chan struct{}
	closed  bool
}

func newSubscription() *Subscription {
	return &Subscription{
		wake: make(chan struct{}, 1),
		done: make(chan struct{}),
	}
}

// C is the wake signal: a receive means there is likely at least one event to
// Drain. The cap-1 wake may occasionally fire with nothing buffered (a coalescing
// offer can leave a stale token after a drain), so consumers must tolerate an
// empty Drain() result.
func (s *Subscription) C() <-chan struct{} { return s.wake }

// Done is closed when the broker recycles this subscription (overflow or
// Unsubscribe), letting the handler exit its loop.
func (s *Subscription) Done() <-chan struct{} { return s.done }

// Drain returns and clears the buffered events.
func (s *Subscription) Drain() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.pending
	s.pending = nil
	return out
}

// offer buffers ev applying coalescing. It returns false when the subscriber has
// overflowed maxPending (the broker then recycles it). A closed subscription
// drops silently and reports success (it is already being torn down).
func (s *Subscription) offer(ev Event) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return true
	}
	if ev.CoalesceKey != "" {
		for i := range s.pending {
			if s.pending[i].CoalesceKey == ev.CoalesceKey {
				s.pending[i] = ev // overwrite in place, keep position
				s.signal()
				return true
			}
		}
	}
	if len(s.pending) >= maxPending {
		return false
	}
	s.pending = append(s.pending, ev)
	s.signal()

	return true
}

// signal nudges the handler without blocking (the channel has capacity 1).
func (s *Subscription) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// close marks the subscription dead and unblocks the handler. Idempotent.
func (s *Subscription) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.pending = nil
	close(s.done)
}

// Broker fans events out to all current subscribers. It owns no goroutines; the
// per-connection SSE handler drives delivery. Safe for concurrent use.
type Broker struct {
	mu     sync.RWMutex
	subs   map[*Subscription]struct{}
	closed bool
}

// NewBroker returns an empty broker ready for Subscribe/Publish.
func NewBroker() *Broker {
	return &Broker{subs: make(map[*Subscription]struct{})}
}

// Subscribe registers a new subscription. ok is false when maxSubscribers is
// reached or the broker is closed; the caller should answer 503 or 500.
func (b *Broker) Subscribe() (sub *Subscription, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, false
	}
	if len(b.subs) >= maxSubscribers {
		return nil, false
	}
	sub = newSubscription()
	b.subs[sub] = struct{}{}

	return sub, true
}

// Unsubscribe removes and closes a subscription. Idempotent.
func (b *Broker) Unsubscribe(sub *Subscription) {
	b.mu.Lock()
	_, present := b.subs[sub]
	delete(b.subs, sub)
	b.mu.Unlock()
	if present {
		sub.close()
	}
}

// Publish delivers ev to every subscriber, recycling any that overflow. It is
// non-blocking and does no IO, so it is safe to call from hot paths.
func (b *Broker) Publish(ev Event) {
	var overflowed []*Subscription
	b.mu.RLock()
	for sub := range b.subs {
		if !sub.offer(ev) {
			overflowed = append(overflowed, sub)
		}
	}
	b.mu.RUnlock()
	// Recycle outside the read lock to avoid upgrading to the write lock while held.
	for _, sub := range overflowed {
		b.Unsubscribe(sub)
	}
}

// Close closes all subscriptions and prevents new ones. Idempotent.
func (b *Broker) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	for sub := range b.subs {
		sub.close()
	}
	b.subs = make(map[*Subscription]struct{})
	b.mu.Unlock()
}
