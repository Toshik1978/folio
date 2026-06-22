package api

import (
	"net/http"
	"net/url"
)

type listsSuite struct {
	baseSuite
}

// browseURL builds a browse list/letters URL with the query params properly
// escaped (notably '#' -> %23 and Cyrillic letters).
func browseURL(path, letter string, library, page, limit int64) string {
	v := url.Values{}
	if letter != "" {
		v.Set("letter", letter)
	}
	if library > 0 {
		v.Set("library", itoa(library))
	}
	if page > 0 {
		v.Set("page", itoa(page))
	}
	if limit > 0 {
		v.Set("limit", itoa(limit))
	}
	if len(v) == 0 {
		return path
	}

	return path + "?" + v.Encode()
}

func (s *listsSuite) TestListAuthorsByLetter() {
	src := s.seedLibrary("folder", "/lib")
	s.seedBook(src, bookSeed{Title: "A", Authors: []string{"Asimov", "Clarke"}})
	s.seedBook(src, bookSeed{Title: "B", Authors: []string{"Asimov"}})

	w := s.do(http.MethodGet, browseURL("/authors", "A", 0, 0, 0), nil)
	s.Require().Equal(http.StatusOK, w.Code)
	var authors []authorView
	s.decode(w, &authors)
	s.Require().Len(authors, 1)
	s.Equal("Asimov", authors[0].Name)
	s.Equal(int64(2), authors[0].BookCount)

	w = s.do(http.MethodGet, browseURL("/authors", "C", 0, 0, 0), nil)
	s.decode(w, &authors)
	s.Require().Len(authors, 1)
	s.Equal("Clarke", authors[0].Name)
	s.Equal(int64(1), authors[0].BookCount)
}

func (s *listsSuite) TestListAuthorsCaseInsensitiveBucket() {
	src := s.seedLibrary("folder", "/lib")
	// A lowercase-initial name: under the old binary name collation it sorted
	// into '#'; with the uppercase name_fold it buckets under its letter and
	// sorts case-insensitively alongside upper-case names.
	s.seedBook(src, bookSeed{Title: "A", Authors: []string{"alice walker"}})
	s.seedBook(src, bookSeed{Title: "B", Authors: []string{"Aldous Huxley"}})

	w := s.do(http.MethodGet, browseURL("/authors", "A", 0, 0, 0), nil)
	s.Require().Equal(http.StatusOK, w.Code)
	var authors []authorView
	s.decode(w, &authors)
	s.Require().Len(authors, 2)
	// Case-insensitive order: "Aldous" (ALDOUS) before "alice" (ALICE).
	s.Equal("Aldous Huxley", authors[0].Name)
	s.Equal("alice walker", authors[1].Name)

	// The lowercase-initial author is not stranded in the '#' bucket.
	w = s.do(http.MethodGet, browseURL("/authors", hashBucket, 0, 0, 0), nil)
	s.decode(w, &authors)
	s.Require().Empty(authors)
}

func (s *listsSuite) TestAuthorLettersMultiScript() {
	src := s.seedLibrary("folder", "/lib")
	s.seedBook(src, bookSeed{Title: "A", Authors: []string{"Asimov", "Tolkien"}})
	s.seedBook(src, bookSeed{Title: "B", Authors: []string{"Толстой"}})
	s.seedBook(src, bookSeed{Title: "C", Authors: []string{"42 Authors"}}) // '#' bucket

	w := s.do(http.MethodGet, "/authors/letters", nil)
	s.Require().Equal(http.StatusOK, w.Code)
	var letters []string
	s.decode(w, &letters)
	// Canonical order: Cyrillic, then Latin, then '#'.
	s.Equal([]string{"Т", "A", "T", "#"}, letters)
}

