package ingest

import (
	"strings"

	"golang.org/x/text/language"
)

// iso6392BToT maps bibliographic codes (B) to terminology codes (T).
var iso6392BToT = map[string]string{ //nolint:gochecknoglobals // read-only lookup table
	"alb": "sqi", // Albanian
	"arm": "hye", // Armenian
	"baq": "eus", // Basque
	"bur": "mya", // Burmese
	"chi": "zho", // Chinese
	"cze": "ces", // Czech
	"dut": "nld", // Dutch
	"fre": "fra", // French
	"geo": "kat", // Georgian
	"ger": "deu", // German
	"gre": "ell", // Greek
	"ice": "isl", // Icelandic
	"mac": "mkd", // Macedonian
	"mao": "mri", // Maori
	"may": "msa", // Malay
	"per": "fas", // Persian
	"rum": "ron", // Romanian
	"slo": "slk", // Slovak
	"tib": "bod", // Tibetan
	"wel": "cym", // Welsh
}

// normalizeLang canonicalizes a language code to its base ISO 639-1 subtag so
// every source shares one facet bucket. It lowercases, maps ISO 639-2/B to /T,
// then reduces full BCP 47 tags to the base language — collapsing region and
// script subtags ("en-US", "en-Latn-US" → "en"). Applied at every ingest source
// (Calibre, folder/ebook, INPX) before grouping so editions of the same work
// don't split on a region suffix.
//
// It returns "" for anything it cannot resolve to a real base language —
// missing, the "und" sentinel, or junk like "english" — rather than letting a
// stray string become its own language facet. Callers coalesce "" to the stored
// "und" sentinel (insert.go, groupKey); keeping it empty here also means junk
// never clobbers a sibling edition's good language in the merge overwrite path.
func normalizeLang(code string) string {
	cleaned := strings.ToLower(strings.TrimSpace(code))
	if cleaned == "" || cleaned == undefinedLanguage {
		return ""
	}

	// If it's a bibliographic code, replace it with the terminology one.
	if tCode, safe := iso6392BToT[cleaned]; safe {
		cleaned = tCode
	}

	// Parse the full tag (not just the base) so a region/script suffix like
	// "en-US" still resolves to its base language rather than being rejected.
	// language.Parse("und") guesses a base of "en", but the guard above already
	// excluded it, so anything reaching here that fails to parse is genuine junk.
	if tag, err := language.Parse(cleaned); err == nil {
		if base, conf := tag.Base(); conf != language.No {
			return base.String()
		}
	}

	return ""
}
