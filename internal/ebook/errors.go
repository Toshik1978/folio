package ebook

import (
	"errors"
	"fmt"
)

var (
	// ErrSourceIO marks a failure to read the book's bytes. It is retryable
	// and never classifies a book as broken.
	ErrSourceIO = errors.New("ebook: source read failed")
	// ErrTooLarge marks an entry that exceeds the size cap (broken: too_large).
	ErrTooLarge = errors.New("ebook: entry exceeds size cap")
	// ErrMalformed marks content a reader could not parse (broken: corrupt).
	ErrMalformed = errors.New("ebook: malformed content")
)

// PanicError carries a value recovered from a panicking parser (broken: crashed).
type PanicError struct{ Value any }

// Error implements the error interface.
func (e *PanicError) Error() string { return fmt.Sprintf("ebook: parser panic: %v", e.Value) }

// Reason is why a book is recorded as broken.
type Reason string

// The broken reasons.
const (
	ReasonCorrupt      Reason = "corrupt"
	ReasonMissingEntry Reason = "missing_entry" // raised by ingest's resolver
	ReasonTooLarge     Reason = "too_large"
	ReasonCrashed      Reason = "crashed"
)

// ReasonOf reports the broken reason for err. ok is false for retryable or
// unknown errors (ErrSourceIO, context errors, open errors). ErrSourceIO wins
// when err also matches a broken kind.
func ReasonOf(err error) (Reason, bool) {
	if err == nil || errors.Is(err, ErrSourceIO) {
		return "", false
	}
	var pe *PanicError
	switch {
	case errors.Is(err, ErrTooLarge):
		return ReasonTooLarge, true
	case errors.As(err, &pe):
		return ReasonCrashed, true
	case errors.Is(err, ErrMalformed):
		return ReasonCorrupt, true
	}

	return "", false
}
