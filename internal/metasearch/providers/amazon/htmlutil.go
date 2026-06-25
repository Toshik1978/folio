package amazon

import "golang.org/x/net/html"

// acceptHTML is the Accept header sent for Amazon HTML fetches.
const acceptHTML = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"

// attr returns n's value for the named attribute, or "".
func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}

	return ""
}

// findNode returns the first node in n's subtree (preorder, n included) for
// which pred holds, or nil. It only considers element nodes.
func findNode(n *html.Node, pred func(*html.Node) bool) *html.Node {
	if n.Type == html.ElementNode && pred(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if got := findNode(c, pred); got != nil {
			return got
		}
	}

	return nil
}
