//nolint:goconst,misspell
package ingest

import (
	"slices"
	"strings"

	dbf "github.com/Toshik1978/folio/internal/db"
)

// Source: https://raw.githubusercontent.com/gribuser/fb2/refs/heads/master/FictionBookGenres.xsd
//
// Target tags (right-side / targetGenres) are BISAC subject labels, so that the
// segments Google Books returns in VolumeInfo.Categories (which are BISAC-derived,
// e.g. "Fiction / Science Fiction / Space Opera") match verbatim and enrichment
// fills the same shelves a Calibre library already uses. Two intentional non-BISAC
// buckets are kept: "Nonfiction" (the catch-all; BISAC has no such subject) and
// "Contemporary Fiction" (BISAC only has "Contemporary" under specific genres).

// fb2GenreMap maps raw tags/codes (left-side) to canonical target tags (right-side).
var fb2GenreMap = map[string]string{ //nolint:gochecknoglobals // read-only lookup table
	"accounting":               "Nonfiction",
	"adv_animal":               "Action & Adventure",
	"adv_geo":                  "Travel",
	"adv_history":              "History",
	"adv_maritime":             "Action & Adventure",
	"adv_western":              "Action & Adventure",
	"adventure":                "Action & Adventure",
	"antique":                  "Classics",
	"antique_ant":              "Classics",
	"antique_east":             "Classics",
	"antique_european":         "Classics",
	"antique_myths":            "Classics",
	"antique_russian":          "Classics",
	"aphorism_quote":           "Nonfiction",
	"architecture_book":        "Nonfiction",
	"auto_regulations":         "Nonfiction",
	"banking":                  "Finance",
	"beginning_authors":        "Nonfiction",
	"child_adv":                "Juvenile Fiction",
	"child_det":                "Juvenile Fiction",
	"child_education":          "Juvenile Fiction",
	"child_prose":              "Literary",
	"child_sf":                 "Science Fiction",
	"child_tale":               "Juvenile Fiction",
	"child_verse":              "Juvenile Fiction",
	"children":                 "Juvenile Fiction",
	"cinema_theatre":           "Nonfiction",
	"city_fantasy":             "Fantasy",
	"comp_db":                  "Computer Science",
	"comp_hard":                "Computer Science",
	"comp_osnet":               "Computer Science",
	"comp_programming":         "Programming",
	"comp_soft":                "Computer Science",
	"comp_www":                 "Computer Science",
	"computers":                "Computer Science",
	"design":                   "Software Development & Engineering",
	"det_action":               "Thrillers",
	"det_classic":              "Classics",
	"det_crime":                "Crime",
	"det_espionage":            "Mystery & Detective",
	"det_hard":                 "Crime",
	"det_history":              "Mystery & Detective",
	"det_irony":                "Mystery & Detective",
	"det_police":               "Crime",
	"det_political":            "Mystery & Detective",
	"detective":                "Mystery & Detective",
	"dragon_fantasy":           "Fantasy",
	"dramaturgy":               "Drama",
	"economics":                "Business & Economics",
	"essays":                   "Nonfiction",
	"fantasy_fight":            "Fantasy",
	"foreign_action":           "Nonfiction",
	"foreign_adventure":        "Action & Adventure",
	"foreign_antique":          "Classics",
	"foreign_business":         "Management",
	"foreign_children":         "Juvenile Fiction",
	"foreign_comp":             "Nonfiction",
	"foreign_contemporary":     "Contemporary Fiction",
	"foreign_contemporary_lit": "Contemporary Fiction",
	"foreign_desc":             "Nonfiction",
	"foreign_detective":        "Mystery & Detective",
	"foreign_dramaturgy":       "Drama",
	"foreign_edu":              "Nonfiction",
	"foreign_fantasy":          "Fantasy",
	"foreign_home":             "Nonfiction",
	"foreign_humor":            "Humor",
	"foreign_language":         "Nonfiction",
	"foreign_love":             "Romance",
	"foreign_novel":            "Nonfiction",
	"foreign_other":            "Nonfiction",
	"foreign_poetry":           "Poetry",
	"foreign_prose":            "Literary",
	"foreign_psychology":       "Nonfiction",
	"foreign_publicism":        "Nonfiction",
	"foreign_religion":         "Religion",
	"foreign_sf":               "Science Fiction",
	"geo_guides":               "Travel",
	"geography_book":           "Travel",
	"global_economy":           "Nonfiction",
	"historical_fantasy":       "Fantasy",
	"home":                     "Nonfiction",
	"home_cooking":             "Nonfiction",
	"home_crafts":              "Nonfiction",
	"home_diy":                 "Nonfiction",
	"home_entertain":           "Nonfiction",
	"home_garden":              "Nonfiction",
	"home_health":              "Nonfiction",
	"home_pets":                "Nonfiction",
	"home_sex":                 "Nonfiction",
	"home_sport":               "Nonfiction",
	"humor":                    "Humor",
	"humor_anecdote":           "Humor",
	"humor_fantasy":            "Fantasy",
	"humor_prose":              "Literary",
	"humor_verse":              "Humor",
	"industries":               "Nonfiction",
	"job_hunting":              "Nonfiction",
	"literature_18":            "Nonfiction",
	"literature_19":            "Nonfiction",
	"literature_20":            "Nonfiction",
	"love_contemporary":        "Romance",
	"love_detective":           "Romance",
	"love_erotica":             "Romance",
	"love_fantasy":             "Romance",
	"love_history":             "Romance",
	"love_sf":                  "Romance",
	"love_short":               "Romance",
	"magician_book":            "Fantasy",
	"management":               "Management",
	"marketing":                "Management",
	"military_special":         "War & Military",
	"music_dancing":            "Music",
	"narrative":                "Nonfiction",
	"newspapers":               "Nonfiction",
	"nonf_biography":           "Biography & Autobiography",
	"nonf_criticism":           "History & Criticism",
	"nonf_publicism":           "Nonfiction",
	"nonfiction":               "Nonfiction",
	"org_behavior":             "Management",
	"paper_work":               "Nonfiction",
	"pedagogy_book":            "Psychology",
	"periodic":                 "Nonfiction",
	"personal_finance":         "Finance",
	"poetry":                   "Poetry",
	"popadanec":                "Fantasy",
	"popular_business":         "Management",
	"prose_classic":            "Classics",
	"prose_counter":            "Literary",
	"prose_history":            "Literary",
	"prose_military":           "War & Military",
	"prose_rus_classic":        "Classics",
	"prose_su_classics":        "Classics",
	"psy_alassic":              "Psychology",
	"psy_childs":               "Psychology",
	"psy_generic":              "Psychology",
	"psy_personal":             "Psychology",
	"psy_sex_and_family":       "Psychology",
	"psy_social":               "Psychology",
	"psy_theraphy":             "Psychology",
	"real_estate":              "Nonfiction",
	"ref_dict":                 "Science",
	"ref_encyc":                "Science",
	"ref_guide":                "Science",
	"ref_ref":                  "Science",
	"reference":                "Science",
	"religion":                 "Religion",
	"religion_esoterics":       "Religion",
	"religion_rel":             "Religion",
	"religion_self":            "Religion",
	"russian_contemporary":     "Contemporary Fiction",
	"russian_fantasy":          "Fantasy",
	"sci_biology":              "Science",
	"sci_chem":                 "Science",
	"sci_culture":              "Nonfiction",
	"sci_history":              "History",
	"sci_juris":                "Nonfiction",
	"sci_linguistic":           "Nonfiction",
	"sci_math":                 "Science",
	"sci_medicine":             "Science",
	"sci_philosophy":           "Philosophy",
	"sci_phys":                 "Science",
	"sci_politics":             "Political Science",
	"sci_religion":             "Religion",
	"sci_tech":                 "Nonfiction",
	"science":                  "Science",
	"sf":                       "Science Fiction",
	"sf_action":                "Science Fiction",
	"sf_cyberpunk":             "Cyberpunk",
	"sf_detective":             "Science Fiction",
	"sf_fantasy":               "Fantasy",
	"sf_heroic":                "Science Fiction",
	"sf_history":               "Alternative History",
	"sf_horror":                "Science Fiction",
	"sf_humor":                 "Science Fiction",
	"sf_social":                "Science Fiction",
	"sf_space":                 "Science Fiction",
	"short_story":              "Nonfiction",
	"sketch":                   "Nonfiction",
	"small_business":           "Management",
	"sociology_book":           "Political Science",
	"stock":                    "Finance",
	"thriller":                 "Thrillers",
	"unrecognised":             "Nonfiction",
	"upbringing_book":          "Psychology",
	"vampire_book":             "Fantasy",
	"visual_arts":              "Nonfiction",
}

