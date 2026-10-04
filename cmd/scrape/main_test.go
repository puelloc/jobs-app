package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jobsapp/internal/store"
)

func TestReuseCachedListingsURL(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	const ttl = 7 * 24 * time.Hour

	cases := []struct {
		name    string
		company store.ScrapeCompany
		ttl     time.Duration
		refresh bool
		want    string
		wantOK  bool
	}{
		{
			name:    "cold cache is not reused",
			company: store.ScrapeCompany{},
			ttl:     ttl,
			wantOK:  false,
		},
		{
			name: "fresh cache is reused",
			company: store.ScrapeCompany{
				ListingsURL:           "https://jobs.example.test/search?q=engineer",
				ListingsURLResolvedAt: "2026-10-01T12:00:00.000Z",
			},
			ttl:    ttl,
			want:   "https://jobs.example.test/search?q=engineer",
			wantOK: true,
		},
		{
			name: "cache past the TTL is re-resolved",
			company: store.ScrapeCompany{
				ListingsURL:           "https://jobs.example.test/search",
				ListingsURLResolvedAt: "2026-09-01T12:00:00.000Z",
			},
			ttl:    ttl,
			wantOK: false,
		},
		{
			name: "an explicit refresh ignores a fresh cache",
			company: store.ScrapeCompany{
				ListingsURL:           "https://jobs.example.test/search",
				ListingsURLResolvedAt: "2026-10-03T11:59:00.000Z",
			},
			ttl:     ttl,
			refresh: true,
			wantOK:  false,
		},
		{
			name: "ttl zero always re-resolves",
			company: store.ScrapeCompany{
				ListingsURL:           "https://jobs.example.test/search",
				ListingsURLResolvedAt: "2026-10-03T11:59:00.000Z",
			},
			ttl:    0,
			wantOK: false,
		},
		{
			name: "an unparseable timestamp is treated as stale",
			company: store.ScrapeCompany{
				ListingsURL:           "https://jobs.example.test/search",
				ListingsURLResolvedAt: "not-a-timestamp",
			},
			ttl:    ttl,
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := reuseCachedListingsURL(tc.company, now, tc.ttl, tc.refresh)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if got != tc.want {
				t.Errorf("url = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOllamaReachable_SucceedsFirstTry(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.URL.Path != "/api/tags" {
			t.Errorf("path = %q, want /api/tags", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: time.Second}
	if err := ollamaReachable(context.Background(), client, srv.URL); err != nil {
		t.Fatalf("ollamaReachable: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1", got)
	}
}

func TestOllamaReachable_RetriesThreeTimesThenErrors(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: time.Second}
	err := ollamaReachable(context.Background(), client, srv.URL)
	if err == nil {
		t.Fatal("ollamaReachable: want an error for a down host")
	}
	if !strings.Contains(err.Error(), "3 retries") {
		t.Errorf("error %q should mention 3 retries", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("calls = %d, want 3", got)
	}
}

func TestOllamaReachable_ConnectionRefusedRetries(t *testing.T) {
	// A server that accepts then immediately closes: every request fails before a response, which
	// models an unreachable host (connection refused/reset) rather than an HTTP error status.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: time.Second}
	if err := ollamaReachable(context.Background(), client, srv.URL); err == nil {
		t.Fatal("ollamaReachable: want an error when the connection drops")
	}
}
