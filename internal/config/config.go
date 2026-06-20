package config

import "github.com/caarlos0/env/v11"

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
}

func MustParse() Config {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		panic(err)
	}
	return cfg
}

// NoColorEnabled reports whether colored log output should be suppressed. Any
// non-empty NO_COLOR value counts as opt-out (per https://no-color.org).
func (c Config) NoColorEnabled() bool {
	return c.NoColor != ""
}
