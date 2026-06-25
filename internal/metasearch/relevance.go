package metasearch

import (
	"strconv"
	"strings"
	"unicode"
)

// This file holds the cover-relevance filter shared by every provider. Scraped
// search results (Amazon, Goodreads, …) routinely include box sets, omnibuses,
// audiobooks, and study guides whose cover art is not the requested single
// book. Filtering them by title is provider-agnostic, so it lives here rather
// than in any one provider.

// titleStopwords are ignored when matching titles so that articles and
// conjunctions don't make unrelated books look similar.
var titleStopwords = map[string]bool{ //nolint:gochecknoglobals // immutable lookup table
	"the": true, "a": true, "an": true, "of": true, "and": true, "to": true,
}

// junkTitleMarkers flag non-print or non-primary editions whose covers pollute
// the grid (audiobook art, samplers, companions, study aids).
var junkTitleMarkers = []string{ //nolint:gochecknoglobals // immutable lookup table
	"audiobook", "audio cd", "audible audiobook",
	"bulk pack", "free sampler", "(a book companion)",
	"study guide", "supersummary", "sparknotes", "cliffsnotes",
}

// collectionTokens flag multi-book products (box sets, collections, omnibuses,
// trilogies) whose cover art shows several books — the wide "rectangle" covers —
// rather than the requested single title. Matched as whole tokens so a single
// book titled e.g. "Mindset" or "Sunset" is not swept up. Sellers spell these
// many ways ("Boxed Set", "3 Books Set", "7-book Collection Set", "The
// Three-Body Trilogy"), all of which carry one of these tokens.
var collectionTokens = map[string]bool{ //nolint:gochecknoglobals // immutable lookup table
	"set": true, "collection": true, "boxset": true,
	"omnibus": true, "anthology": true, "bundle": true, "trilogy": true,
}

// StripQualifiers removes parenthetical and bracketed spans from a query title,
// e.g. the trailing "(Remembrance of Earth's Past)" series qualifier. Real
// single-book listings rarely repeat the series name, so requiring those words
// would drop the correct cover and keep only the box sets that spell the series
// out. Dropping them keeps the series words from being mandatory while still
// allowing them to appear in a candidate.
func StripQualifiers(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '(', '[':
			depth++
		case ')', ']':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}

	return b.String()
}

// TitleTokens normalizes s to lowercase significant word tokens: every
// non-alphanumeric rune becomes a separator and stopwords are dropped.
func TitleTokens(s string) []string {
	mapped := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}

		return ' '
	}, s)

	var out []string
	for t := range strings.FieldsSeq(mapped) {
		if !titleStopwords[t] {
			out = append(out, t)
		}
	}

	return out
}

// TitleMatchesTokens reports whether candidate title cand contains every token
// in queryTokens.
func TitleMatchesTokens(queryTokens []string, cand string) bool {
	have := make(map[string]bool)
	for _, t := range TitleTokens(cand) {
		have[t] = true
	}
	for _, q := range queryTokens {
		if !have[q] {
			return false
		}
	}

	return true
}

// IsJunkTitle reports whether title names an edition we never want in the cover
// grid: a non-primary edition (box set, omnibus, study guide, …) or a
// foreign-script (CJK) edition whose cover is not the requested one.
func IsJunkTitle(title string) bool {
	if hasCJK(title) {
		return true
	}
	low := strings.ToLower(title)
	for _, m := range junkTitleMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	tokens := TitleTokens(title)
	for _, t := range tokens {
		if collectionTokens[t] {
			return true
		}
	}

	return hasMultiBookCount(tokens)
}

// hasCJK reports whether s contains Han / Hiragana / Katakana / Hangul
// characters, used to drop foreign-language editions.
func hasCJK(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			return true
		}
	}

	return false
}

// hasMultiBookCount reports whether tokens contain a "<n> book(s)" run with n>1,
// e.g. "3 Books", "4 books", "7-book" — common wording for a multi-volume bundle
// whose cover shows several books. The count must PRECEDE the word, so a single
// volume's "Book 3" / "Series, 2" is not mistaken for a bundle.
func hasMultiBookCount(tokens []string) bool {
	for i := 0; i+1 < len(tokens); i++ {
		if tokens[i+1] != "book" && tokens[i+1] != "books" {
			continue
		}
		if n, err := strconv.Atoi(tokens[i]); err == nil && n > 1 {
			return true
		}
	}

	return false
}

// TitleAcceptable reports whether a candidate carrying candTitle is a wanted
// single-cover for queryTitle. It is the per-candidate entry point for providers
// whose every candidate has a title (e.g. Goodreads): junk editions are always
// rejected, and a title-token match is required unless the query has no
// significant tokens or the candidate carries no title (fail open on the match
// only, never on junk).
func TitleAcceptable(queryTitle, candTitle string) bool {
	if IsJunkTitle(candTitle) {
		return false
	}
	qt := TitleTokens(StripQualifiers(queryTitle))
	if len(qt) == 0 || strings.TrimSpace(candTitle) == "" {
		return true
	}

	return TitleMatchesTokens(qt, candTitle)
}
