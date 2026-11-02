package ingest

import (
	"context"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

type coverStateSuite struct {
	baseSuite
}

// insertBook inserts a minimal library + book row and returns the book id.
// It mirrors backfillSuite.seedBook but needs no library id argument —
// the library is created here too, keeping the test self-contained.
func (s *coverStateSuite) insertBook() int64 {
	lib := s.insertLibrary("folder", "/lib/coverstate")
	q := dbq.New(s.db)
	id, err := q.InsertBook(context.Background(), dbq.InsertBookParams{
		Title: "TestBook", LibraryID: lib.ID, LibraryKey: "coverstate-test",
		Language: "en", ContentHash: "coverstate-test", AddedAt: 1,
	})
	s.Require().NoError(err)

	return id
}

func (s *coverStateSuite) TestGetDefaultsToUnknownThenRoundTrips() {
	id := s.insertBook()

	cs := NewCoverState(s.db)

	state, err := cs.Get(context.Background(), id)
	s.Require().NoError(err)
	s.Equal(int8(0), state) // default unknown

	s.Require().NoError(cs.Set(context.Background(), id, 2)) // none
	state, err = cs.Get(context.Background(), id)
	s.Require().NoError(err)
	s.Equal(int8(2), state)
}

// TestMarkFileCoverRecordsHasAndFilePrio proves a lazily extracted cover is
// recorded as cover_state = has with cover_prio = 1, which marks it as derived
// from the book's own file (and so invalidated when that file changes).
func (s *coverStateSuite) TestMarkFileCoverRecordsHasAndFilePrio() {
	id := s.insertBook()

	s.Require().NoError(NewCoverState(s.db).MarkFileCover(context.Background(), id))

	book, err := dbq.New(s.db).GetBook(context.Background(), id)
	s.Require().NoError(err)
	s.EqualValues(1, book.CoverState)
	s.EqualValues(1, book.CoverPrio)
}
