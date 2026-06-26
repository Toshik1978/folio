package opds

import (
	"encoding/xml"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"strconv"
)

// maxPage caps the requested page number so (page-1)*defaultLimit cannot
// overflow int64 into a negative SQL OFFSET. Must stay in sync with
// internal/api/util.go's unexported maxPage constant. Note that opds uses
// defaultLimit = 50 while internal/api uses 24, so the offset multiplier
// differs between the two packages, but both stay well within int64 at this cap.
const maxPage = 1_000_000_000

// OPDS / Atom media types and link relations.
const (
	typeNavigation  = "application/atom+xml;profile=opds-catalog;kind=navigation"
	typeAcquisition = "application/atom+xml;profile=opds-catalog;kind=acquisition"
	typeOpenSearch  = "application/opensearchdescription+xml"
	// typeSearchInline is the media type of the inline templated search link.
	// Moon+ Reader, Librera, and Stanza do not fetch the OpenSearch description
	// document; they scan the feed for a rel="search" link whose href literally
	// contains {searchTerms}. This link carries that template directly.
	typeSearchInline = "application/atom+xml"

	relSelf        = "self"
	relStart       = "start"
	relSearch      = "search"
	relNext        = "next"
	relPrevious    = "previous"
	relSubsection  = "subsection"
	relAcquisition = "http://opds-spec.org/acquisition"
	relImage       = "http://opds-spec.org/image"
	relThumbnail   = "http://opds-spec.org/image/thumbnail"

	nsAtom = "http://www.w3.org/2005/Atom"
	nsOPDS = "http://opds-spec.org/2010/catalog"
	nsDC   = "http://purl.org/dc/terms/"
)

// feed is an Atom feed carrying an OPDS catalog (navigation or acquisition).
// Dublin Core elements use literal "dc:" prefixes with the namespace declared on
// the root, the conventional approach for OPDS readers.
type feed struct {
	XMLName   xml.Name `xml:"feed"`
	Xmlns     string   `xml:"xmlns,attr"`
	XmlnsOPDS string   `xml:"xmlns:opds,attr"`
	XmlnsDC   string   `xml:"xmlns:dc,attr"`
	ID        string   `xml:"id"`
	Title     string   `xml:"title"`
	Updated   string   `xml:"updated"`
	Links     []link   `xml:"link"`
	Entries   []entry  `xml:"entry"`
}

type link struct {
	Rel   string `xml:"rel,attr"`
	Href  string `xml:"href,attr"`
	Type  string `xml:"type,attr"`
	Title string `xml:"title,attr,omitempty"`
}

type entry struct {
	Title      string     `xml:"title"`
	ID         string     `xml:"id"`
	Updated    string     `xml:"updated"`
	Authors    []author   `xml:"author"`
	Links      []link     `xml:"link"`
	Categories []category `xml:"category"`
	Publisher  string     `xml:"dc:publisher,omitempty"`
	Issued     string     `xml:"dc:issued,omitempty"`
	Language   string     `xml:"dc:language,omitempty"`
	Content    *content   `xml:"content"`
}

type author struct {
	Name string `xml:"name"`
}

type category struct {
	Term string `xml:"term,attr"`
}

type content struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

// newFeed builds a feed with the standard namespaces and self/start links.
func newFeed(id, title, self, mediaType string) feed {
	return feed{
		Xmlns:     nsAtom,
		XmlnsOPDS: nsOPDS,
		XmlnsDC:   nsDC,
		ID:        id,
		Title:     title,
		Updated:   nowRFC3339(),
		Links: []link{
			{Rel: relSelf, Href: self, Type: mediaType},
			{Rel: relStart, Href: opdsPrefix + "/", Type: typeNavigation},
			{Rel: relSearch, Href: opdsPrefix + "/opensearch.xml", Type: typeOpenSearch},
			// Inline templated link for readers that don't dereference the
			// OpenSearch description document (Moon+ Reader, Librera, Stanza).
			// {searchTerms} must stay literal — do not URL-encode it.
			{Rel: relSearch, Href: opdsPrefix + "/search?q={searchTerms}", Type: typeSearchInline},
		},
	}
}

// selfHref returns the request's full OPDS URL (path plus any raw query) for use
// as the feed's rel="self" link.
func selfHref(r *http.Request, path string) string {
	if r.URL.RawQuery == "" {
		return path
	}
	return path + "?" + r.URL.RawQuery
}

// pageParam reads the 1-indexed ?page= query value, defaulting to 1 for a
// missing or malformed value.
func pageParam(r *http.Request) int64 {
	n, err := strconv.ParseInt(r.URL.Query().Get("page"), 10, 64)
	if err != nil || n < 1 {
		return 1
	}
	if n > maxPage {
		n = maxPage
	}

	return n
}

// addPageLinks appends rel="previous"/"next" navigation links to f for a feed
// at path carrying query params q. previous is added when page > 1; next when
// the current page came back full (more rows may follow).
func addPageLinks(f *feed, path string, q url.Values, page int64, full bool, mediaType string) {
	if page > 1 {
		f.Links = append(f.Links, link{Rel: relPrevious, Href: pageHref(path, q, page-1), Type: mediaType})
	}
	if full {
		f.Links = append(f.Links, link{Rel: relNext, Href: pageHref(path, q, page+1), Type: mediaType})
	}
}

// pageHref builds an href for path with q's params and page set to the given
// value. q is copied so the caller's values are left untouched.
func pageHref(path string, q url.Values, page int64) string {
	out := make(url.Values, len(q)+1)
	maps.Copy(out, q)
	out.Set("page", strconv.FormatInt(page, 10))

	return path + "?" + out.Encode()
}

// write marshals an OPDS feed to w with the correct content type.
func (h *Handler) write(w http.ResponseWriter, mediaType string, f feed) {
	w.Header().Set("Content-Type", mediaType)
	if _, err := w.Write([]byte(xml.Header)); err != nil {
		h.log.Error("write opds header", slog.Any("error", err))
		return
	}

	enc := xml.NewEncoder(w)
	defer func() { _ = enc.Close() }()

	enc.Indent("", "  ")
	if err := enc.Encode(f); err != nil {
		h.log.Error("encode opds feed", slog.Any("error", err))
	}
}
