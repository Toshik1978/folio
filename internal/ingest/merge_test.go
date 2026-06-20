package ingest

import (
	"database/sql"

	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

// mergeSuite covers the pure planMerge decision. It needs no database — each case
// builds a bookView + bookRecord literal and asserts on the returned mergePlan —
// so it embeds a plain suite.Suite rather than baseSuite.
type mergeSuite struct {
	suite.Suite
}

// view builds a minimal stored-book view for plan tests.
func (s *mergeSuite) view(format, hash, seriesName string) bookView {
	return bookView{
		ID: 1, Title: "T", Language: "en",
		MetadataFormat: sql.NullString{String: format, Valid: format != ""},
		ContentHash:    hash,
		SeriesName:     seriesName,
	}
}

func (s *mergeSuite) TestHigherPriorityFormatOverwrites() {
	book := s.view("pdf", "old", "")                    // PDF currently owns metadata
	rec := bookRecord{Title: "New", FileFormat: "epub"} // EPUB outranks PDF
	plan := planMerge(book, rec, nil)

	s.True(plan.relations, "higher-priority format must take the overwrite path")
	s.Equal("New", plan.upd.Title, "title must be overwritten")
	s.True(plan.titleFTS, "title overwrite must refresh FTS")
	s.True(plan.bookChanged, "overwrite path must mark the book row changed")
}

func (s *mergeSuite) TestLowerPriorityGapFillsNeverOverwritesTitle() {
	book := s.view("epub", "old", "")                    // EPUB owns metadata
	rec := bookRecord{Title: "Other", FileFormat: "pdf"} // PDF is lower priority
	plan := planMerge(book, rec, nil)

	s.False(plan.relations, "lower-priority edition must gap-fill, not overwrite")
	s.Equal("T", plan.upd.Title, "gap-fill must not touch the title")
	s.False(plan.titleFTS, "gap-fill must not flag a title FTS refresh")
}

func (s *mergeSuite) TestEditedInPlaceOverwritesUnlessSameFormatSibling() {
	book := s.view("epub", "old", "")
	rec := bookRecord{Title: "Retagged", FileFormat: "epub", SourcePath: "a.epub"}
	// rec hashes to something != "old" because Title differs, so editedInPlace fires.
	s.NotEqual(
		contentHash(rec),
		book.ContentHash,
		"pre-condition: rec hash must differ from stored hash for the overwrite path to fire",
	)

	s.Run("no sibling overwrites", func() {
		plan := planMerge(book, rec, []dbq.BookFile{{SourcePath: "a.epub", FileFormat: "epub"}})
		s.True(plan.relations, "in-place edit of the owning edition must overwrite")
	})

	s.Run("same-format sibling suppresses (M8)", func() {
		withSibling := []dbq.BookFile{
			{SourcePath: "a.epub", FileFormat: "epub"},
			{SourcePath: "b.epub", FileFormat: "epub"},
		}
		plan := planMerge(book, rec, withSibling)
		s.False(plan.relations, "same-format sibling must suppress the in-place overwrite")
	})
}

func (s *mergeSuite) TestManualMatchOnlyGapFills() {
	book := s.view("pdf", "old", "")
	book.ManuallyMatched = 1
	rec := bookRecord{Title: "Source", FileFormat: "epub"} // would overwrite if not locked
	plan := planMerge(book, rec, nil)

	s.False(plan.relations, "manually matched book must never be overwritten by sync")
	s.False(plan.titleFTS)
	s.Equal("T", plan.upd.Title, "manual match must preserve the stored title")
}

func (s *mergeSuite) TestSeriesStagedByNameNotID() {
	book := s.view("epub", "old", "Old Series") // currently in "Old Series"
	rec := bookRecord{
		Title: "T", FileFormat: "epub", Series: "New Series",
		SeriesNumber: sql.NullFloat64{Float64: 2, Valid: true},
	}
	// Same-format owner, hash differs (series changed) → overwrite path.
	s.NotEqual(
		contentHash(rec),
		book.ContentHash,
		"pre-condition: rec hash must differ from stored hash for the overwrite path to fire",
	)
	// SourcePath deliberately blank on both rec and its sole file, so hasSameFormatSibling finds no sibling and the
	// in-place overwrite proceeds.
	plan := planMerge(book, rec, []dbq.BookFile{{SourcePath: "", FileFormat: "epub"}})

	s.Equal("New Series", plan.seriesName, "series change must be staged by name")
	s.True(plan.seriesFTS)
}

func (s *mergeSuite) TestGapFillStagesSeriesWhenBookHasNone() {
	book := s.view("epub", "old", "") // seriesless: SeriesID zero value is invalid
	// Lower-priority edition (pdf < epub) → gap-fill path, but it still fills the
	// empty series via fillSeriesPlan.
	rec := bookRecord{
		Title: "T", FileFormat: "pdf", Series: "S",
		SeriesNumber: sql.NullFloat64{Float64: 3, Valid: true},
	}
	plan := planMerge(book, rec, nil)

	s.False(plan.relations, "gap-fill must not take the overwrite/relink path")
	s.Equal("S", plan.seriesName, "an empty series must be filled from the record")
	s.Equal(sql.NullFloat64{Float64: 3, Valid: true}, plan.seriesNumber)
	s.True(plan.seriesFTS, "filling the series must refresh series FTS")
	s.True(plan.bookChanged, "filling the series must mark the book row changed")
}

func (s *mergeSuite) TestSeriesNumberOnlyChangeOnOverwrite() {
	book := s.view("epub", "old", "S") // already in series "S"
	book.SeriesNumber = sql.NullFloat64{Float64: 1, Valid: true}
	// Same owning format and same series NAME, only the number moves 1 → 2.
	rec := bookRecord{
		Title: "T", FileFormat: "epub", Series: "S",
		SeriesNumber: sql.NullFloat64{Float64: 2, Valid: true},
	}
	s.NotEqual(
		contentHash(rec),
		book.ContentHash,
		"pre-condition: rec hash must differ from stored hash for the overwrite path to fire",
	)
	// Sole same-path file, so no same-format sibling suppresses the in-place edit.
	plan := planMerge(book, rec, []dbq.BookFile{{SourcePath: "", FileFormat: "epub"}})

	s.Equal("S", plan.seriesName, "a number-only change must still stage the series")
	s.Equal(sql.NullFloat64{Float64: 2, Valid: true}, plan.seriesNumber, "the new series number must be staged")
	s.True(plan.seriesFTS, "a series number change must refresh series FTS")
}

func (s *mergeSuite) TestGapFillUpgradesUndefinedLanguage() {
	book := s.view("epub", "old", "")
	book.Language = undefinedLanguage // "und" — unknown, eligible for upgrade
	// Lower-priority edition → gap-fill, which still upgrades the unknown language.
	rec := bookRecord{Title: "T", FileFormat: "pdf", Language: "fr"}
	plan := planMerge(book, rec, nil)

	s.False(plan.relations, "gap-fill must not take the overwrite path")
	s.Equal("fr", plan.upd.Language, "an 'und' language must be upgraded from the record")
	s.True(plan.bookChanged, "upgrading the language must mark the book row changed")
}
