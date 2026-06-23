package api

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"time"

	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/events"
)

type metaSuite struct {
	baseSuite
}

func (s *metaSuite) TestStats() {
	src := s.seedLibrary("folder", "/lib")
	s.seedBook(src, bookSeed{Title: "A", Format: "epub", Authors: []string{"Asimov"}, Series: "Foundation", Size: 100})
	s.seedBook(src, bookSeed{Title: "B", Format: "fb2", Authors: []string{"Clarke"}, Size: 200})
	s.seedBook(src, bookSeed{Title: "C", Format: "epub", Authors: []string{"Lem"}, Lang: "ru", Size: 50})

	w := s.do(http.MethodGet, "/stats", nil)
	s.Require().Equal(http.StatusOK, w.Code)

	var st statsView
	s.decode(w, &st)
	s.Equal(int64(3), st.TotalBooks)
	s.Equal(int64(350), st.TotalSizeBytes)
	s.Equal(int64(3), st.Authors)
	s.Equal(int64(1), st.Series)
	s.Equal(int64(1), st.Libraries)
	s.Equal(int64(2), st.Formats["epub"])
	s.Equal(int64(1), st.Formats["fb2"])
	s.Equal(int64(2), st.Languages["en"]) // A and B default to "en"
	s.Equal(int64(1), st.Languages["ru"])
}

// TestGlobalCountsAndLettersExcludeOrphans covers 1.2: purge/prune delete books
// and cascade the junction rows but leave the authors/series/genres rows behind.
// Those orphans must not inflate the global stats counts or surface phantom
// alphabet buckets.
func (s *metaSuite) TestGlobalCountsAndLettersExcludeOrphans() {
	ctx := context.Background()
	src := s.seedLibrary("folder", "/lib")
	s.seedBook(src, bookSeed{
		Title: "Keeper", Authors: []string{"Asimov"}, Series: "Alpha Saga", Genres: []string{"Adventure"},
	})
	orphan := s.seedBook(src, bookSeed{
		Title: "Goner", Authors: []string{"Zztop"}, Series: "Zephyr Saga", Genres: []string{"Zines"},
	})

	// Cascades the orphan book's junction rows but leaves Zztop / Zephyr Saga /
	// Zines orphaned, all bucketing under 'Z'.
	s.Require().NoError(s.q.DeleteBook(ctx, orphan))

	w := s.do(http.MethodGet, "/stats", nil)
	s.Require().Equal(http.StatusOK, w.Code)
	var st statsView
	s.decode(w, &st)
	s.Equal(int64(1), st.TotalBooks)
	s.Equal(int64(1), st.Authors, "orphaned author must not be counted")
	s.Equal(int64(1), st.Series, "orphaned series must not be counted")

	// The orphans' 'Z' bucket must not appear in any global alphabet selector.
	for _, facet := range []string{"/authors/letters", "/series/letters", "/tags/letters"} {
		lw := s.do(http.MethodGet, facet, nil)
		s.Require().Equal(http.StatusOK, lw.Code)
		var letters []string
		s.decode(lw, &letters)
		s.NotContains(letters, "Z", "orphan must not surface a phantom bucket in %s", facet)
		s.Contains(letters, "A", "the kept book's bucket must still be present in %s", facet)
	}
}

func (s *metaSuite) TestTriggerSyncAndStatus() {
	s.sync.status.Running = true
	s.sync.status.Current = 7

	w := s.do(http.MethodPost, "/sync", nil)
	s.Require().Equal(http.StatusAccepted, w.Code)
	// POST /api/sync is the "Re-index All" action: it forces a full re-read,
	// bypassing checkpoint gating (manual triggers always force).
	s.Equal(1, s.sync.triggeredAllForced)
	s.Zero(s.sync.triggeredAll)

	st := s.do(http.MethodGet, "/sync/status", nil)
	s.Require().Equal(http.StatusOK, st.Code)
	var status struct {
		Running bool  `json:"running"`
		Current int64 `json:"current"`
	}
	s.decode(st, &status)
	s.True(status.Running)
	s.Equal(int64(7), status.Current)
}

func (s *metaSuite) TestStatsErrorHelper() {
	w := httptest.NewRecorder()
	s.catalog.statsError(w, errors.New("test-stats-error"))
	s.Equal(http.StatusInternalServerError, w.Code)
}

func (s *metaSuite) TestWriteJSONError() {
	w := httptest.NewRecorder()
	s.catalog.writeJSON(w, http.StatusOK, make(chan int))
	s.Equal(http.StatusOK, w.Code)
}

