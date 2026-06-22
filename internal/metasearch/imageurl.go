package metasearch

import (
	"regexp"
	"strings"
)

// amazonSizeModifier matches the Amazon/Goodreads CDN size-modifier segment that
// appears between the image id and the file extension, e.g. "._AC_UL320_" or
// "._SX50_SY75_". Removing it yields the original full-resolution image.
//
// Pattern: a literal dot, underscore, one or more alphanumeric/comma/underscore
// chars, a trailing underscore, then dot + extension at end of string.
var amazonSizeModifier = regexp.MustCompile(`(?i)\._[A-Za-z0-9,_]+_\.(jpg|jpeg|png|gif)$`)

// OriginalAmazonImage strips the Amazon-CDN size-modifier segment from url and
// returns the original full-resolution image URL. If url contains no modifier
// (or is not an Amazon CDN URL) it is returned unchanged.
//
// Examples:
//
//	"https://m.media-amazon.com/images/I/71abc._AC_UL320_.jpg"
//	  → "https://m.media-amazon.com/images/I/71abc.jpg"
//
//	"https://images-na.ssl-images-amazon.com/images/S/abc._SX50_.jpg"
//	  → "https://images-na.ssl-images-amazon.com/images/S/abc.jpg"
func OriginalAmazonImage(url string) string {
	loc := amazonSizeModifier.FindStringIndex(url)
	if loc == nil {
		return url
	}
	// FindStringSubmatch to grab the captured extension group.
	m := amazonSizeModifier.FindStringSubmatch(url)
	if m == nil {
		return url
	}
	ext := strings.ToLower(m[1])

	return url[:loc[0]] + "." + ext
}
