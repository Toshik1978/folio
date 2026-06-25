package metasearch

import (
	"fmt"
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

// replaceAmazonModifier swaps the Amazon-CDN size-modifier segment of url for
// repl (placed before the lower-cased extension), or returns url unchanged when
// it carries no recognizable modifier (e.g. a Google Books content URL). repl is
// "" to strip the modifier entirely, or "._SY450_" to resize.
func replaceAmazonModifier(url, repl string) string {
	loc := amazonSizeModifier.FindStringIndex(url)
	if loc == nil {
		return url
	}
	m := amazonSizeModifier.FindStringSubmatch(url)
	if m == nil {
		return url
	}

	return url[:loc[0]] + repl + "." + strings.ToLower(m[1])
}

// OriginalAmazonImage strips the Amazon-CDN size-modifier segment from url and
// returns the original full-resolution image URL.
//
// Examples:
//
//	"https://m.media-amazon.com/images/I/71abc._AC_UL320_.jpg"
//	  → "https://m.media-amazon.com/images/I/71abc.jpg"
//
//	"https://images-na.ssl-images-amazon.com/images/S/abc._SX50_.jpg"
//	  → "https://images-na.ssl-images-amazon.com/images/S/abc.jpg"
func OriginalAmazonImage(url string) string {
	return replaceAmazonModifier(url, "")
}

// ThumbAmazonImage returns url with its Amazon-CDN size modifier replaced by a
// uniform height-scaled modifier (_SY<height>_), yielding a crisp,
// aspect-preserving thumbnail. It deliberately drops the _AC_ "adaptive crop"
// modifier, which squares or pads some covers, and replaces tiny modifiers like
// _SY75_ that render blurry.
func ThumbAmazonImage(url string, height int) string {
	return replaceAmazonModifier(url, fmt.Sprintf("._SY%d_", height))
}
