package sync

import (
	"encoding/json"
	stdsync "sync"

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

type engineEventsSuite struct {
	suite.Suite
}

func (s *engineEventsSuite) newEngine(p Publisher) *Engine {
	return &Engine{
		events: p,
		wake:   make(chan struct{}, 1),
		queued: map[reqKey]bool{},
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

func (s *engineEventsSuite) TestEnqueueDedupsPerKind() {
	e := s.newEngine(nil)

	e.enqueue(
		syncReq{id: 7},
		syncReq{id: 7, kind: kindDeepIndex},
		syncReq{id: 7},
		syncReq{id: 7, kind: kindDeepIndex},
	)

	s.Equal([]syncReq{{id: 7}, {id: 7, kind: kindDeepIndex}}, e.queue)
	s.Equal([]int64{7, 7}, e.Status().Queued)
}

func (s *engineEventsSuite) TestForceUpgradeAppliesOnlyToSync() {
	e := s.newEngine(nil)

	e.enqueue(syncReq{id: 7, kind: kindDeepIndex}, syncReq{id: 7})
	e.enqueue(syncReq{id: 7, force: true})

	s.Equal([]syncReq{{id: 7, kind: kindDeepIndex}, {id: 7, force: true}}, e.queue)
}

func (s *engineEventsSuite) TestStatusKindInPayload() {
	rec := &recordingPublisher{}
	e := s.newEngine(rec)

	e.current, e.currentKind = 2, kindDeepIndex
	e.enqueue(syncReq{id: 1})

	got := rec.events()
	s.Require().Len(got, 1)
	raw, err := json.Marshal(got[0].Data)
	s.Require().NoError(err)
	s.JSONEq(`{"running":true,"current":2,"kind":"deep_index","queued":[1]}`, string(raw))

	e.current = 0
	raw, err = json.Marshal(e.Status())
	s.Require().NoError(err)
	s.JSONEq(`{"running":false,"queued":[1]}`, string(raw), "kind is omitted when idle")
}
