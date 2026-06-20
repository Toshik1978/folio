package ebook

// metadataSuite covers the publishing-metadata fields (publisher, year,
// identifiers) that are shared across formats, using in-memory inputs.
type metadataSuite struct {
	baseSuite
}

func (s *metadataSuite) TestParseYear() {
	cases := map[string]int{
		"2003":                2003,
		"2003-06-01":          2003,
		"D:20030601000000+00": 2003,
		"1869-01-01 00:00:00": 1869,
		"":                    0,
		"no year here":        0,
		"12":                  0,
		"0101":                0, // leading-zero sentinel (Calibre "unknown")
		"0101-01-01 00:00:00": 0,
		"0999-01-01":          0, // below the plausible-year floor
	}
	for in, want := range cases {
		s.Equal(want, ParseYear(in), "ParseYear(%q)", in)
	}
}

func (s *metadataSuite) TestFB2PublishInfo() {
	doc := `<?xml version="1.0" encoding="utf-8"?>
<FictionBook>
  <description>
    <title-info><book-title>Dune</book-title><lang>en</lang></title-info>
    <publish-info>
      <publisher>Chilton Books</publisher>
      <year>1965</year>
      <isbn>9780441013593</isbn>
    </publish-info>
  </description>
</FictionBook>`

	m, err := parseFB2XML([]byte(doc))
	s.Require().NoError(err)
	s.Equal("Chilton Books", m.Publisher)
	s.Equal(1965, m.Year)
	s.Require().Len(m.Identifiers, 1)
	s.Equal(Identifier{Type: "isbn", Value: "9780441013593"}, m.Identifiers[0])
}

func (s *metadataSuite) TestEPUBIdentifierScheme() {
	ids := extractEPUBIdentifiers([]opfIdentifier{
		{Scheme: "ISBN", Value: "9780441013593"},
		{Scheme: "", Value: "urn:isbn:9780000000001"}, // urn: prefix declares ISBN (no checksum)
		{Scheme: "", Value: "isbn:9780000000002"},     // isbn: prefix declares ISBN (no checksum)
		{Scheme: "", Value: "978-3-945749-74-6"},      // bare ISBN-13, valid check digit
		{Scheme: "", Value: "0441013597"},             // bare ISBN-10, valid check digit
		{Scheme: "", Value: "155860832X"},             // bare ISBN-10 with 'X' check digit
		{Scheme: "", Value: "junk-no-scheme"},
		{Scheme: "", Value: "A441013597"},
		{Scheme: "", Value: "044101359723W"},
		{Scheme: "", Value: "12345"},
		{Scheme: "", Value: "044101359X"},    // ISBN-10 shape but wrong check digit
		{Scheme: "", Value: "4006381333931"}, // valid EAN-13 check digit but not a 978/979 ISBN
		{Scheme: "", Value: "9780000000000"}, // 978 prefix but wrong check digit
	})
	s.Require().Len(ids, 6)
	s.Equal(Identifier{Type: "isbn", Value: "9780441013593"}, ids[0])
	s.Equal(Identifier{Type: "isbn", Value: "urn:isbn:9780000000001"}, ids[1])
	s.Equal(Identifier{Type: "isbn", Value: "isbn:9780000000002"}, ids[2])
	s.Equal(Identifier{Type: "isbn", Value: "978-3-945749-74-6"}, ids[3])
	s.Equal(Identifier{Type: "isbn", Value: "0441013597"}, ids[4])
	s.Equal(Identifier{Type: "isbn", Value: "155860832X"}, ids[5])
}
