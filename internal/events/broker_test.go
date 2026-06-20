package events

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

type subscriptionSuite struct{ suite.Suite }

func TestSubscriptionSuite(t *testing.T) { suite.Run(t, new(subscriptionSuite)) }

func (s *subscriptionSuite) TestReliableEventsKeepOrder() {
	sub := newSubscription()
	s.True(sub.offer(Event{Type: TypeLibrary, Data: 1}))
	s.True(sub.offer(Event{Type: TypeLibrary, Data: 2}))

	got := sub.Drain()
	s.Require().Len(got, 2)
	s.Equal(1, got[0].Data)
	s.Equal(2, got[1].Data)
}

func (s *subscriptionSuite) TestCoalesceKeyKeepsLatest() {
	sub := newSubscription()
	s.True(sub.offer(Event{Type: TypeStatus, CoalesceKey: "status", Data: "a"}))
	s.True(sub.offer(Event{Type: TypeStatus, CoalesceKey: "status", Data: "b"}))

	got := sub.Drain()
	s.Require().Len(got, 1)
	s.Equal("b", got[0].Data)
}

func (s *subscriptionSuite) TestOverflowReturnsFalse() {
	sub := newSubscription()
	for i := range maxPending {
		s.True(sub.offer(Event{Type: TypeLibrary, Data: i}))
	}
	s.False(sub.offer(Event{Type: TypeLibrary, Data: 999}), "one past the cap must signal overflow")
}

func (s *subscriptionSuite) TestClosedSubscriptionDropsSilently() {
	sub := newSubscription()
	sub.close()
	s.True(
		sub.offer(Event{Type: TypeStatus, CoalesceKey: "status", Data: "x"}),
		"closed sub is a no-op, not an overflow",
	)
	s.Empty(sub.Drain())
}

func (s *subscriptionSuite) TestCoalesceSucceedsAtCap() {
	sub := newSubscription()
	// Fill to the cap with same-key coalescable events; they collapse to one entry.
	for i := range maxPending + 5 {
		s.True(sub.offer(Event{Type: TypeProgress, CoalesceKey: "progress:1", Data: i}))
	}
	got := sub.Drain()
	s.Require().Len(got, 1, "same-key events must coalesce to a single entry regardless of count")
	s.Equal(maxPending+4, got[0].Data, "the latest value wins")
}

func (s *subscriptionSuite) TestCoalescePreservesPosition() {
	sub := newSubscription()
	s.True(sub.offer(Event{Type: TypeStatus, CoalesceKey: "status", Data: "A"}))
	s.True(sub.offer(Event{Type: TypeLibrary, Data: "B"})) // reliable, no key
	s.True(sub.offer(Event{Type: TypeStatus, CoalesceKey: "status", Data: "A2"}))

	got := sub.Drain()
	s.Require().Len(got, 2)
	s.Equal("A2", got[0].Data, "coalesced status keeps its original position")
	s.Equal("B", got[1].Data)
}

type brokerSuite struct{ suite.Suite }

func TestBrokerSuite(t *testing.T) { suite.Run(t, new(brokerSuite)) }

func (s *brokerSuite) TestPublishFansOut() {
	b := NewBroker()
	sub, ok := b.Subscribe()
	s.Require().True(ok)

	b.Publish(Event{Type: TypeStatus, CoalesceKey: "status", Data: "x"})
	got := sub.Drain()
	s.Require().Len(got, 1)
	s.Equal("x", got[0].Data)
}

func (s *brokerSuite) TestUnsubscribeIsIdempotentAndStopsDelivery() {
	b := NewBroker()
	sub, _ := b.Subscribe()
	other, _ := b.Subscribe() // stays registered; proves fan-out is otherwise intact
	b.Unsubscribe(sub)
	b.Unsubscribe(sub) // must not panic

	select {
	case <-sub.Done():
	default:
		s.Fail("Done() must be closed after Unsubscribe")
	}
	b.Publish(Event{Type: TypeStatus, Data: "y"})
	s.Empty(sub.Drain(), "unsubscribed sub must receive nothing")

	got := other.Drain()
	s.Require().Len(got, 1, "a still-registered sub must keep receiving after another is unsubscribed")
	s.Equal("y", got[0].Data)
}

func (s *brokerSuite) TestOverflowRecyclesSlowSubscriber() {
	b := NewBroker()
	sub, _ := b.Subscribe()
	for i := range maxPending + 5 {
		b.Publish(Event{Type: TypeLibrary, Data: i})
	}
	select {
	case <-sub.Done():
	default:
		s.Fail("an overflowed subscriber must be recycled (Done closed)")
	}
}

func (s *brokerSuite) TestMaxSubscribers() {
	b := NewBroker()
	for range maxSubscribers {
		_, ok := b.Subscribe()
		s.Require().True(ok)
	}
	_, ok := b.Subscribe()
	s.False(ok, "subscribing past the cap must fail")
}

func (s *brokerSuite) TestClose() {
	b := NewBroker()
	sub1, ok1 := b.Subscribe()
	s.Require().True(ok1)
	sub2, ok2 := b.Subscribe()
	s.Require().True(ok2)

	b.Close()

	// Both active subscriptions should be closed/Done.
	select {
	case <-sub1.Done():
	default:
		s.Fail("sub1 must be closed after broker Close")
	}
	select {
	case <-sub2.Done():
	default:
		s.Fail("sub2 must be closed after broker Close")
	}

	// New subscriptions must be rejected.
	_, ok := b.Subscribe()
	s.False(ok, "subscribing to closed broker must fail")

	// Idempotent Close.
	b.Close()
}
