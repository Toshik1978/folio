package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	Env string `env:"APP_ENV" envDefault:"development"`
	// NoColor opts out of ANSI-colored logs. Per the NO_COLOR convention
	// (https://no-color.org) it is the var's *presence* that disables color, not
	// its value, so it is kept as a string and interpreted by NoColorEnabled.
	NoColor string `env:"NO_COLOR"`
	Port    string `env:"PORT"     envDefault:"8080"`
	DataDir string `env:"DATA_DIR" envDefault:"./data"`
	// PublicURL is the canonical external base URL (e.g. https://folio.example.com).
	// When set it is authoritative for the absolute URLs Folio advertises (the
	// OPDS OpenSearch template), so a forged X-Forwarded-Host cannot poison them.
	// Leave empty for local/direct access, where the request host is trusted.
	PublicURL string `env:"PUBLIC_URL"`
	// GoogleKey enables Google Books enrichment. Empty falls back to the
	// anonymous quota; enrichment is still attempted.
	GoogleKey string `env:"GOOGLE_KEY"`
	// LibraryRoot optionally confines every library path to this base directory
	// as defense-in-depth: with it set, an admin (or a stolen admin session)
	// cannot point a library at an arbitrary host path like /etc and serve files
	// back out. Empty (the default) leaves library paths unconstrained, matching
	// historical behavior. In container deployments set it to the mounted volume.
	LibraryRoot string `env:"LIBRARY_ROOT"`
}

func MustParse() Config {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		panic(err)
	}
	if err := cfg.Validate(); err != nil {
		panic(err)
	}

	return cfg
}

// Validate checks config values that would otherwise fail late or silently
// degrade at runtime, so misconfiguration is rejected at startup with a clear
// message instead. MustParse calls it and panics on failure.
func (c Config) Validate() error {
	if port, err := strconv.Atoi(c.Port); err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid PORT %q: must be a number in 1..65535", c.Port)
	}

	// PublicURL is optional, but when set it must parse to an absolute URL. An
	// unparsable value would otherwise degrade silently to "" in originOf, quietly
	// disabling the CORS/OPDS origin it was meant to pin.
	if c.PublicURL != "" {
		u, err := url.Parse(strings.TrimSpace(c.PublicURL))
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf(
				"invalid PUBLIC_URL %q: must be an absolute URL like https://folio.example.com",
				c.PublicURL,
			)
		}
	}

	return nil
}

// NoColorEnabled reports whether colored log output should be suppressed. Any
// non-empty NO_COLOR value counts as opt-out (per https://no-color.org).
func (c Config) NoColorEnabled() bool {
	return c.NoColor != ""
}