func (s *listsSuite) TestListAuthorsCyrillic() {
	src := s.seedLibrary("folder", "/lib")
	s.seedBook(src, bookSeed{Title: "A", Authors: []string{"Толстой"}})
	s.seedBook(src, bookSeed{Title: "B", Authors: []string{"Тургенев"}})
	s.seedBook(src, bookSeed{Title: "C", Authors: []string{"Пушкин"}})

	w := s.do(http.MethodGet, browseURL("/authors", "Т", 0, 0, 0), nil)
	s.Require().Equal(http.StatusOK, w.Code)
	var authors []authorView
	s.decode(w, &authors)
	s.Require().Len(authors, 2)
	s.Equal("Толстой", authors[0].Name)
	s.Equal("Тургенев", authors[1].Name)
}

func (s *listsSuite) TestListAuthorsHashBucket() {
	src := s.seedLibrary("folder", "/lib")
	s.seedBook(src, bookSeed{Title: "A", Authors: []string{"42 Authors"}})
	s.seedBook(src, bookSeed{Title: "B", Authors: []string{"Ёжик"}}) // Ё now folds to Е (M6)
	s.seedBook(src, bookSeed{Title: "C", Authors: []string{"Asimov"}})

	// Only the digit-led name remains in '#'; db.Fold maps Ё→Е, so "Ёжик" files
	// under the Cyrillic 'Е' bucket rather than the catch-all.
	w := s.do(http.MethodGet, browseURL("/authors", hashBucket, 0, 0, 0), nil)
	s.Require().Equal(http.StatusOK, w.Code)
	var authors []authorView
	s.decode(w, &authors)
	s.Require().Len(authors, 1)
	s.Equal("42 Authors", authors[0].Name)

	ew := s.do(http.MethodGet, browseURL("/authors", "Е", 0, 0, 0), nil)
	s.Require().Equal(http.StatusOK, ew.Code)
	var eauthors []authorView
	s.decode(ew, &eauthors)
	names := make([]string, len(eauthors))
	for i := range eauthors {
		names[i] = eauthors[i].Name
	}
	s.Contains(names, "Ёжик", "Ё-led name files under the Cyrillic Е bucket")
}

func (s *listsSuite) TestListAuthorsPaginated() {
	src := s.seedLibrary("folder", "/lib")
	for _, name := range []string{"Adams", "Asimov", "Atwood", "Austen"} {
		s.seedBook(src, bookSeed{Title: name, Authors: []string{name}})
	}

	page1 := s.do(http.MethodGet, browseURL("/authors", "A", 0, 1, 2), nil)
	var authors []authorView
	s.decode(page1, &authors)
	s.Require().Len(authors, 2)
	s.Equal("Adams", authors[0].Name)
	s.Equal("Asimov", authors[1].Name)

	page2 := s.do(http.MethodGet, browseURL("/authors", "A", 0, 2, 2), nil)
	s.decode(page2, &authors)
	s.Require().Len(authors, 2)
	s.Equal("Atwood", authors[0].Name)
	s.Equal("Austen", authors[1].Name)
}

func (s *listsSuite) TestListSeriesByLetter() {
	src := s.seedLibrary("folder", "/lib")
	s.seedBook(src, bookSeed{Title: "A", Series: "Foundation"})
	s.seedBook(src, bookSeed{Title: "B", Series: "Foundation"})
	s.seedBook(src, bookSeed{Title: "C"}) // no series — excluded

	w := s.do(http.MethodGet, browseURL("/series", "F", 0, 0, 0), nil)
	s.Require().Equal(http.StatusOK, w.Code)
	var series []nameIDCountView
	s.decode(w, &series)
	s.Require().Len(series, 1)
	s.Equal("Foundation", series[0].Name)
	s.Equal(int64(2), series[0].BookCount)
}

