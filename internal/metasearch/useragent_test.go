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

// TestRandomUserAgentRotates asserts that calling RandomUserAgent many times
// yields at least 2 distinct values. With a 5-UA pool the probability of
// drawing the same UA 50 times in a row is (1/5)^49 ≈ 10^-34, so a failure
// here unambiguously signals a broken implementation.
func (s *uaSuite) TestRandomUserAgentRotates() {
	seen := make(map[string]struct{})
	for range 50 {
		seen[RandomUserAgent()] = struct{}{}
	}
	s.GreaterOrEqual(len(seen), 2, "RandomUserAgent must return at least 2 distinct UAs across 50 calls")
}
