// config_test.go: tests for environment parsing and validation.
package config

import (
	"strings"
	"testing"
	"time"
)

// cleanEnv clears every variable Load reads, so a developer's shell cannot
// change the outcome.
func cleanEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"DB_PATH", "DATA_DIR", "REMOTEOK_ENDPOINT", "USER_AGENT", "HTTP_TIMEOUT", "LOG_LEVEL", "SEARXNG_URL",
		"ENABLE_BROWSER_USE", "BROWSER_WORKER_COMMAND", "BROWSER_USE_MODEL", "OLLAMA_HOST"} {
		t.Setenv(k, "")
	}
}

func TestLoadAppliesDocumentedDefaults(t *testing.T) {
	cleanEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, tc := range []struct{ name, got, want string }{
		{"DBPath", cfg.DBPath, DefaultDBPath},
		{"DataDir", cfg.DataDir, DefaultDataDir},
		{"Endpoint", cfg.Endpoint, DefaultEndpoint},
		{"UserAgent", cfg.UserAgent, DefaultUserAgent},
		{"LogLevel", cfg.LogLevel, DefaultLogLevel},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
	if cfg.HTTPTimeout != DefaultHTTPTimeout {
		t.Errorf("HTTPTimeout = %s, want %s", cfg.HTTPTimeout, DefaultHTTPTimeout)
	}
}

func TestDefaultEndpointIsTheJSONAPI(t *testing.T) {
	// Regression guard: the HTML listing page was the original default and made
	// the parse step fail on every run.
	if DefaultEndpoint != "https://remoteok.com/api" {
		t.Errorf("DefaultEndpoint = %q, want https://remoteok.com/api", DefaultEndpoint)
	}
}

func TestLoadOverridesFromEnvironment(t *testing.T) {
	cleanEnv(t)
	t.Setenv("DB_PATH", "/tmp/other.db")
	t.Setenv("DATA_DIR", "/tmp/other-data")
	t.Setenv("REMOTEOK_ENDPOINT", "https://example.test/feed")
	t.Setenv("USER_AGENT", "custom-agent/9")
	t.Setenv("HTTP_TIMEOUT", "7s")
	t.Setenv("LOG_LEVEL", "debug")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DBPath != "/tmp/other.db" {
		t.Errorf("DBPath = %q", cfg.DBPath)
	}
	if cfg.DataDir != "/tmp/other-data" {
		t.Errorf("DataDir = %q", cfg.DataDir)
	}
	if cfg.Endpoint != "https://example.test/feed" {
		t.Errorf("Endpoint = %q", cfg.Endpoint)
	}
	if cfg.UserAgent != "custom-agent/9" {
		t.Errorf("UserAgent = %q", cfg.UserAgent)
	}
	if cfg.HTTPTimeout != 7*time.Second {
		t.Errorf("HTTPTimeout = %s, want 7s", cfg.HTTPTimeout)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q", cfg.LogLevel)
	}
}

// design: Inputs - "An empty string is treated as unset."
func TestLoadTreatsEmptyStringAsUnset(t *testing.T) {
	cleanEnv(t)
	t.Setenv("DB_PATH", "")
	t.Setenv("HTTP_TIMEOUT", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DBPath != DefaultDBPath {
		t.Errorf("DBPath = %q, want the default when the var is empty", cfg.DBPath)
	}
	if cfg.HTTPTimeout != DefaultHTTPTimeout {
		t.Errorf("HTTPTimeout = %s, want the default when the var is empty", cfg.HTTPTimeout)
	}
}

// design: Run lifecycle step 1 - a bad value exits here with no DB trace.
func TestLoadRejectsBadHTTPTimeout(t *testing.T) {
	for _, bad := range []string{"abc", "30", "5 seconds", "-5s", "0s", "0"} {
		t.Run(bad, func(t *testing.T) {
			cleanEnv(t)
			t.Setenv("HTTP_TIMEOUT", bad)
			if _, err := Load(); err == nil {
				t.Errorf("Load accepted HTTP_TIMEOUT=%q, want an error", bad)
			}
		})
	}
}

func TestLoadAcceptsValidHTTPTimeouts(t *testing.T) {
	for _, good := range []string{"1s", "500ms", "2m", "1h"} {
		t.Run(good, func(t *testing.T) {
			cleanEnv(t)
			t.Setenv("HTTP_TIMEOUT", good)
			if _, err := Load(); err != nil {
				t.Errorf("Load rejected HTTP_TIMEOUT=%q: %v", good, err)
			}
		})
	}
}

func TestLoadRejectsBadEndpoint(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"relative", "/feed.json"},
		{"no host", "https://"},
		{"unsupported scheme", "ftp://example.test/feed"},
		{"scheme only", "http://"},
		{"garbage", "://nope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cleanEnv(t)
			t.Setenv("REMOTEOK_ENDPOINT", tc.value)
			if _, err := Load(); err == nil {
				t.Errorf("Load accepted REMOTEOK_ENDPOINT=%q, want an error", tc.value)
			}
		})
	}
}

