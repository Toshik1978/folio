package events

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

func TestEvents(t *testing.T) {
	suite.Run(t, new(subscriptionSuite))
	suite.Run(t, new(brokerSuite))
}
