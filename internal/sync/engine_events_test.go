package sync

import (
	stdsync "sync"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/events"
)

type recordingPublisher struct {
	mu  stdsync.Mutex
	got []events.Event
}

func (p *recordingPublisher) Publish(ev events.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.got = append(p.got, ev)
}

func (p *recordingPublisher) events() []events.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]events.Event, len(p.got))
	copy(out, p.got)
	return out
}

// libraryIDs returns the IDs carried by every TypeLibrary event, in order.
func (p *recordingPublisher) libraryIDs() []int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	var ids []int64
	for _, ev := range p.got {
		if ev.Type == events.TypeLibrary {
			if event, ok := ev.Data.(libraryEvent); ok {
				ids = append(ids, event.ID)
			}
		}
	}

	return ids
}

type engineEventsSuite struct{ suite.Suite }

func TestEngineEventsSuite(t *testing.T) { suite.Run(t, new(engineEventsSuite)) }

func (s *engineEventsSuite) newEngine(p Publisher) *Engine {
	return &Engine{
		events: p,
		wake:   make(chan struct{}, 1),
		queued: map[int64]bool{},
	}
}

func (s *engineEventsSuite) TestEnqueueEmitsStatus() {
	rec := &recordingPublisher{}
	e := s.newEngine(rec)

	e.enqueue(syncReq{id: 7})

	got := rec.events()
	s.Require().Len(got, 1)
	s.Equal(events.TypeStatus, got[0].Type)
	s.Equal("status", got[0].CoalesceKey)
	st, ok := got[0].Data.(Status)
	s.True(ok)
	s.False(st.Running)
	s.Equal([]int64{7}, st.Queued)
}

func (s *engineEventsSuite) TestEmitLibrary() {
	rec := &recordingPublisher{}
	e := s.newEngine(rec)

	e.emitLibrary(7, "error")

	got := rec.events()
	s.Require().Len(got, 1)
	s.Equal(events.TypeLibrary, got[0].Type)
	s.Empty(got[0].CoalesceKey)
	le, ok := got[0].Data.(libraryEvent)
	s.True(ok)
	s.Equal(int64(7), le.ID)
	s.Equal("error", le.Status)
}

func (s *engineEventsSuite) TestNilPublisherIsNoOp() {
	e := s.newEngine(nil)
	s.NotPanics(func() {
		e.emitStatus()
		e.emitLibrary(1, "active")
	})
}
