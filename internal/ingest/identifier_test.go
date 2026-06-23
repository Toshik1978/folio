package ingest

import (
	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/ebook"
)

type identifierSuite struct {
	suite.Suite
}

func (s *identifierSuite) TestCleanIdentifier() {
	// 1. empty/trim tests
	t, v := cleanIdentifier("  ", " 123 ")
	s.Empty(t)
	s.Empty(v)

	// 2. useless schemes
	t, v = cleanIdentifier("uuid", "abc")
	s.Empty(t)
	s.Empty(v)

	// 3. UUID-like filter tests
	t, v = cleanIdentifier("isbn", "urn:uuid:2e796e6d-53db-4e1b-9686-35368a528e18")
	s.Empty(t)
	s.Empty(v)

	t, v = cleanIdentifier("isbn", "uuid:2e796e6d-53db-4e1b-9686-35368a528e18")
	s.Empty(t)
	s.Empty(v)

	t, v = cleanIdentifier("isbn", "2e796e6d-53db-4e1b-9686-35368a528e18")
	s.Empty(t)
	s.Empty(v)

	t, v = cleanIdentifier("isbn", "{2e796e6d-53db-4e1b-9686-35368a528e18}")
	s.Empty(t)
	s.Empty(v)

	// 4. Type mapping
	t, v = cleanIdentifier("amazon-asin", "B00WDVKZY0")
	s.Equal("amazon", t)
	s.Equal("B00WDVKZY0", v)

	// 5. Normalization - ASIN casing
	t, v = cleanIdentifier("asin", "b00wdvkzy0")
	s.Equal("amazon", t)
	s.Equal("B00WDVKZY0", v)

	// 6. Normalization - ISBN prefix, spaces, hyphens, casing
	t, v = cleanIdentifier("isbn-13", "urn:isbn: 978-3-16-148410-0 ")
	s.Equal("isbn", t)
	s.Equal("9783161484100", v)

	t, v = cleanIdentifier("isbn", "isbn:0-393-04002-x")
	s.Equal("isbn", t)
	s.Equal("039304002X", v)

	// 7. Google Books IndustryIdentifiers type strings
	t, v = cleanIdentifier("ISBN_13", "9780441013593")
	s.Equal("isbn", t)
	s.Equal("9780441013593", v)
}

func (s *identifierSuite) TestCleanIdentifiers() {
	// Deduplication and conflict resolution
	ids := []identifier{
		{Type: "isbn", Value: "12345"},         // garbage
		{Type: "isbn", Value: "0441013597"},    // valid ISBN-10
		{Type: "isbn", Value: "9780441013593"}, // valid ISBN-13
		{Type: "amazon", Value: "b00wdvkzy0"},
		{Type: "mobi-asin", Value: "B00WDVKZY0"}, // duplicate same-length ASIN
	}

	clean := cleanIdentifiers(ids)
	s.Len(clean, 2)
	s.Equal("9780441013593", clean["isbn"]) // preferred ISBN-13
	s.Equal("B00WDVKZY0", clean["amazon"])  // ASIN mapped to amazon, uppercased, and deduplicated (last-one-wins)
}

func (s *identifierSuite) TestValidStrongIdentifier() {
	// ISBN: must pass checksum to be a usable grouping key.
	s.True(validStrongIdentifier(ebook.IdentifierISBN, "9781466853454"))  // valid ISBN-13
	s.True(validStrongIdentifier(ebook.IdentifierISBN, "0441013597"))     // valid ISBN-10
	s.False(validStrongIdentifier(ebook.IdentifierISBN, "9781234567890")) // bad ISBN-13 checksum
	s.False(validStrongIdentifier(ebook.IdentifierISBN, "12345"))         // wrong shape

	// Placeholders (a single character repeated) are never a usable grouping key,
	// regardless of type.
	s.False(validStrongIdentifier(ebook.IdentifierISBN, "0000000000000"))
	s.False(validStrongIdentifier(amazonType, "0000000000"))
	s.False(validStrongIdentifier(goodreadsType, "11111"))

	// Real non-ISBN strong identifiers pass (no checksum scheme to apply).
	s.True(validStrongIdentifier(amazonType, "B00WDVKZY0"))
	s.True(validStrongIdentifier(googleType, "zyTCAlFPjgYC"))
	s.True(validStrongIdentifier(goodreadsType, "18007564"))
}

func (s *identifierSuite) TestCleanedEbookIdentifiers() {
	in := []ebook.Identifier{
		{Type: "AMAZON-ASIN", Value: "b001"},
		{Type: "isbn-13", Value: "978-0-441-01359-3"},
	}
	out := cleanedEbookIdentifiers(in)
	s.Equal([]ebook.Identifier{
		{Type: "amazon", Value: "B001"},
		{Type: "isbn", Value: "9780441013593"},
	}, out)
}
