package ingest

import (
	"context"
	"log/slog"
	"sync"
)

// captureHandler is a minimal slog.Handler that records every emitted record so
// a test can assert on the importer's identifier-override log line.
type captureHandler struct {
	mu      *sync.Mutex
	records *[]slog.Record
}

func (h captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.records = append(*h.records, r.Clone())

	return nil
}

func (h captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h captureHandler) WithGroup(string) slog.Handler { return h }

// overrideRecords returns the captured records whose message is the identifier
// override line, paired with their attributes as a map for easy assertions.
func overrideRecords(recs []slog.Record) []map[string]slog.Value {
	out := make([]map[string]slog.Value, 0)
	for i := range recs {
		if recs[i].Message != msgIdentifierOverride {
			continue
		}
		attrs := make(map[string]slog.Value)
		recs[i].Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value

			return true
		})
		out = append(out, attrs)
	}

	return out
}

func (s *idMatchSuite) newCaptureLogger() (*slog.Logger, *[]slog.Record) {
	var (
		mu   sync.Mutex
		recs []slog.Record
	)

	return slog.New(captureHandler{mu: &mu, records: &recs}), &recs
}

// A record matched onto a book under a DIFFERENT library_key (the over-merge /
// heal signal) emits exactly one identifier-override log line carrying the
// winning book id, both keys, and the deciding identifier.
func (s *idMatchSuite) TestIdentifierOverrideIsLogged() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib")
	logger, recs := s.newCaptureLogger()
	im := newImporter(logger, s.db, s.store, 1)
	defer im.rollback()

	id1, err := im.add(ctx, s.recISBN(lib, "key-cixin-liu", "a.epub", "9781466853454"), 1)
	s.Require().NoError(err)
	_, err = im.add(ctx, s.recISBN(lib, "key-liu-cixin", "b.azw3", "978-1-4668-5345-4"), 1)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	got := overrideRecords(*recs)
	s.Require().Len(got, 1)
	s.Equal(lib.ID, got[0]["library_id"].Int64())
	s.Equal(id1, got[0]["matched_book"].Int64())
	s.Equal("key-liu-cixin", got[0]["record_key"].String())
	s.Equal("key-cixin-liu", got[0]["matched_key"].String())
	s.Equal("isbn", got[0]["identifier_type"].String())
	s.Equal("9781466853454", got[0]["identifier_value"].String())
}

// When the identifier match agrees with the record's own key (no override), no
// log line is emitted — the heal/over-merge signal must not fire on benign hits.
func (s *idMatchSuite) TestIdentifierMatchSameKeyNotLogged() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib")
	logger, recs := s.newCaptureLogger()
	im := newImporter(logger, s.db, s.store, 1)
	defer im.rollback()

	_, err := im.add(ctx, s.recISBN(lib, "same-key", "a.epub", "9781466853454"), 1)
	s.Require().NoError(err)
	_, err = im.add(ctx, s.recISBN(lib, "same-key", "b.azw3", "9781466853454"), 1)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	s.Empty(overrideRecords(*recs))
}
