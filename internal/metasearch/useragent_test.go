package metasearch

import (
	"slices"

	"github.com/stretchr/testify/suite"
)

type uaSuite struct {
	suite.Suite
}

func (s *uaSuite) TestRandomUserAgentReturnsFromPool() {
	for range 20 {
		ua := RandomUserAgent()
		s.NotEmpty(ua)
		s.True(slices.Contains(desktopUserAgents, ua), "UA must come from the pool")
	}
}

func (s *uaSuite) TestPoolIsNonTrivial() {
	s.GreaterOrEqual(len(desktopUserAgents), 3, "rotation needs a few options")
}
