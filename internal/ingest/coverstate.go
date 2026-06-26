package ingest

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

// CoverState adapts the books.cover_state column to the covers.CoverState
// interface, keeping the covers package a DB-free leaf. It is the cover-state
// twin of Extractor: the composition root injects it into covers.NewStore.
type CoverState struct {
	q *dbq.Queries
}

// NewCoverState builds the cover-state adapter over the folio database.
func NewCoverState(db *sql.DB) *CoverState {
	return &CoverState{q: dbq.New(db)}
}

// Get returns the book's cover-extraction marker (0=unknown, 1=has, 2=none).
func (c *CoverState) Get(ctx context.Context, bookID int64) (int8, error) {
	v, err := c.q.GetCoverState(ctx, bookID)
	if err != nil {
		return 0, fmt.Errorf("get cover state %d: %w", bookID, err)
	}

	return int8(v), nil //nolint:gosec // value domain is 0-2; fits safely in int8
}

// Set records the result of a cover-extraction attempt for the book.
func (c *CoverState) Set(ctx context.Context, bookID int64, state int8) error {
	if err := c.q.SetCoverState(ctx, dbq.SetCoverStateParams{
		CoverState: int64(state),
		ID:         bookID,
	}); err != nil {
		return fmt.Errorf("set cover state %d: %w", bookID, err)
	}

	return nil
}