// targetGenres contains the set of all allowed target categories (right-side whitelist).
// Every entry is a valid BISAC subject label except the two intentional buckets
// "Nonfiction" and "Contemporary Fiction" (BISAC has no equivalent for either).
var targetGenres = map[string]struct{}{ //nolint:gochecknoglobals // read-only set
	"Action & Adventure":                 {},
	"Alternative History":                {},
	"Artificial Intelligence":            {},
	"Biography & Autobiography":          {},
	"Business & Economics":               {},
	"Classics":                           {},
	"Computer Science":                   {},
	"Contemporary Fiction":               {},
	"Crime":                              {},
	"Cyberpunk":                          {},
	"Drama":                              {},
	"Fantasy":                            {},
	"Finance":                            {},
	"History":                            {},
	"History & Criticism":                {},
	"Humor":                              {},
	"Juvenile Fiction":                   {},
	"Leadership":                         {},
	"Literary":                           {},
	"Management":                         {},
	"Music":                              {},
	"Mystery & Detective":                {},
	"Nonfiction":                         {},
	"Philosophy":                         {},
	"Poetry":                             {},
	"Political Science":                  {},
	"Programming":                        {},
	"Project Management":                 {},
	"Psychology":                         {},
	"Religion":                           {},
	"Romance":                            {},
	"Science":                            {},
	"Science Fiction":                    {},
	"Software Development & Engineering": {},
	"Thrillers":                          {},
	"Travel":                             {},
	"War & Military":                     {},
	"Young Adult Fiction":                {},
}

