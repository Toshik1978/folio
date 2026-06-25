package metasearch

import (
	"fmt"
	"regexp"
	"strings"
)

// cdnSizeModifier matches the Amazon/Goodreads CDN size-modifier segment that
// appears between the image id and the file extension, e.g. "._AC_UL320_" or
// "._SX50_SY75_". Removing it yields the original full-resolution image.
//
// Pattern: a literal dot, underscore, one or more alphanumeric/comma/underscore
// chars, a trailing underscore, then dot + extension at end of string.
var cdnSizeModifier = regexp.MustCompile(`(?i)\._[A-Za-z0-9,_]+_\.(jpg|jpeg|png|gif)$`)

// replaceCDNModifier swaps the Amazon/Goodreads CDN size-modifier segment of url for
// repl (placed before the lower-cased extension), or returns url unchanged when
// it carries no recognizable modifier (e.g. a Google Books content URL). repl is
// "" to strip the modifier entirely, or "._SY450_" to resize.
func replaceCDNModifier(url, repl string) string {
	loc := cdnSizeModifier.FindStringSubmatchIndex(url)
	if loc == nil {
		return url
	}
	// loc[0] = match start; loc[2]:loc[3] = the extension capture group.
	ext := strings.ToLower(url[loc[2]:loc[3]])

	return url[:loc[0]] + repl + "." + ext
}

// OriginalCDNImage strips the CDN size-modifier segment (the Amazon/Goodreads
// `._AC_UL320_.`-style token) from url and returns the original full-resolution
// image URL. A URL with no recognized modifier (e.g. a Google Books content URL) is
// returned unchanged.
func OriginalCDNImage(url string) string {
	return replaceCDNModifier(url, "")
}

// ThumbCDNImage replaces the CDN size modifier with a uniform height-scaled modifier
// (_SY<height>_) for a crisp, aspect-preserving thumbnail, dropping the _AC_ adaptive
// crop and tiny modifiers. A URL with no recognized modifier is returned unchanged.
func ThumbCDNImage(url string, height int) string {
	return replaceCDNModifier(url, fmt.Sprintf("._SY%d_", height))
}