func (s *metaSuite) TestSyncEventsInitialSnapshotAndPush() {
	s.sync.status.Running = true
	s.sync.status.Current = 7

	srv := httptest.NewServer(s.router)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/sync/events", http.NoBody)
	s.Require().NoError(err)
	resp, err := http.DefaultClient.Do(req)
	s.Require().NoError(err)
	defer func() { _ = resp.Body.Close() }()

	s.Equal("text/event-stream", resp.Header.Get("Content-Type"))
	s.Equal("no-cache", resp.Header.Get("Cache-Control"))

	// Start a single long-lived goroutine that reads lines from the stream and
	// feeds them into a buffered channel. Sharing one goroutine across both
	// readSSEEvent calls prevents a data race on the underlying bufio.Reader.
	lines := make(chan string, 32)
	go func() {
		r := bufio.NewReader(resp.Body)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				close(lines)
				return
			}

			lines <- strings.TrimRight(line, "\n")
		}
	}()

	// Initial snapshot: a status event reflecting the engine's current state.
	evt, data := s.readSSEEvent(lines)
	s.Equal("status", evt)
	s.Contains(data, `"running":true`)
	s.Contains(data, `"current":7`)

	// A pushed library event must reach the wire.
	s.broker.Publish(events.Event{Type: events.TypeLibrary, Data: map[string]any{"id": 7, "status": "active"}})
	evt, data = s.readSSEEvent(lines)
	s.Equal("library", evt)
	s.Contains(data, `"id":7`)
}

// TestWritePingEmitsNamedEvent pins the heartbeat frame format. The keepalive
// must be a real (named) SSE event, not a bare comment (": ping"): the browser's
// EventSource silently ignores comments, so a comment cannot serve as the
// client's liveness signal. The data: line must also be present and non-empty,
// or EventSource discards the frame without dispatching it.
func (s *metaSuite) TestWritePingEmitsNamedEvent() {
	var buf bytes.Buffer
	s.Require().NoError(writePing(&buf))
	s.Equal("event: ping\ndata: {}\n\n", buf.String())
}

func (s *metaSuite) TestFacets() {
	src1 := s.seedLibrary("folder", "/lib1")
	src2 := s.seedLibrary("folder", "/lib2")

	s.seedBook(src1, bookSeed{Title: "A", Format: "epub", Lang: "en"})
	s.seedBook(src2, bookSeed{Title: "B", Format: "fb2", Lang: "ru"})

	// Test global facets (no library param)
	w := s.do(http.MethodGet, "/facets", nil)
	s.Require().Equal(http.StatusOK, w.Code)
	var f facetsResponse
	s.decode(w, &f)
	s.ElementsMatch([]string{"epub", "fb2"}, f.Formats)
	s.ElementsMatch([]string{"en", "ru"}, f.Languages)

	// Test scoped facets (lib1)
	w = s.do(http.MethodGet, "/facets?library="+strconv.FormatInt(src1, 10), nil)
	s.Require().Equal(http.StatusOK, w.Code)
	var f1 facetsResponse
	s.decode(w, &f1)
	s.Equal([]string{"epub"}, f1.Formats)
	s.Equal([]string{"en"}, f1.Languages)
}

func (s *metaSuite) TestStatsCacheInvalidation() {
	src := s.seedLibrary("folder", "/lib")
	s.seedBook(src, bookSeed{Title: "A", Format: "epub", Size: 100})

	// First call computes and caches
	w := s.do(http.MethodGet, "/stats", nil)
	s.Require().Equal(http.StatusOK, w.Code)
	var st statsView
	s.decode(w, &st)
	s.Equal(int64(1), st.TotalBooks)

	// Inject a book directly into DB without invalidating the API handler's cache
	ctx := context.Background()
	_, err := s.q.InsertBook(ctx, dbq.InsertBookParams{
		LibraryID: src, LibraryKey: "manual-key", Title: "B", AddedAt: time.Now().Unix(),
	})
	s.Require().NoError(err)

	// Cache should still return 1 book
	w = s.do(http.MethodGet, "/stats", nil)
	s.Require().Equal(http.StatusOK, w.Code)
	s.decode(w, &st)
	s.Equal(int64(1), st.TotalBooks)

	// Trigger invalidation manually (to simulate sync completion)
	s.catalog.StatsChanged()

	// Next call should compute updated stats (2 books)
	w = s.do(http.MethodGet, "/stats", nil)
	s.Require().Equal(http.StatusOK, w.Code)
	s.decode(w, &st)
	s.Equal(int64(2), st.TotalBooks)
}

// readSSEEvent reads one domain event:/data: frame from a shared lines channel,
// skipping comment (":") and "retry:" lines as well as heartbeat "ping" events,
// which are transport keepalives rather than domain events.
func (s *metaSuite) readSSEEvent(lines <-chan string) (event, data string) {
	s.T().Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			s.FailNow("timed out waiting for an SSE event")
		case line, ok := <-lines:
			if !ok {
				s.FailNow("stream closed before a full event")
			}
			var complete bool
			if event, data, complete = foldSSELine(line, event, data); complete {
				return event, data
			}
		}
	}
}

// foldSSELine folds one raw stream line into the in-progress (event, data) pair.
// complete is true once a blank line closes a dispatchable domain frame; a blank
// line closing a heartbeat "ping" frame resets the pair and reports incomplete,
// so the caller skips it and keeps reading.
func foldSSELine(line, event, data string) (string, string, bool) {
	switch {
	case strings.HasPrefix(line, "event:"):
		return strings.TrimSpace(strings.TrimPrefix(line, "event:")), data, false
	case strings.HasPrefix(line, "data:"):
		return event, strings.TrimSpace(strings.TrimPrefix(line, "data:")), false
	case line == "" && event == "ping":
		return "", "", false // heartbeat keepalive: reset and keep reading
	case line == "" && event != "":
		return event, data, true
	default:
		return event, data, false
	}
}