// CanonicalGenres returns the BISAC-aligned taxonomy (the targetGenres
// whitelist) sorted alphabetically. It feeds the manual-edit genre autocomplete
// so a user can only assign genres the enrichment pipeline also recognizes.
func CanonicalGenres() []string {
	out := make([]string, 0, len(targetGenres))
	for g := range targetGenres {
		out = append(out, g)
	}
	slices.Sort(out)

	return out
}

// normalizeGenre normalizes a tag against the taxonomy. It returns the mapped tag
// and true if it belongs to the taxonomy, or empty string and false if it should be discarded.
func normalizeGenre(tag string) (string, bool) {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return "", false
	}

	tagLower := strings.ToLower(tag)
	if target, ok := fb2GenreMap[tagLower]; ok {
		return target, true
	}

	for canonical := range targetGenres {
		if strings.EqualFold(tag, canonical) {
			return canonical, true
		}
	}

	return "", false
}

// CanonicalizeGenres maps raw genre tags through the FB2 taxonomy and removes
// duplicates. It is the single normalization both the importer (insert.go) and
// the API edit/enrich paths (api/enrich.go) use, so a genre stored via Fix Match
// matches one stored during a sync.
func CanonicalizeGenres(tags []string) []string {
	return deduplicate(normalizeGenres(tags))
}

func normalizeGenres(tags []string) []string {
	cleanedGenres := make([]string, 0, len(tags))
	for _, g := range tags {
		cleanedGenres = append(cleanedGenres, normalizeRawTag(g)...)
	}

	return cleanedGenres
}

// normalizeRawTag maps one raw category to zero or more canonical taxonomy genres.
// A whole-string match wins; otherwise a BISAC-style "A / B / C" path (as Google
// Books returns its categories) is split on "/" and each segment matched, so a
// path like "Fiction / Science Fiction / Space Opera" still contributes its
// recognized segment(s). Every result is whitelist-bounded — splitting only
// widens what we recognize, never what we accept.
func normalizeRawTag(tag string) []string {
	if normalized, ok := normalizeGenre(tag); ok {
		return []string{normalized}
	}
	if !strings.Contains(tag, "/") {
		return nil
	}

	var out []string
	for segment := range strings.SplitSeq(tag, "/") {
		if normalized, ok := normalizeGenre(segment); ok {
			out = append(out, normalized)
		}
	}

	return out
}

func deduplicate(s []string) []string {
	seen := make(map[string]bool)
	var unique []string
	for _, t := range s {
		trimmed := strings.TrimSpace(t)
		if trimmed == "" {
			continue
		}
		fold := dbf.Fold(trimmed)
		if !seen[fold] {
			seen[fold] = true
			unique = append(unique, trimmed)
		}
	}

	return unique
}