func TestLoadAcceptsValidEndpoints(t *testing.T) {
	for _, good := range []string{
		"https://remoteok.com/api",
		"http://127.0.0.1:8080/feed.json",
		"https://example.test/feed?x=1",
	} {
		t.Run(good, func(t *testing.T) {
			cleanEnv(t)
			t.Setenv("REMOTEOK_ENDPOINT", good)
			if _, err := Load(); err != nil {
				t.Errorf("Load rejected %q: %v", good, err)
			}
		})
	}
}

func TestLoadErrorMentionsTheVariable(t *testing.T) {
	cleanEnv(t)
	t.Setenv("HTTP_TIMEOUT", "nonsense")

	_, err := Load()
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "HTTP_TIMEOUT") {
		t.Errorf("error = %q, want it to name HTTP_TIMEOUT so an operator can fix it", err)
	}
}

func TestEnvOrDefault(t *testing.T) {
	cleanEnv(t)
	if got := envOrDefault("DB_PATH", "fallback"); got != "fallback" {
		t.Errorf("got %q, want the fallback", got)
	}
	t.Setenv("DB_PATH", "set")
	if got := envOrDefault("DB_PATH", "fallback"); got != "set" {
		t.Errorf("got %q, want the environment value", got)
	}
	t.Setenv("DB_PATH", "")
	if got := envOrDefault("DB_PATH", "fallback"); got != "fallback" {
		t.Errorf("got %q, want the fallback for an empty value", got)
	}
}

// Load must not touch the filesystem: a path that cannot exist must still load.
func TestLoadPerformsNoIO(t *testing.T) {
	cleanEnv(t)
	t.Setenv("DB_PATH", "/definitely/not/a/real/directory/nope.db")
	t.Setenv("DATA_DIR", "/definitely/not/a/real/directory/data")

	if _, err := Load(); err != nil {
		t.Fatalf("Load returned %v, want no I/O and therefore no error", err)
	}
}

// SearXNG is an optional dependency: the searxng resolve phase must be able to
// run in a deployment that has no SearXNG at all, so an unset URL is valid and
// means "disabled" rather than an error.
func TestLoadAllowsMissingSearxngURL(t *testing.T) {
	cleanEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SearxngURL != "" {
		t.Errorf("SearxngURL = %q, want empty when SEARXNG_URL is unset", cfg.SearxngURL)
	}
	if cfg.SearxngEnabled() {
		t.Error("SearxngEnabled() = true with no URL, want false")
	}
}

func TestLoadReadsSearxngURL(t *testing.T) {
	cleanEnv(t)
	t.Setenv("SEARXNG_URL", "https://search.siggy-lab.org")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SearxngURL != "https://search.siggy-lab.org" {
		t.Errorf("SearxngURL = %q", cfg.SearxngURL)
	}
	if !cfg.SearxngEnabled() {
		t.Error("SearxngEnabled() = false with a URL set, want true")
	}
}

// design: Run lifecycle step 1 - a bad value exits here with no DB trace.
func TestLoadRejectsBadSearxngURL(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"relative", "/search"},
		{"no host", "https://"},
		{"unsupported scheme", "ftp://search.test"},
		{"scheme only", "http://"},
		{"garbage", "://nope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cleanEnv(t)
			t.Setenv("SEARXNG_URL", tc.value)
			if _, err := Load(); err == nil {
				t.Errorf("Load accepted SEARXNG_URL=%q, want an error", tc.value)
			}
		})
	}
}

