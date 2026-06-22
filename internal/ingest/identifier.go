package ingest

import (
	"slices"
	"strings"

	"github.com/gofrs/uuid/v5"

	"github.com/Toshik1978/folio/internal/ebook"
)

const (
	isbnType      = "isbn"
	amazonType    = "amazon"
	googleType    = "google"
	goodreadsType = "goodreads"
)

// validStrongIdentifier reports whether a cleaned strong identifier is trustworthy
// enough to force two records onto the same book. cleanIdentifiers normalizes
// shape but never validates content, so a placeholder or garbage value (a
// copy-pasted/placeholder ISBN, an all-zero ASIN) could otherwise become a
// "strong" key and collapse two genuinely different books. ISBNs must pass a
// checksum; any strong type made of a single repeated character is rejected as a
// placeholder. Failing the check only disables grouping — the identifier is still
// stored for display.
func validStrongIdentifier(typ, val string) bool {
	if isPlaceholderIdentifier(val) {
		return false
	}
	if typ == isbnType {
		return ebook.LooksLikeISBN(val)
	}

	return true
}

// isPlaceholderIdentifier reports whether val is a single character repeated
// (e.g. "0000000000000", "XXXXXXXXXX") — a common stand-in for a missing
// identifier and never a real one.
func isPlaceholderIdentifier(val string) bool {
	if val == "" {
		return true
	}
	for i := 1; i < len(val); i++ {
		if val[i] != val[0] {
			return false
		}
	}

	return true
}

func cleanIdentifier(typ, val string) (string, string) {
	typ = strings.ToLower(strings.TrimSpace(typ))
	val = strings.TrimSpace(val)

	if typ == "" || val == "" {
		return "", ""
	}

	if isUselessScheme(typ) || isUUIDLike(val) {
		return "", ""
	}

	typ = mapType(typ)
	val = normalizeValue(typ, val)

	return typ, val
}

func isUselessScheme(typ string) bool {
	switch typ {
	case "uuid", "urn", "uri", "calibre", "mobi-uid", "local":
		return true
	}
	return false
}

func isUUIDLike(val string) bool {
	valLower := strings.ToLower(val)
	if strings.HasPrefix(valLower, "urn:uuid:") || strings.HasPrefix(valLower, "urn:calibre:") {
		return true
	}

	// Strip uuid: prefix before calling FromString, as FromString does not parse it
	checkVal := val
	if strings.HasPrefix(valLower, "uuid:") {
		checkVal = val[len("uuid:"):]
	}

	return uuid.FromStringOrNil(checkVal) != uuid.Nil
}

func mapType(typ string) string {
	switch typ {
	case "amazon-asin", "mobi-asin", "asin":
		return amazonType
	case "isbn-10", "isbn-13", "isbn10", "isbn13", "isbn_10", "isbn_13":
		// isbn_10/isbn_13 are Google Books' IndustryIdentifiers type strings.
		return isbnType
	default:
		return typ
	}
}

func normalizeValue(typ, val string) string {
	switch typ {
	case isbnType:
		valLower := strings.ToLower(val)
		if strings.HasPrefix(valLower, "urn:isbn:") {
			val = val[len("urn:isbn:"):]
		} else if strings.HasPrefix(valLower, "isbn:") {
			val = val[len("isbn:"):]
		}
		// Clean common ISBN artifacts: strip hyphens/spaces and uppercase
		val = strings.ReplaceAll(val, "-", "")
		val = strings.ReplaceAll(val, " ", "")

		return strings.ToUpper(val)
	case amazonType:
		// Normalize ASINs to uppercase
		return strings.ToUpper(val)
	default:
		return val
	}
}

func cleanIdentifiers(ids []identifier) map[string]string {
	bestIDs := make(map[string]string)

	for _, id := range ids {
		t, v := cleanIdentifier(id.Type, id.Value)
		if t == "" || v == "" {
			continue
		}

		existing, ok := bestIDs[t]
		if !ok {
			bestIDs[t] = v
			continue
		}

		// Conflict resolution rules
		if t == isbnType {
			bestIDs[t] = lookupISBN(v, existing)
		} else if len(v) >= len(existing) {
			// For other types, longer/non-empty string usually contains more data, or last one wins
			bestIDs[t] = v
		}
	}

	return bestIDs
}

// cleanedEbookIdentifiers normalizes and dedupes parser identifiers using the
// same rules as ingestion (cleanIdentifiers), returned as ebook.Identifier in
// type-sorted order. Used by the lazy metadata backfill and online enrichment so
// identifiers from any path land identical to what sync persists.
func cleanedEbookIdentifiers(ids []ebook.Identifier) []ebook.Identifier {
	clean := cleanIdentifiers(fromEbookIdentifiers(ids))
	types := make([]string, 0, len(clean))
	for t := range clean {
		types = append(types, t)
	}
	slices.Sort(types)

	out := make([]ebook.Identifier, 0, len(types))
	for _, t := range types {
		out = append(out, ebook.Identifier{Type: t, Value: clean[t]})
	}

	return out
}

func lookupISBN(v, existing string) string {
	// Prefer ISBN-13 over ISBN-10, and either over other lengths
	if len(v) == 13 {
		if len(existing) != 13 {
			return v
		}
	} else if len(v) == 10 {
		if len(existing) != 13 && len(existing) != 10 {
			return v
		}
	}

	return existing
}
