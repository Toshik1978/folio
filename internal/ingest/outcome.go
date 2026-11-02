package ingest

import (
	"errors"

	"github.com/Toshik1978/folio/internal/ebook"
)

// readFlow names what a read of a book's files is for.
type readFlow string

const (
	// flowMetadata reads a book's description and identifiers.
	flowMetadata readFlow = "metadata"
	// flowCover reads a book's cover image.
	flowCover readFlow = "cover"
)

// maxDetailBytes caps ReadResult.Detail.
const maxDetailBytes = 256

// ReadStatus is the outcome class of reading a book's files.
type ReadStatus int

// The read outcomes.
const (
	// ReadDone means the read finished: content was found, or the book has
	// nothing to read.
	ReadDone ReadStatus = iota
	// ReadBroken means the book's bytes can never be read by this reader; the
	// failure is recorded and not retried until the file or the reader changes.
	ReadBroken
	// ReadRetryable means the read failed for a reason that may pass (I/O,
	// cancellation, a busy write guard); nothing is recorded.
	ReadRetryable
)

// ReadResult is what a read of a book's files concluded.
type ReadResult struct {
	// Status is the outcome class.
	Status ReadStatus
	// Reason is why a broken book is broken; empty otherwise.
	Reason ebook.Reason
	// Detail is the error text for a failed read, capped at 256 bytes.
	Detail string
	// Fingerprint identifies the files that were read.
	Fingerprint string
	// Known reports that the book was already recorded as broken for this
	// fingerprint, so no file was opened.
	Known bool
	// Recorded reports that a broken-read record for the book and flow was
	// stored when the read began, applicable or not; a done read clears it.
	Recorded bool
	// Err is the underlying error of a failed read.
	Err error
}

// classify maps the error of a failed read to its outcome: broken for content
// the reader cannot parse (and for an archive entry that is gone), retryable
// for everything else, including open and stat failures, source I/O errors,
// context errors and a write-guard timeout.
func classify(err error) ReadResult {
	res := ReadResult{Err: err, Detail: truncate(err.Error(), maxDetailBytes)}
	if errors.Is(err, errMissingEntry) {
		res.Status, res.Reason = ReadBroken, ebook.ReasonMissingEntry

		return res
	}
	if reason, ok := ebook.ReasonOf(err); ok {
		res.Status, res.Reason = ReadBroken, reason

		return res
	}
	res.Status = ReadRetryable

	return res
}

// truncate cuts s to at most n bytes without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}

	return s[:n]
}

// combine folds the per-file results of a multi-file book, listed best
// candidate first, into one: the first done result wins, else any retryable
// one, else the top candidate's broken result. No results is done.
func combine(results []ReadResult) ReadResult {
	if len(results) == 0 {
		return ReadResult{Status: ReadDone}
	}
	for _, r := range results {
		if r.Status == ReadDone {
			return r
		}
	}
	for _, r := range results {
		if r.Status == ReadRetryable {
			return r
		}
	}

	return results[0]
}
