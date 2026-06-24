package metasearch

// math/rand/v2 provides a goroutine-safe PRNG without requiring external locking.
// Weak (non-cryptographic) randomness is intentional: UA rotation only needs
// unpredictability across requests, not security-grade entropy.
import "math/rand/v2"

// desktopUserAgents is a small pool of realistic current desktop browser UAs.
// Rotating them makes scraping look less like a single robotic client.
var desktopUserAgents = []string{ //nolint:gochecknoglobals // immutable lookup table
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0 Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64; rv:124.0) Gecko/20100101 Firefox/124.0",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7; rv:123.0) Gecko/20100101 Firefox/123.0",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko)" +
		" Chrome/124.0 Safari/537.36 Edg/124.0",
}

// RandomUserAgent returns a randomly chosen realistic desktop User-Agent.
func RandomUserAgent() string {
	return desktopUserAgents[rand.IntN(len(desktopUserAgents))] //nolint:gosec // weak rand is fine for UA rotation
}
