package mcpserver

import (
	"strconv"
	"strings"
)

// bookLink returns the absolute Folio URL of a book, or a root-relative path
// when no public URL is configured.
func bookLink(publicURL string, id int64) string {
	return strings.TrimRight(publicURL, "/") + "/books/" + strconv.FormatInt(id, 10)
}
