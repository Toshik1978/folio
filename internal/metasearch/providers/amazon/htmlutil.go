package amazon

import (
	"slices"
	"strings"

	"golang.org/x/net/html"
)

// acceptHTML is the Accept header sent for Amazon/DDG HTML fetches.
const acceptHTML = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"

// hasClass reports whether n's class attribute contains the given class token.
func hasClass(n *html.Node, class string) bool {
	return slices.Contains(strings.Fields(attr(n, "class")), class)
}

// attr returns n's value for the named attribute, or "".
func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}

	return ""
}
