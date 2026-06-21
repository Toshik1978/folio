package events

import "github.com/stretchr/testify/suite"

type subscriptionSuite struct {
	suite.Suite
}

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
