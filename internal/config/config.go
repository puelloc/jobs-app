// config.go: environment-driven configuration for the scraper.
// design: Inputs table (env vars, defaults, empty string means unset).
package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// Defaults, per the Inputs table in docs/scraper-design.md.
const (
	DefaultDBPath     = "./jobs.db"
	DefaultDataDir    = "./data"
	DefaultLogLevel   = "info"
	DefaultServerAddr = "127.0.0.1:8080"

	// DefaultEndpoint is the RemoteOK URL recorded in
	// internal/db/migrations/002_seed_platforms.sql for platform id 1. It is the
	// JSON feed endpoint: the previous value, https://remoteok.com/remote-dev-jobs,
	// returns the HTML listing page and made the parse step fail.
	DefaultEndpoint = "https://remoteok.com/api"

	DefaultUserAgent = "jobs-app/0.1 (+https://github.com/local/jobs-app)"

	// DefaultHTTPTimeout is the whole-request deadline (design: Inputs table).
	DefaultHTTPTimeout = 30 * time.Second
)

// Config holds the resolved runtime settings. No I/O happens in this package.
type Config struct {
	DBPath      string
	DataDir     string
	Endpoint    string
	UserAgent   string
	HTTPTimeout time.Duration
	LogLevel    string
	ServerAddr  string

	// SearxngURL is the base URL of a SearXNG instance, e.g.
	// https://search.siggy-lab.org. Empty means the searxng resolve phase is
	// disabled: SearXNG is an optional dependency, not a required one. The value
	// is stored with any trailing slash trimmed.
	SearxngURL string
}

// SearxngEnabled reports whether a SearXNG instance is configured.
func (c Config) SearxngEnabled() bool { return c.SearxngURL != "" }

// Load resolves configuration from the environment, applying the documented
// defaults. An empty string is treated as unset. It performs no I/O.
func Load() (Config, error) {
	cfg := Config{
		DBPath:      envOrDefault("DB_PATH", DefaultDBPath),
		DataDir:     envOrDefault("DATA_DIR", DefaultDataDir),
		Endpoint:    envOrDefault("REMOTEOK_ENDPOINT", DefaultEndpoint),
		UserAgent:   envOrDefault("USER_AGENT", DefaultUserAgent),
		LogLevel:    envOrDefault("LOG_LEVEL", DefaultLogLevel),
		ServerAddr:  envOrDefault("SERVER_ADDR", DefaultServerAddr),
		HTTPTimeout: DefaultHTTPTimeout,
		SearxngURL:  strings.TrimSuffix(envOrDefault("SEARXNG_URL", ""), "/"),
	}

	// design: Run lifecycle step 1 - a bad value exits here with no DB trace.
	if raw := envOrDefault("HTTP_TIMEOUT", ""); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("HTTP_TIMEOUT %q is not a Go duration: %w", raw, err)
		}
		if d <= 0 {
			return Config{}, fmt.Errorf("HTTP_TIMEOUT %q must be greater than zero", raw)
		}
		cfg.HTTPTimeout = d
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	if c.DBPath == "" {
		return fmt.Errorf("DB_PATH must not be empty")
	}
	if c.DataDir == "" {
		return fmt.Errorf("DATA_DIR must not be empty")
	}
	if c.Endpoint == "" {
		return fmt.Errorf("REMOTEOK_ENDPOINT must not be empty")
	}
	if err := validateHTTPURL("REMOTEOK_ENDPOINT", c.Endpoint); err != nil {
		return err
	}
	// SEARXNG_URL is optional; only a value that is present must be well formed.
	// A malformed one is still fatal here, at startup, with no DB trace.
	if c.SearxngURL != "" {
		if err := validateHTTPURL("SEARXNG_URL", c.SearxngURL); err != nil {
			return err
		}
	}
	if c.UserAgent == "" {
		return fmt.Errorf("USER_AGENT must not be empty")
	}
	if c.ServerAddr == "" {
		return fmt.Errorf("SERVER_ADDR must not be empty")
	}
	if c.HTTPTimeout <= 0 {
		return fmt.Errorf("HTTP_TIMEOUT must be greater than zero")
	}
	return nil
}

// envOrDefault returns the environment value when set and non-empty, else def.
func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// validateHTTPURL checks that raw is an absolute http or https URL with a host.
// name is the environment variable, and appears in every message so an operator
// can tell which setting is wrong.
func validateHTTPURL(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s %q is not a valid URL: %w", name, raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s %q must be an absolute http or https URL, got scheme %q", name, raw, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("%s %q has no host", name, raw)
	}
	return nil
}
