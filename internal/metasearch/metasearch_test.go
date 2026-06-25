package metasearch

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

type coreSuite struct {
	suite.Suite
}

func TestMetasearch(t *testing.T) {
	suite.Run(t, new(coreSuite))
	suite.Run(t, new(uaSuite))
	suite.Run(t, new(relevanceSuite))
}

func (s *coreSuite) TestHasCapability() {
	caps := []Capability{CapCover}
	s.True(HasCapability(caps, CapCover))
	s.False(HasCapability(caps, CapIdentify))
	s.False(HasCapability(nil, CapCover))
}

func (s *coreSuite) TestSourceNamesAreDistinct() {
	names := map[string]struct{}{
		SourceAmazon: {}, SourceGoodreads: {}, SourceOpenLibrary: {}, SourceGoogleBooks: {},
	}
	s.Len(names, 4, "source name constants must be unique")
}

func (s *coreSuite) TestOriginalCDNImage() {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "single modifier AC_UL320",
			in:   "https://m.media-amazon.com/images/I/71abc._AC_UL320_.jpg",
			want: "https://m.media-amazon.com/images/I/71abc.jpg",
		},
		{
			name: "multi modifier SX50_SY75",
			in:   "https://m.media-amazon.com/images/I/71abc._SX50_SY75_.jpg",
			want: "https://m.media-amazon.com/images/I/71abc.jpg",
		},
		{
			name: "goodreads SX50 modifier",
			in:   "https://images-na.ssl-images-amazon.com/images/S/aaa._SX50_.jpg",
			want: "https://images-na.ssl-images-amazon.com/images/S/aaa.jpg",
		},
		{
			name: "no modifier unchanged",
			in:   "https://m.media-amazon.com/images/I/71abc.jpg",
			want: "https://m.media-amazon.com/images/I/71abc.jpg",
		},
		{
			name: "non-amazon URL unchanged",
			in:   "https://books.google.com/cover.jpg",
			want: "https://books.google.com/cover.jpg",
		},
	}
	for _, tc := range cases {
		s.Run(tc.name, func() {
			s.Equal(tc.want, OriginalCDNImage(tc.in))
		})
	}
}

func (s *coreSuite) TestThumbCDNImage() {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "replaces AC adaptive-crop modifier",
			in:   "https://m.media-amazon.com/images/I/91naQyo8fsL._AC_UY218_.jpg",
			want: "https://m.media-amazon.com/images/I/91naQyo8fsL._SY450_.jpg",
		},
		{
			name: "upgrades tiny goodreads thumbnail",
			in:   "https://i.gr-assets.com/images/S/books/123i/25451264._SY75_.jpg",
			want: "https://i.gr-assets.com/images/S/books/123i/25451264._SY450_.jpg",
		},
		{
			name: "non-amazon URL unchanged",
			in:   "https://books.google.com/books/content?id=abc&img=1",
			want: "https://books.google.com/books/content?id=abc&img=1",
		},
	}
	for _, tc := range cases {
		s.Run(tc.name, func() {
			s.Equal(tc.want, ThumbCDNImage(tc.in, 450))
		})
	}
}
