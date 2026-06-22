package metasearch

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

type coreSuite struct {
	suite.Suite
}

func TestMetasearch(t *testing.T) {
	suite.Run(t, new(coreSuite))
}

func (s *coreSuite) TestHasCapability() {
	caps := []Capability{CapCover}
	s.True(HasCapability(caps, CapCover))
	s.False(HasCapability(caps, CapIdentify))
	s.False(HasCapability(nil, CapCover))
}

func (s *coreSuite) TestSourceNamesAreDistinct() {
	names := map[string]struct{}{
		SourceAmazon: {}, SourceGoodreads: {}, SourceOpenLibrary: {}, SourceGoogleBooks: {},
	}
	s.Len(names, 4, "source name constants must be unique")
}
