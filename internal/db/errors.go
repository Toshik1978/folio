package db

import (
	"errors"

	"modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"
)

// IsUniqueViolation reports whether err is a SQLite UNIQUE constraint failure
// (e.g. inserting a second library with the same path), so the API layer can
// answer 409 instead of a generic 500.
func IsUniqueViolation(err error) bool {
	var se *sqlite.Error

	return errors.As(err, &se) && se.Code() == sqlitelib.SQLITE_CONSTRAINT_UNIQUE
}
