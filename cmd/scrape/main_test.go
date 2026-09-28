package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

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
