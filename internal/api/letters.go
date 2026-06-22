package api

import "unicode/utf8"

// The browse pages (authors/series/tags/publishers) load one alphabet bucket at
// a time instead of the whole table, which for a large Cyrillic library means
// 167k authors. A bucket is a single first letter; the catch-all '#' bucket
// collects everything that isn't a Cyrillic or Latin letter (digits,
// punctuation, lowercase, other scripts).
//
// The SQL counterparts in *.sql encode the same Cyrillic (U+0410..U+042F) and
// Latin (A..Z) ranges via char() literals; keep bucketOf and those queries in
// sync.

const hashBucket = "#"

// Cyrillic range А..Я and Latin range A..Z, as runes (uppercase only — proper
// names start uppercase, and binary collation sorts lowercase elsewhere).
const (
	cyrLo rune = 0x0410 // А
	cyrHi rune = 0x042F // Я
	latLo rune = 'A'
	latHi rune = 'Z'
)

func buildAlphabet() []string {
	out := make([]string, 0, int(cyrHi-cyrLo+1)+int(latHi-latLo+1)+1)
	for r := cyrLo; r <= cyrHi; r++ {
		out = append(out, string(r))
	}
	for r := latLo; r <= latHi; r++ {
		out = append(out, string(r))
	}

	return append(out, hashBucket)
}

// bucketOf maps a name's first character (as returned by the *FirstChars
// queries) to its alphabet bucket. Anything outside the Cyrillic/Latin letter
// ranges falls into '#'.
func bucketOf(firstChar string) string {
	if firstChar == "" {
		return hashBucket
	}
	r, _ := utf8.DecodeRuneInString(firstChar)
	if (r >= cyrLo && r <= cyrHi) || (r >= latLo && r <= latHi) {
		return string(r)
	}

	return hashBucket
}

// letterBounds returns the half-open [lo, hi) name range for a single-letter
// bucket, used to seek the name index. ok is false for '#' or any input that
// isn't one of the alphabet's letters (the '#' bucket has its own query).
func letterBounds(letter string) (lo, hi string, ok bool) {
	if letter == "" || letter == hashBucket {
		return "", "", false
	}
	runes := []rune(letter)
	r := runes[0]
	if len(runes) != 1 || (r < cyrLo || r > cyrHi) && (r < latLo || r > latHi) {
		return "", "", false
	}

	return string(r), string(r + 1), true
}

// availableLetters reduces the distinct first characters of an entity to the
// set of alphabet buckets that have data, returned in display order so the
// frontend can light up its selector and default to the first one.
func (h *CatalogHandler) availableLetters(firstChars []string) []string {
	present := make(map[string]bool, len(firstChars))
	for _, c := range firstChars {
		present[bucketOf(c)] = true
	}
	out := make([]string, 0, len(present))
	for _, letter := range h.alphabet {
		if present[letter] {
			out = append(out, letter)
		}
	}

	return out
}
