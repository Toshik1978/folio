package metasearch

import "github.com/stretchr/testify/suite"

// relevanceSuite covers the provider-agnostic cover relevance filter.
type relevanceSuite struct {
	suite.Suite
}

func (s *relevanceSuite) TestTitleAcceptableKeepsSingleTitle() {
	s.True(TitleAcceptable("Death's End (Remembrance of Earth's Past)",
		"Death's End (Remembrance of Earth's Past, #3)"))
	s.True(TitleAcceptable("Death's End", "Death's End"))
}

func (s *relevanceSuite) TestTitleAcceptableDropsTrilogyBoxSet() {
	// The real Goodreads box set that showed three spines instead of one cover.
	s.False(TitleAcceptable("Death's End (Remembrance of Earth's Past)",
		"Remembrance of Earth's Past: The Three-Body Trilogy (The Three-Body Problem, The Dark Forest, Death's End)"))
}

func (s *relevanceSuite) TestTitleAcceptableDropsUnrelated() {
	s.False(TitleAcceptable("Death's End", "The Three-Body Problem"))
}

func (s *relevanceSuite) TestTitleAcceptableFailsOpenButDropsJunk() {
	// Empty query: keep a real title, still drop a box set.
	s.True(TitleAcceptable("", "Anything At All"))
	s.False(TitleAcceptable("", "Dune 6-Book Boxed Set"))
}

func (s *relevanceSuite) TestIsJunkTitle() {
	for _, junk := range []string{
		"Three-Body Problem Boxed Set",
		"5 Books Collection Set",
		"The Three-Body Trilogy",
		"Death's End (3 Books)",
		"Study Guide: Death's End (SuperSummary)",
		"Dune [Audiobook]",
	} {
		s.True(IsJunkTitle(junk), junk)
	}
	for _, ok := range []string{
		"Death's End",
		"Death's End (The Three-Body Problem Series, 3)",
		"Death's End, Book 3",
		"Mindset",
	} {
		s.False(IsJunkTitle(ok), ok)
	}
}

func (s *relevanceSuite) TestIsJunkTitleDropsForeignScript() {
	// Build CJK from code points so the source stays ASCII (gosmopolitan lint):
	// U+4E09 U+4F53 = Han, U+3042 = Hiragana, U+AC00 = Hangul.
	han := string([]rune{0x4e09, 0x4f53})
	hiragana := string(rune(0x3042))
	hangul := string(rune(0xac00))
	for _, cjk := range []string{han, "Death's End " + han, hiragana, hangul} {
		s.True(IsJunkTitle(cjk), cjk)
	}
	s.False(IsJunkTitle("Death's End"))
}
