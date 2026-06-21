package events

import (
	"github.com/stretchr/testify/suite"
)

type brokerSuite struct {
	suite.Suite
}

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