func (s *listsSuite) TestListTagsByLetter() {
	src := s.seedLibrary("folder", "/lib")
	s.seedBook(src, bookSeed{Title: "A", Genres: []string{"SciFi", "Classic"}})
	s.seedBook(src, bookSeed{Title: "B", Genres: []string{"SciFi"}})

	w := s.do(http.MethodGet, browseURL("/tags", "C", 0, 0, 0), nil)
	s.Require().Equal(http.StatusOK, w.Code)
	var tags []nameCountView
	s.decode(w, &tags)
	s.Require().Len(tags, 1)
	s.Equal("Classic", tags[0].Name)
	s.Equal(int64(1), tags[0].BookCount)

	w = s.do(http.MethodGet, browseURL("/tags", "S", 0, 0, 0), nil)
	s.decode(w, &tags)
	s.Require().Len(tags, 1)
	s.Equal("SciFi", tags[0].Name)
	s.Equal(int64(2), tags[0].BookCount)
}

func (s *listsSuite) TestListPublishersByLetter() {
	src := s.seedLibrary("folder", "/lib")
	s.seedBook(src, bookSeed{Title: "A", Publisher: "Tor"})
	s.seedBook(src, bookSeed{Title: "B", Publisher: "Tor"})
	s.seedBook(src, bookSeed{Title: "C", Publisher: "Gollancz"})
	s.seedBook(src, bookSeed{Title: "D"}) // no publisher — excluded

	w := s.do(http.MethodGet, browseURL("/publishers", "G", 0, 0, 0), nil)
	s.Require().Equal(http.StatusOK, w.Code)
	var pubs []nameCountView
	s.decode(w, &pubs)
	s.Require().Len(pubs, 1)
	s.Equal("Gollancz", pubs[0].Name)
	s.Equal(int64(1), pubs[0].BookCount)

	w = s.do(http.MethodGet, browseURL("/publishers", "T", 0, 0, 0), nil)
	s.decode(w, &pubs)
	s.Require().Len(pubs, 1)
	s.Equal("Tor", pubs[0].Name)
	s.Equal(int64(2), pubs[0].BookCount)
}

// TestPublisherBrowseFoldsCase covers the fold() bucketing fix: a lowercase
// publisher must bucket under its real letter (not '#'), and case variants must
// all surface under that letter as distinct entries (each still resolvable by
// the exact-match book filter).
func (s *listsSuite) TestPublisherBrowseFoldsCase() {
	src := s.seedLibrary("folder", "/lib")
	s.seedBook(src, bookSeed{Title: "A", Publisher: "penguin"}) // lowercase
	s.seedBook(src, bookSeed{Title: "B", Publisher: "Penguin"}) // case variant
	s.seedBook(src, bookSeed{Title: "C", Publisher: "PENGUIN"}) // case variant

	// The alphabet selector buckets every variant under 'P', never '#'.
	w := s.do(http.MethodGet, browseURL("/publishers/letters", "", 0, 0, 0), nil)
	s.Require().Equal(http.StatusOK, w.Code)
	var letters []string
	s.decode(w, &letters)
	s.Contains(letters, "P")
	s.NotContains(letters, "#")

	// Listing 'P' returns all three case variants, summing to all three books.
	w = s.do(http.MethodGet, browseURL("/publishers", "P", 0, 0, 0), nil)
	s.Require().Equal(http.StatusOK, w.Code)
	var pubs []nameCountView
	s.decode(w, &pubs)
	s.Require().Len(pubs, 3)
	names := make([]string, 0, len(pubs))
	var total int64
	for _, p := range pubs {
		names = append(names, p.Name)
		total += p.BookCount
	}
	s.ElementsMatch([]string{"penguin", "Penguin", "PENGUIN"}, names)
	s.Equal(int64(3), total)
}

func (s *listsSuite) TestAuthorsScopedByLibrary() {
	lib1 := s.seedLibrary("folder", "/a")
	lib2 := s.seedLibrary("folder", "/b")
	s.seedBook(lib1, bookSeed{Title: "A", Authors: []string{"Ann"}})
	s.seedBook(lib2, bookSeed{Title: "B", Authors: []string{"Bob"}})

	// Letters are scoped to the library.
	w := s.do(http.MethodGet, browseURL("/authors/letters", "", lib1, 0, 0), nil)
	var letters []string
	s.decode(w, &letters)
	s.Equal([]string{"A"}, letters)

	// And so is the by-letter listing.
	scoped := s.do(http.MethodGet, browseURL("/authors", "A", lib1, 0, 0), nil)
	s.Require().Equal(http.StatusOK, scoped.Code)
	var authors []authorView
	s.decode(scoped, &authors)
	s.Require().Len(authors, 1)
	s.Equal("Ann", authors[0].Name)

	// 'B' (Bob) lives only in lib2, so it is empty under lib1.
	empty := s.do(http.MethodGet, browseURL("/authors", "B", lib1, 0, 0), nil)
	s.decode(empty, &authors)
	s.Empty(authors)
}

