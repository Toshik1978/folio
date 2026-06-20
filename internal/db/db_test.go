package db

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

// TestDB is the package's single entry point; every suite is registered here.
func TestDB(t *testing.T) {
	suite.Run(t, new(booksFilterSuite))
	suite.Run(t, new(connSuite))
}
