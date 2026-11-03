package notify

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

func TestNotify(t *testing.T) {
	suite.Run(t, new(configSuite))
	suite.Run(t, new(storeSuite))
	suite.Run(t, new(clientSuite))
	suite.Run(t, new(statusSuite))
	suite.Run(t, new(formatSuite))
	suite.Run(t, new(queueSuite))
	suite.Run(t, new(serviceSuite))
	suite.Run(t, new(crashSuite))
	suite.Run(t, new(runsSuite))
	suite.Run(t, new(errWindowSuite))
	suite.Run(t, new(leakSuite))
}
