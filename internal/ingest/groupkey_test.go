package ingest

import (
	"github.com/stretchr/testify/suite"
)

type groupKeySuite struct {
	suite.Suite
}

func (s *groupKeySuite) TestGroupKeyAuthorOrderIndependent() {
	const title, lang = "Death's End", "en"
	surnameFirst := groupKey(title, []string{"Liu, Cixin"}, lang)
	firstLast := groupKey(title, []string{"Cixin Liu"}, lang)
	spaceOnly := groupKey(title, []string{"Liu Cixin"}, lang)

	s.Equal(surnameFirst, firstLast, "comma vs plain differ")
	s.Equal(spaceOnly, firstLast, "order differ")
}

func (s *groupKeySuite) TestGroupKeyMultiAuthorOrderIndependent() {
	a := groupKey("T", []string{"Smith, John", "Doe, Jane"}, "en")
	b := groupKey("T", []string{"Jane Doe", "John Smith"}, "en")

	s.Equal(a, b, "multi-author order not normalized")
}

func (s *groupKeySuite) TestGroupKeyDifferentLanguageSplits() {
	en := groupKey("T", []string{"A B"}, "en")
	ru := groupKey("T", []string{"A B"}, "ru")

	s.NotEqual(en, ru, "different language must produce different key")
}

func (s *groupKeySuite) TestGroupKeyMiddleInitialStillSplits() {
	// Accepted limitation: a differing middle token still splits (identifiers heal it).
	withInitial := groupKey("T", []string{"Ursula K. Le Guin"}, "en")
	without := groupKey("T", []string{"Ursula Le Guin"}, "en")

	s.NotEqual(without, withInitial, "expected middle-initial difference to split (documented limitation)")
}
