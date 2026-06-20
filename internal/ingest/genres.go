//nolint:goconst,misspell
package ingest

import (
	"strings"

	dbf "github.com/Toshik1978/folio/internal/db"
)

// Source: https://raw.githubusercontent.com/gribuser/fb2/refs/heads/master/FictionBookGenres.xsd

// fb2GenreMap maps raw tags/codes (left-side) to canonical target tags (right-side).
var fb2GenreMap = map[string]string{
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
	"banking":                  "Finances",
	"beginning_authors":        "Nonfiction",
	"child_adv":                "Children's Book",
	"child_det":                "Children's Book",
	"child_education":          "Children's Book",
	"child_prose":              "Literary Fiction",
	"child_sf":                 "Science Fiction",
	"child_tale":               "Children's Book",
	"child_verse":              "Children's Book",
	"children":                 "Children's Book",
	"cinema_theatre":           "Nonfiction",
	"city_fantasy":             "Fantasy",
	"comp_db":                  "Computer Science",
	"comp_hard":                "Computer Science",
	"comp_osnet":               "Computer Science",
	"comp_programming":         "Programming",
	"comp_soft":                "Computer Science",
	"comp_www":                 "Computer Science",
	"computers":                "Computer Science",
	"design":                   "Software Design",
	"det_action":               "Thriller & Suspense",
	"det_classic":              "Classics",
	"det_crime":                "Crime Fiction",
	"det_espionage":            "Mystery",
	"det_hard":                 "Crime Fiction",
	"det_history":              "Mystery",
	"det_irony":                "Mystery",
	"det_police":               "Crime Fiction",
	"det_political":            "Mystery",
	"detective":                "Mystery",
	"dragon_fantasy":           "Fantasy",
	"dramaturgy":               "Poetry & Drama",
	"economics":                "Business",
	"essays":                   "Nonfiction",
	"fantasy_fight":            "Fantasy",
	"foreign_action":           "Nonfiction",
	"foreign_adventure":        "Action & Adventure",
	"foreign_antique":          "Classics",
	"foreign_business":         "Management & Leadership",
	"foreign_children":         "Children's Book",
	"foreign_comp":             "Nonfiction",
	"foreign_contemporary":     "Nonfiction",
	"foreign_contemporary_lit": "Nonfiction",
	"foreign_desc":             "Nonfiction",
	"foreign_detective":        "Mystery",
	"foreign_dramaturgy":       "Poetry & Drama",
	"foreign_edu":              "Nonfiction",
	"foreign_fantasy":          "Fantasy",
	"foreign_home":             "Nonfiction",
	"foreign_humor":            "Humor",
	"foreign_language":         "Nonfiction",
	"foreign_love":             "Romance",
	"foreign_novel":            "Nonfiction",
	"foreign_other":            "Nonfiction",
	"foreign_poetry":           "Poetry & Drama",
	"foreign_prose":            "Literary Fiction",
	"foreign_psychology":       "Nonfiction",
	"foreign_publicism":        "Nonfiction",
	"foreign_religion":         "Politics",
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
	"humor_prose":              "Literary Fiction",
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
	"management":               "Management & Leadership",
	"marketing":                "Management & Leadership",
	"military_special":         "War",
	"music_dancing":            "Music",
	"narrative":                "Nonfiction",
	"newspapers":               "Nonfiction",
	"nonf_biography":           "Biographies",
	"nonf_criticism":           "History & Criticism",
	"nonf_publicism":           "Nonfiction",
	"nonfiction":               "Nonfiction",
	"org_behavior":             "Management & Leadership",
	"paper_work":               "Nonfiction",
	"pedagogy_book":            "Self-Help & Psychology",
	"periodic":                 "Nonfiction",
	"personal_finance":         "Finances",
	"poetry":                   "Poetry & Drama",
	"popadanec":                "Fantasy",
	"popular_business":         "Management & Leadership",
	"prose_classic":            "Classics",
	"prose_counter":            "Literary Fiction",
	"prose_history":            "Literary Fiction",
	"prose_military":           "War",
	"prose_rus_classic":        "Classics",
	"prose_su_classics":        "Classics",
	"psy_alassic":              "Self-Help & Psychology",
	"psy_childs":               "Self-Help & Psychology",
	"psy_generic":              "Self-Help & Psychology",
	"psy_personal":             "Self-Help & Psychology",
	"psy_sex_and_family":       "Self-Help & Psychology",
	"psy_social":               "Self-Help & Psychology",
	"psy_theraphy":             "Self-Help & Psychology",
	"real_estate":              "Nonfiction",
	"ref_dict":                 "Science & Reference",
	"ref_encyc":                "Science & Reference",
	"ref_guide":                "Science & Reference",
	"ref_ref":                  "Science & Reference",
	"reference":                "Science & Reference",
	"religion":                 "Politics",
	"religion_esoterics":       "Politics",
	"religion_rel":             "Politics",
	"religion_self":            "Politics",
	"russian_contemporary":     "Nonfiction",
	"russian_fantasy":          "Fantasy",
	"sci_biology":              "Science & Reference",
	"sci_chem":                 "Science & Reference",
	"sci_culture":              "Nonfiction",
	"sci_history":              "History",
	"sci_juris":                "Nonfiction",
	"sci_linguistic":           "Nonfiction",
	"sci_math":                 "Science & Reference",
	"sci_medicine":             "Science & Reference",
	"sci_philosophy":           "Politics",
	"sci_phys":                 "Science & Reference",
	"sci_politics":             "Politics",
	"sci_religion":             "Politics",
	"sci_tech":                 "Nonfiction",
	"science":                  "Nonfiction",
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
	"small_business":           "Management & Leadership",
	"sociology_book":           "Politics",
	"stock":                    "Finances",
	"thriller":                 "Thriller & Suspense",
	"unrecognised":             "Nonfiction",
	"upbringing_book":          "Self-Help & Psychology",
	"vampire_book":             "Fantasy",
	"visual_arts":              "Nonfiction",
}

// targetGenres contains the set of all allowed target categories (right-side whitelist).
var targetGenres = map[string]struct{}{
	"Action & Adventure":      {},
	"Alternative History":     {},
	"Biographies":             {},
	"Business":                {},
	"Children's Book":         {},
	"Classics":                {},
	"Computer Science":        {},
	"Crime Fiction":           {},
	"Cyberpunk":               {},
	"Fantasy":                 {},
	"Finances":                {},
	"History":                 {},
	"History & Criticism":     {},
	"Humor":                   {},
	"Literary Fiction":        {},
	"Management & Leadership": {},
	"Music":                   {},
	"Mystery":                 {},
	"Nonfiction":              {},
	"Poetry & Drama":          {},
	"Politics":                {},
	"Programming":             {},
	"Romance":                 {},
	"Science & Reference":     {},
	"Science Fiction":         {},
	"Self-Help & Psychology":  {},
	"Software Design":         {},
	"Thriller & Suspense":     {},
	"Travel":                  {},
	"War":                     {},
	"AI & Machine Learning":   {},
	"Contemporary Fiction":    {},
	"DevOps":                  {},
	"Project Management":      {},
	"Russian":                 {},
	"Young Adults":            {},
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
