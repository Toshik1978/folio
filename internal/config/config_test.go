package config

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

// TestConfig is the package's single entry point; every suite is registered here.
func TestConfig(t *testing.T) {
	suite.Run(t, new(validateSuite))
}

type validateSuite struct {
	suite.Suite
}

// TestValidPortAndURL accepts a well-formed config: a numeric in-range port and
// an absolute PublicURL.
func (s *validateSuite) TestValidPortAndURL() {
	cfg := Config{Port: "8080", PublicURL: "https://folio.example.com"}
	s.NoError(cfg.Validate())
}

// TestEmptyPublicURLIsValid confirms PublicURL is optional: an empty value is
// accepted (local/direct access trusts the request host).
func (s *validateSuite) TestEmptyPublicURLIsValid() {
	cfg := Config{Port: "8080", PublicURL: ""}
	s.NoError(cfg.Validate())
}

// TestInvalidPortRejected rejects non-numeric and out-of-range ports rather than
// letting them fail late at ListenAndServe with an opaque error.
func (s *validateSuite) TestInvalidPortRejected() {
	for _, port := range []string{"", "abc", "0", "70000", "-1"} {
		cfg := Config{Port: port}
		s.Errorf(cfg.Validate(), "port %q must be rejected", port)
	}
}

// TestInvalidPublicURLRejected rejects a PublicURL that cannot be parsed into an
// absolute origin, which would otherwise degrade silently to "" in originOf.
func (s *validateSuite) TestInvalidPublicURLRejected() {
	for _, raw := range []string{"folio.example.com", "://missing-scheme", "https://"} {
		cfg := Config{Port: "8080", PublicURL: raw}
		s.Errorf(cfg.Validate(), "PublicURL %q must be rejected", raw)
	}
}
