package metasearch

import "errors"

// ErrBlocked indicates a source was refused by an anti-bot defense (a CAPTCHA
// interstitial, a Cloudflare challenge, or a rate-limit page) rather than
// returning a genuine empty result. Providers wrap it with %w; the aggregator
// uses errors.Is to log it distinctly from ordinary failures.
var ErrBlocked = errors.New("source blocked by anti-bot")
