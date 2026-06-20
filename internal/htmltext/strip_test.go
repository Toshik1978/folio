package htmltext

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

func TestHTMLText(t *testing.T) {
	suite.Run(t, new(htmltextTestSuite))
}

type htmltextTestSuite struct {
	suite.Suite
}

func (s *htmltextTestSuite) TestStripMarkup() {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain text", "Hello world", "Hello world"},
		{"strips tags", "<p>Hello <b>world</b></p>", "Hello world"},
		{"collapses whitespace across tags", "<p>one</p>\n  <p>two</p>", "one two"},
		{"named entity mdash", "Folio&mdash;reader", "Folio—reader"},
		{"xml builtin entities", "a &amp; b &lt;c&gt;", "a & b <c>"},
		{"numeric char ref", "x&#8212;y", "x—y"},
		// rsquo resolves to the ASCII apostrophe in the FTS table so search
		// matches regardless of the quote style used in the source.
		{"fts normalizes single quote to ascii", "It&rsquo;s here", "It's here"},
		// A bare ampersand is invalid XML, so the decoder fails before emitting
		// any text and StripMarkup falls back to the regex strip.
		{"fallback on bare ampersand", "Tom & Jerry", "Tom & Jerry"},
		// A stray '<' breaks the XML decoder midway; the regex fallback must
		// then process the whole string instead of silently losing the tail.
		{
			"fallback on malformed midway keeps tail",
			"Start <b>bold</b> then a stray < and the tail survives",
			"Start bold then a stray < and the tail survives",
		},
	}
	for _, tc := range cases {
		s.Run(tc.name, func() {
			s.Equal(tc.want, StripMarkup(tc.in))
		})
	}
}

// TestEntityVariants pins the one intentional difference between the display and
// FTS entity tables: the single curly quotes. Everything else must stay in sync.
func (s *htmltextTestSuite) TestEntityVariants() {
	s.Equal("‘", DisplayEntities["lsquo"], "display keeps the typographic left quote")
	s.Equal("’", DisplayEntities["rsquo"], "display keeps the typographic right quote")
	s.Equal("'", ftsEntities["lsquo"], "FTS normalizes the left quote to ASCII")
	s.Equal("'", ftsEntities["rsquo"], "FTS normalizes the right quote to ASCII")

	// Both variants are otherwise identical (same base + same key set).
	s.Len(DisplayEntities, len(ftsEntities))
	for k, v := range baseEntities {
		s.Equal(v, DisplayEntities[k], "display table carries base entity %q", k)
		s.Equal(v, ftsEntities[k], "fts table carries base entity %q", k)
	}
}
