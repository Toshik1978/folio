package notify

import (
	"strings"
	"unicode/utf8"
)

// Kind identifies a notification message type.
type Kind uint8

// Notification kinds.
const (
	KindTest Kind = iota
	KindCrash
	KindCrashRepeat
	KindRunFailed
	KindRunRecovered
	KindUnreadable
	KindMassRemoval
	KindErrorSummary
)

const (
	// maxMessageChars is Telegram's per-message limit, counted in characters (runes).
	maxMessageChars = 4000
	// maxItemChars caps each dynamic item (error text, panic value, reason).
	maxItemChars = 300
	// headerLines are the prefix and headline, which are never cut.
	headerLines = 2
	truncMark   = "… (truncated)"
)

// prefix is line 1 of every message: PUBLIC_URL without a trailing slash, or
// "Folio" when unset.
func prefix(publicURL string) string {
	p := strings.TrimRight(strings.TrimSpace(publicURL), "/")
	if p == "" {
		return "Folio"
	}
	return p
}

// oneLine collapses all whitespace runs (newlines, tabs, ...) to single
// spaces and caps the result at limit runes, ending in "…" when cut.
func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if limit <= 0 || utf8.RuneCountInString(s) <= limit {
		return s
	}
	r := []rune(s)

	return string(r[:limit-1]) + "…"
}

// item collapses a dynamic item (error text, panic value, reason) to one line
// of at most maxItemChars runes.
func item(s string) string { return oneLine(s, maxItemChars) }

// truncate caps text at maxMessageChars runes. When cut, whole trailing lines
// are dropped and "… (truncated)" is appended; the first two lines (prefix and
// headline) are always kept.
func truncate(text string) string {
	if utf8.RuneCountInString(text) <= maxMessageChars {
		return text
	}
	lines := strings.Split(text, "\n")
	suffix := "\n" + truncMark
	size := utf8.RuneCountInString(suffix)
	keep := 0
	for i, l := range lines {
		n := utf8.RuneCountInString(l)
		if i > 0 {
			n++
		}
		if i >= headerLines && size+n > maxMessageChars {
			break
		}
		size += n
		keep = i + 1
	}

	return strings.Join(lines[:keep], "\n") + suffix
}

// testText is the KindTest message.
func testText(prefix string) string {
	return prefix + "\nTest message: Telegram notifications are working."
}
