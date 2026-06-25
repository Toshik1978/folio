package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
	"github.com/Toshik1978/folio/internal/metasearch"
)

// BookLookup builds a metasearch query from a book in the folio database: its
// title, first author, and first ISBN identifier. It satisfies
// metasearch.BookLookup, the seam the Coordinator uses to auto-enrich.
type BookLookup struct {
	db *sql.DB
}

// NewBookLookup builds a BookLookup over the folio database.
func NewBookLookup(db *sql.DB) *BookLookup { return &BookLookup{db: db} }

// Lookup returns the enrich query for bookID. ok is false when the book is gone.
func (l *BookLookup) Lookup(ctx context.Context, bookID int64) (metasearch.Query, bool, error) {
	q := dbq.New(l.db)
	book, err := q.GetBook(ctx, bookID)
	if errors.Is(err, sql.ErrNoRows) {
		return metasearch.Query{}, false, nil
	}
	if err != nil {
		return metasearch.Query{}, false, fmt.Errorf("get book: %w", err)
	}

	out := metasearch.Query{Title: book.Title}

	ids, err := q.ListIdentifiersForBook(ctx, bookID)
	if err != nil {
		return metasearch.Query{}, false, fmt.Errorf("list identifiers: %w", err)
	}
	ApplyIdentifierQuery(&out, ids)

	authors, err := q.ListAuthorsForBook(ctx, bookID)
	if err != nil {
		return metasearch.Query{}, false, fmt.Errorf("list authors: %w", err)
	}
	if len(authors) > 0 {
		out.Author = authors[0].Name
	}

	return out, true, nil
}

// ApplyIdentifierQuery fills q.ISBN and q.ASIN from a book's identifier rows,
// taking the first of each type. It is the single source of truth for mapping
// stored identifiers onto a provider query, shared by BookLookup (auto-enrich) and
// the API cover-search seed so the two never drift.
func ApplyIdentifierQuery(q *metasearch.Query, ids []dbq.ListIdentifiersForBookRow) {
	for _, id := range ids {
		switch id.Type {
		case ebook.IdentifierISBN:
			if q.ISBN == "" {
				q.ISBN = id.Value
			}
		case ebook.IdentifierAmazon:
			if q.ASIN == "" {
				q.ASIN = id.Value
			}
		}
	}
}
