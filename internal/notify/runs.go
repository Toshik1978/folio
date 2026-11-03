package notify

import (
	"strconv"
	"strings"
)

// RunKind is the kind of library run an outcome reports.
type RunKind uint8

// Run kinds.
const (
	RunSync RunKind = iota
	RunDeepIndex
)

// String is the kind as it appears in messages.
func (k RunKind) String() string {
	if k == RunDeepIndex {
		return "deep index"
	}

	return "sync"
}

// RunStatus is how a library run ended.
type RunStatus uint8

// Run statuses.
const (
	RunFailed RunStatus = iota
	RunCompleted
)

// UnreadableBook is one newly unreadable book file.
type UnreadableBook struct {
	BookID int64
	Reason string // ebook.Reason value: corrupt | missing_entry | too_large | crashed
}

// RunOutcome is what a producer reports when a library run ends.
type RunOutcome struct {
	LibraryID        int64
	Library          string // library name
	Kind             RunKind
	Status           RunStatus
	Reason           string // RunFailed: the error text
	CrashReported    bool   // RunFailed came from a recovered panic already sent as a crash
	PerBookErrors    int    // RunCompleted: books left for retry
	FillFailed       string // RunSync completed but its post-sync fill could not complete: the error text
	NewlyUnreadable  int    // total newly recorded unreadable books
	UnreadableSample []UnreadableBook
	BooksBefore      int // RunSync: book count before the run
	BooksRemoved     int // RunSync: books pruned by the run
}

// RunFinished applies a run outcome to the library's failing state: the first
// failure sends "failed" (unless a crash message already stood in), further
// failures are silent, and the first success after a failure sends
// "recovered". It returns at once when notifications are off or not ready and
// never blocks.
func (s *Service) RunFinished(o RunOutcome) {
	cfg := s.cfg.Load()
	if cfg == nil || !cfg.Enabled || !cfg.Ready() {
		return
	}

	switch o.Status {
	case RunFailed:
		if s.markFailing(o.LibraryID) && !o.CrashReported {
			s.enqueue(message{kind: KindRunFailed, text: runFailedText(s.prefix, o)})
		}
	case RunCompleted:
		s.runCompleted(o)
	}
}

// runCompleted sends the recovery message for a library that was failing,
// then the unreadable summary and mass-removal alert, which depend only on
// this run.
func (s *Service) runCompleted(o RunOutcome) {
	if s.clearFailing(o.LibraryID) {
		s.enqueue(message{kind: KindRunRecovered, text: runRecoveredText(s.prefix, o)})
	}
	if o.NewlyUnreadable > 0 {
		s.enqueue(message{kind: KindUnreadable, text: unreadableText(s.prefix, s.publicURL, o)})
	}
	if massRemoval(o) {
		s.enqueue(message{kind: KindMassRemoval, text: massRemovalText(s.prefix, o)})
	}
}

// LibraryRemoved forgets a deleted library's failing state, sending nothing.
func (s *Service) LibraryRemoved(id int64) { s.clearFailing(id) }

// markFailing records id as failing and reports whether it was healthy before.
func (s *Service) markFailing(id int64) bool {
	s.failingMu.Lock()
	defer s.failingMu.Unlock()
	if s.failing[id] {
		return false
	}
	if s.failing == nil {
		s.failing = make(map[int64]bool)
	}
	s.failing[id] = true

	return true
}

// clearFailing records id as healthy and reports whether it was failing.
func (s *Service) clearFailing(id int64) bool {
	s.failingMu.Lock()
	defer s.failingMu.Unlock()
	was := s.failing[id]
	delete(s.failing, id)

	return was
}

// resetFailing discards every library's failing state.
func (s *Service) resetFailing() {
	s.failingMu.Lock()
	defer s.failingMu.Unlock()
	clear(s.failing)
}

// runFailedText is the KindRunFailed message.
func runFailedText(prefix string, o RunOutcome) string {
	return truncate(prefix +
		"\nLibrary \"" + o.Library + "\": " + o.Kind.String() + " failed" +
		"\nReason: " + oneLine(o.Reason, maxItemChars) +
		"\nNo further messages for this library until a run succeeds.")
}

// runRecoveredText is the KindRunRecovered message.
func runRecoveredText(prefix string, o RunOutcome) string {
	return truncate(prefix +
		"\nLibrary \"" + o.Library + "\": recovered" +
		"\nThe last " + o.Kind.String() + " completed successfully.")
}

const (
	// unreadableItems is how many unreadable books a summary lists.
	unreadableItems = 5
	// massRemovalMin is the fewest removed books that count as a mass removal.
	massRemovalMin = 20
	// massRemovalPercent is the smallest share of the library, in percent,
	// that counts as a mass removal.
	massRemovalPercent = 10
)

// massRemoval reports whether a completed sync removed enough books, in
// absolute and relative terms, to warrant an alert.
func massRemoval(o RunOutcome) bool {
	return o.Kind == RunSync && o.BooksBefore > 0 &&
		o.BooksRemoved >= massRemovalMin &&
		o.BooksRemoved*100 >= o.BooksBefore*massRemovalPercent
}

// massRemovalText is the KindMassRemoval message.
func massRemovalText(prefix string, o RunOutcome) string {
	return truncate(prefix +
		"\nLibrary \"" + o.Library + "\": sync removed " + strconv.Itoa(o.BooksRemoved) +
		" of " + strconv.Itoa(o.BooksBefore) + " books (" +
		strconv.Itoa(o.BooksRemoved*100/o.BooksBefore) + "%)" +
		"\nThe removal has been applied. If this was not expected, " +
		"check that the library source is complete and mounted.")
}

// unreadableText is the KindUnreadable message. Book titles are never used:
// a book is named by its link (or ID) and the reason it cannot be read.
func unreadableText(prefix, publicURL string, o RunOutcome) string {
	base := strings.TrimRight(strings.TrimSpace(publicURL), "/")
	var b strings.Builder
	b.WriteString(prefix)
	b.WriteString("\nLibrary \"" + o.Library + "\": " + o.Kind.String() + " found " +
		strconv.Itoa(o.NewlyUnreadable) + " newly unreadable book file(s)")
	for _, u := range o.UnreadableSample[:min(len(o.UnreadableSample), unreadableItems)] {
		b.WriteString("\n- " + bookRef(base, u.BookID) + ": " + reasonLabel(u.Reason))
	}
	if more := o.NewlyUnreadable - unreadableItems; more > 0 {
		b.WriteString("\n…and " + strconv.Itoa(more) +
			" more. They will not be reported again unless the files change.")
	}

	return truncate(b.String())
}

// bookRef links a book on the instance, or names it by ID when no public URL is set.
func bookRef(base string, id int64) string {
	if base == "" {
		return "book #" + strconv.FormatInt(id, 10)
	}

	return base + "/books/" + strconv.FormatInt(id, 10)
}

// reasonLabel is the owner-facing text of an ebook.Reason; unknown values pass through.
func reasonLabel(reason string) string {
	switch reason {
	case "corrupt":
		return "file is damaged or not in the expected format"
	case "missing_entry":
		return "file is missing from its archive"
	case "too_large":
		return "file is too large to read"
	case "crashed":
		return "reading the file crashed the reader"
	}

	return item(reason)
}