func (s *listsSuite) TestEmptyListsReturnArrays() {
	// No books seeded: the letters endpoints return [] and the list endpoints
	// (with no letter selected) return [] too — so the frontend can iterate
	// without guards.
	for _, path := range []string{"/authors", "/series", "/tags", "/publishers"} {
		letters := s.do(http.MethodGet, path+"/letters", nil)
		s.Require().Equal(http.StatusOK, letters.Code, path)
		s.Equal("[]\n", letters.Body.String(), path+"/letters")

		list := s.do(http.MethodGet, path, nil)
		s.Require().Equal(http.StatusOK, list.Code, path)
		s.Equal("[]\n", list.Body.String(), path)
	}
}

func (s *listsSuite) TestNonLetterListsHashBucket() {
	src := s.seedLibrary("folder", "/lib")
	s.seedBook(src, bookSeed{Title: "A", Series: "123 Series", Genres: []string{"456 Genre"}, Publisher: "789 Pub"})

	// Series '#' hash bucket
	wSeries := s.do(http.MethodGet, browseURL("/series", hashBucket, 0, 0, 0), nil)
	s.Require().Equal(http.StatusOK, wSeries.Code)
	var series []nameIDCountView
	s.decode(wSeries, &series)
	s.Require().Len(series, 1)
	s.Equal("123 Series", series[0].Name)

	// Tags '#' hash bucket
	wTags := s.do(http.MethodGet, browseURL("/tags", hashBucket, 0, 0, 0), nil)
	s.Require().Equal(http.StatusOK, wTags.Code)
	var tags []nameCountView
	s.decode(wTags, &tags)
	s.Require().Len(tags, 1)
	s.Equal("456 Genre", tags[0].Name)

	// Publishers '#' hash bucket
	wPubs := s.do(http.MethodGet, browseURL("/publishers", hashBucket, 0, 0, 0), nil)
	s.Require().Equal(http.StatusOK, wPubs.Code)
	var pubs []nameCountView
	s.decode(wPubs, &pubs)
	s.Require().Len(pubs, 1)
	s.Equal("789 Pub", pubs[0].Name)

	// Invalid letter bucket
	wInvalid := s.do(http.MethodGet, browseURL("/authors", "XYZ", 0, 0, 0), nil)
	s.Require().Equal(http.StatusOK, wInvalid.Code)
	s.Equal("null\n", wInvalid.Body.String())
}

func (s *listsSuite) TestLettersHelpers() {
	// bucketOf
	s.Equal(hashBucket, bucketOf(""))
	s.Equal(hashBucket, bucketOf("1"))
	s.Equal(hashBucket, bucketOf("@"))
	s.Equal("A", bucketOf("A"))

	// letterBounds
	lo, hi, ok := letterBounds("")
	s.False(ok)
	s.Empty(lo)
	s.Empty(hi)

	lo, hi, ok = letterBounds(hashBucket)
	s.False(ok)
	s.Empty(lo)
	s.Empty(hi)

	lo, hi, ok = letterBounds("@")
	s.False(ok)
	s.Empty(lo)
	s.Empty(hi)
}

func (s *listsSuite) TestListGenres() {
	w := s.do(http.MethodGet, "/genres", nil)
	s.Require().Equal(http.StatusOK, w.Code)

	var got []string
	s.decode(w, &got)
	s.NotEmpty(got)
	s.Contains(got, "Science Fiction")
}
