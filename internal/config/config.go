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

	// DefaultBrowserUseCommand is the browser-use worker invocation, whitespace-split and run
	// without a shell. It is a command rather than a python-path plus a script-path so a test can
	// substitute a fake worker in one variable, which is the only way the command's own wiring is
	// exercised without a browser.
	DefaultBrowserUseCommand = ".venv-browser/bin/python worker/browser_worker.py"

	// DefaultBrowserUseModel is the model the escalation tier asks Ollama for. The plan's M3
	// section selects this tag: it has vision and tool-calling, and 64k of context.
	DefaultBrowserUseModel = "qwen3.8-27b-64k:latest"
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

	// BrowserUseEnabled gates the browser-use tier. It is off unless ENABLE_BROWSER_USE is set to a
	// true value, so a browser-driven run is always a deliberate act: the tier starts a Python
	// process and a Chromium, which nothing else in this tree does.
	BrowserUseEnabled bool
	// BrowserUseCommand is the worker invocation, whitespace-split, run without a shell.
	BrowserUseCommand string
	// BrowserUseModel is the Ollama model tag for the escalation tier.
	BrowserUseModel string
	// OllamaHost is the Ollama base URL the worker talks to. Empty means the worker's own default,
	// which is why a run that needs escalation requires it to be set: a silently wrong default would
	// make every escalation fail for a reason nothing in the run explains.
	OllamaHost string
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

		BrowserUseCommand: envOrDefault("BROWSER_WORKER_COMMAND", DefaultBrowserUseCommand),
		BrowserUseModel:   envOrDefault("BROWSER_USE_MODEL", DefaultBrowserUseModel),
		OllamaHost:        strings.TrimSuffix(envOrDefault("OLLAMA_HOST", ""), "/"),
	}

	// ENABLE_BROWSER_USE is parsed rather than merely tested for emptiness: "ENABLE_BROWSER_USE=0"
	// reads as "off" to a human, and treating any non-empty value as true would quietly do the
	// opposite of what was written.
	if raw := envOrDefault("ENABLE_BROWSER_USE", ""); raw != "" {
		enabled, err := parseBool("ENABLE_BROWSER_USE", raw)
		if err != nil {
			return Config{}, err
		}
		cfg.BrowserUseEnabled = enabled
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
	if c.BrowserUseCommand == "" {
		return fmt.Errorf("BROWSER_WORKER_COMMAND must not be empty")
	}
	// OLLAMA_HOST is optional; only a value that is present must be well formed.
	if c.OllamaHost != "" {
		if err := validateHTTPURL("OLLAMA_HOST", c.OllamaHost); err != nil {
			return err
		}
	}
	return nil
}

// parseBool accepts the spellings an operator actually writes for a feature flag, and rejects
// anything else rather than guessing. A typo like ENABLE_BROWSER_USE=yes-please must not be read as
// either true or false silently.
func parseBool(name, raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%s %q is not a boolean; use 1/0, true/false, yes/no or on/off", name, raw)
	}
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