func TestLoadSearxngErrorMentionsTheVariable(t *testing.T) {
	cleanEnv(t)
	t.Setenv("SEARXNG_URL", "ftp://search.test")

	_, err := Load()
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "SEARXNG_URL") {
		t.Errorf("error = %q, want it to name SEARXNG_URL so an operator can fix it", err)
	}
}

// A trailing slash would produce https://host//search, which some reverse
// proxies do not normalise. Load trims it so callers can concatenate safely.
func TestLoadTrimsTrailingSlashFromSearxngURL(t *testing.T) {
	cleanEnv(t)
	t.Setenv("SEARXNG_URL", "https://search.siggy-lab.org/")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SearxngURL != "https://search.siggy-lab.org" {
		t.Errorf("SearxngURL = %q, want the trailing slash trimmed", cfg.SearxngURL)
	}
}

// --- browser-use tier -----------------------------------------------------

func TestBrowserUseIsOffUnlessEnabled(t *testing.T) {
	cleanEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BrowserUseEnabled {
		t.Error("BrowserUseEnabled = true with the variable unset; the tier must be opt-in")
	}
	for _, tc := range []struct{ name, raw string }{
		{"one", "1"}, {"true", "true"}, {"yes", "yes"}, {"on", "ON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cleanEnv(t)
			t.Setenv("ENABLE_BROWSER_USE", tc.raw)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if !cfg.BrowserUseEnabled {
				t.Errorf("ENABLE_BROWSER_USE=%q did not enable the tier", tc.raw)
			}
		})
	}
}

// "ENABLE_BROWSER_USE=0" reads as off to a human. Treating any non-empty value as true would do the
// opposite of what was written, which is the worst possible reading of a switch.
func TestBrowserUseExplicitZeroMeansOff(t *testing.T) {
	cleanEnv(t)
	for _, raw := range []string{"0", "false", "no", "off"} {
		t.Setenv("ENABLE_BROWSER_USE", raw)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.BrowserUseEnabled {
			t.Errorf("ENABLE_BROWSER_USE=%q enabled the tier", raw)
		}
	}
}

func TestBrowserUseRejectsASpellingItDoesNotUnderstand(t *testing.T) {
	cleanEnv(t)
	t.Setenv("ENABLE_BROWSER_USE", "yes-please")

	_, err := Load()
	if err == nil {
		t.Fatal("an unrecognised boolean was accepted")
	}
	if !strings.Contains(err.Error(), "ENABLE_BROWSER_USE") {
		t.Errorf("error = %q, want it to name the variable", err)
	}
}

func TestBrowserUseDefaultsAreUsableWithoutConfiguration(t *testing.T) {
	cleanEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BrowserUseCommand != DefaultBrowserUseCommand {
		t.Errorf("BrowserUseCommand = %q, want %q", cfg.BrowserUseCommand, DefaultBrowserUseCommand)
	}
	if cfg.BrowserUseModel != DefaultBrowserUseModel {
		t.Errorf("BrowserUseModel = %q, want %q", cfg.BrowserUseModel, DefaultBrowserUseModel)
	}
	// OLLAMA_HOST has no default on purpose: the plan records two possible hosts (the NAS itself and
	// the LAN address), and guessing between them would make every escalation fail for a reason the
	// run cannot explain.
	if cfg.OllamaHost != "" {
		t.Errorf("OllamaHost = %q, want empty", cfg.OllamaHost)
	}
}

func TestBrowserUseRejectsAMalformedOllamaHost(t *testing.T) {
	cleanEnv(t)
	t.Setenv("OLLAMA_HOST", "ai.siggy-lab.org")

	_, err := Load()
	if err == nil {
		t.Fatal("a host with no scheme was accepted")
	}
	if !strings.Contains(err.Error(), "OLLAMA_HOST") {
		t.Errorf("error = %q, want it to name OLLAMA_HOST", err)
	}
}

func TestBrowserUseTrimsATrailingSlashFromOllamaHost(t *testing.T) {
	cleanEnv(t)
	t.Setenv("OLLAMA_HOST", "https://ai.siggy-lab.org/")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OllamaHost != "https://ai.siggy-lab.org" {
		t.Errorf("OllamaHost = %q, want the trailing slash trimmed", cfg.OllamaHost)
	}
}
