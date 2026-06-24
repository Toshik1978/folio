package amazon

import (
	"strings"
	"unicode"

	"github.com/Toshik1978/folio/internal/metasearch"
)

// maxCandidates caps how many Amazon thumbnails a single search contributes to
// the picker grid. Amazon sorts results by relevance, so the long tail is noise.
const maxCandidates = 6

// rawCandidate pairs a parsed cover with its result title (the s-image alt),
// kept only long enough to relevance-filter before returning public candidates.
type rawCandidate struct {
	cover metasearch.CoverCandidate
	title string
}

// titleStopwords are ignored when matching titles so that articles and
// conjunctions don't make unrelated books look similar.
var titleStopwords = map[string]bool{ //nolint:gochecknoglobals // immutable lookup table
	"the": true, "a": true, "an": true, "of": true, "and": true, "to": true,
}

// junkTitleMarkers flag non-print or non-primary editions whose covers pollute
// the grid (square audiobook art, samplers, bundles, companions).
var junkTitleMarkers = []string{ //nolint:gochecknoglobals // immutable lookup table
	"audiobook", "audio cd", "audible audiobook",
	"bulk pack", "free sampler", "(a book companion)",
}

// filterByTitle keeps, in Amazon's relevance order, up to maxCount candidates whose
// title contains every significant token of the query title and is not a junk
// edition. It fails open — returning the top maxCount unfiltered — when the query
// title is empty or no candidate carries a title (Amazon markup drift), so the
// source degrades gracefully instead of going dark.
//
//nolint:unparam // maxCount parameter enables flexible testing
func filterByTitle(cands []rawCandidate, queryTitle string, maxCount int) []metasearch.CoverCandidate {
	qt := titleTokens(queryTitle)
	anyTitle := false
	for _, c := range cands {
		if c.title != "" {
			anyTitle = true

			break
		}
	}
	filter := len(qt) > 0 && anyTitle

	out := make([]metasearch.CoverCandidate, 0, maxCount)
	for _, c := range cands {
		if filter && (isJunkTitle(c.title) || !titleMatches(qt, c.title)) {
			continue
		}
		out = append(out, c.cover)
		if len(out) >= maxCount {
			break
		}
	}

	return out
}

// titleTokens normalizes s to lowercase significant word tokens: every
// non-alphanumeric rune becomes a separator and stopwords are dropped.
func titleTokens(s string) []string {
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

// titleMatches reports whether candidate title cand contains every token in
// queryTokens.
func titleMatches(queryTokens []string, cand string) bool {
	have := make(map[string]bool)
	for _, t := range titleTokens(cand) {
		have[t] = true
	}
	for _, q := range queryTokens {
		if !have[q] {
			return false
		}
	}

	return true
}

// isJunkTitle reports whether title names a non-primary edition we never want
// in the cover grid.
func isJunkTitle(title string) bool {
	low := strings.ToLower(title)
	for _, m := range junkTitleMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}

	return false
}
