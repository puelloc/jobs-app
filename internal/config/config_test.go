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
	for _, k := range []string{"DB_PATH", "DATA_DIR", "REMOTEOK_ENDPOINT", "USER_AGENT", "HTTP_TIMEOUT", "LOG_LEVEL"} {
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
