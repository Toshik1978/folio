package notify

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/stretchr/testify/suite"
)

type formatSuite struct{ suite.Suite }

func (s *formatSuite) TestPrefix() {
	s.Equal("https://folio.example.com", prefix("https://folio.example.com/"))
	s.Equal("https://folio.example.com", prefix("https://folio.example.com"))
	s.Equal("Folio", prefix(""))
	s.Equal("Folio", prefix("  "))
}

func (s *formatSuite) TestOneLineCollapsesWhitespace() {
	s.Equal("a b c d", oneLine("a\nb\tc\r\nd", 300))
}

func (s *formatSuite) TestOneLineCaps() {
	got := oneLine("x\n"+strings.Repeat("y", 398), 300)
	s.NotContains(got, "\n")
	s.Equal(300, utf8.RuneCountInString(got))
	s.True(strings.HasSuffix(got, "…"))
}

func (s *formatSuite) TestOneLineKeepsMultiByteRunes() {
	got := oneLine(strings.Repeat("é", 400), 300)
	s.True(utf8.ValidString(got))
	s.Equal(300, utf8.RuneCountInString(got))
	s.Equal(strings.Repeat("é", 299)+"…", got)
}

func (s *formatSuite) TestOneLineShortUnchanged() {
	s.Equal("short", oneLine("short", 300))
	s.Equal(strings.Repeat("a", 300), oneLine(strings.Repeat("a", 300), 300))
}

func (s *formatSuite) TestTruncateCutsWholeTrailingLines() {
	lines := make([]string, 0, 102)
	lines = append(lines, "https://folio.example.com", "Headline")
	for i := range 100 {
		lines = append(lines, fmt.Sprintf("- item %03d %s", i, strings.Repeat("é", 40)))
	}
	text := strings.Join(lines, "\n")
	s.Greater(utf8.RuneCountInString(text), 5000-1)

	got := truncate(text)
	s.LessOrEqual(utf8.RuneCountInString(got), maxMessageChars)
	s.True(strings.HasSuffix(got, "\n… (truncated)"))
	gl := strings.Split(got, "\n")
	s.Equal(lines[0], gl[0])
	s.Equal(lines[1], gl[1])
	for _, l := range gl[2 : len(gl)-1] {
		s.Contains(lines, l, "no partial item line")
	}
	s.Less(len(gl), len(lines))
}

func (s *formatSuite) TestTruncateShortUnchanged() {
	s.Equal("a\nb\n- c", truncate("a\nb\n- c"))
}

func (s *formatSuite) TestTestText() {
	s.Equal("Folio\nTest message: Telegram notifications are working.", testText("Folio"))
}

func (s *formatSuite) TestItemCapsAtThreeHundred() {
	got := item(strings.Repeat("z", 500))
	s.Equal(300, utf8.RuneCountInString(got))
}

func (s *formatSuite) TestOneLineCustomLimit() {
	s.Equal("abcd…", oneLine("abcdefghij", 5))
}
