package opds

import (
	"context"
	"sync"
	"time"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

// opdsBackfillWorkers bounds the offline-backfill fan-out over a feed page's
// books, and opdsBackfillBudget caps the whole pass so a cold page never hangs a
// reader app: stragglers leave metadata_checked unset and enrich on a later load.
const (
	opdsBackfillWorkers = 6
	opdsBackfillBudget  = 5 * time.Second
)

// backfillPage runs the offline metadata tier over the page's not-yet-checked
// books (bounded-parallel, budgeted), then re-reads the filled rows so entries
// render the fresh annotation/identifiers. Best-effort throughout; a fill or
// re-read error leaves that book's existing row in place. It mutates books in
// place. A nil filler is a no-op.
func (h *Handler) backfillPage(ctx context.Context, books []dbq.Book) {
	if h.filler == nil {
		return
	}

	todo := uncheckedBookIDs(books)
	if len(todo) == 0 {
		return
	}

	bctx, cancel := context.WithTimeout(ctx, opdsBackfillBudget)
	defer cancel()

	h.fillBooks(bctx, todo)
	h.rereadBooks(ctx, books, todo)
}

// uncheckedBookIDs collects the ids of books whose offline metadata has not yet
// been checked (metadata_checked == 0).
func uncheckedBookIDs(books []dbq.Book) []int64 {
	todo := make([]int64, 0, len(books))
	for i := range books {
		if books[i].MetadataChecked == 0 {
			todo = append(todo, books[i].ID)
		}
	}

	return todo
}

// fillBooks fans the offline filler out over todo with a bounded worker pool,
// stopping early once the budget context is cancelled. Remaining jobs drain via
// the closed channel so no worker goroutine leaks.
func (h *Handler) fillBooks(ctx context.Context, todo []int64) {
	jobs := make(chan int64)

	var wg sync.WaitGroup
	for range opdsBackfillWorkers {
		wg.Go(func() {
			for id := range jobs {
				_ = h.filler.Fill(ctx, id) // best-effort; gated by metadata_checked
			}
		})
	}

feed:
	for _, id := range todo {
		select {
		case <-ctx.Done():
			break feed // budget exhausted: stop feeding, let workers drain
		case jobs <- id:
		}
	}
	close(jobs)
	wg.Wait()
}

// rereadBooks refreshes the attempted rows in place so freshly-filled annotation
// and identifiers reach the feed.
func (h *Handler) rereadBooks(ctx context.Context, books []dbq.Book, todo []int64) {
	byID := make(map[int64]struct{}, len(todo))
	for _, id := range todo {
		byID[id] = struct{}{}
	}
	for i := range books {
		if _, ok := byID[books[i].ID]; !ok {
			continue
		}
		if fresh, err := h.q.GetBook(ctx, books[i].ID); err == nil {
			books[i] = fresh
		}
	}
}
