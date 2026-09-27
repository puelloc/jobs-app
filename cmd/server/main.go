// Command server serves the read-only job viewer API.
//
// It is the HTTP half of docs/ui-design.md: GET /api/jobs and
// GET /api/jobs/{id} over the SQLite database the scraper writes. There is no
// request logging, no metrics, no auth, and no write path - the process only
// reads. Shutdown is the standard signal pattern and nothing more.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"jobsapp/internal/api"
	"jobsapp/internal/config"
	"jobsapp/internal/db"
	"jobsapp/internal/store"
)

// shutdownTimeout bounds how long in-flight requests may finish after a signal.
const shutdownTimeout = 5 * time.Second

// teeServerLog appends the standard logger's output to <dataDir>/logs/server.log, in addition to
// stderr, so the UI's GET /api/logs/server can show the server's own runtime errors. The file is
// held open for the life of the process and closed on exit.
func teeServerLog(dataDir string) error {
	dir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "server.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	log.SetOutput(io.MultiWriter(os.Stderr, f))
	return nil
}

func main() {
	os.Exit(run())
}

func run() int {
	// design: config.Load performs no I/O; a bad value fails before the
	// database is touched.
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "server: config: %v\n", err)
		return 1
	}

	// Tee the standard logger (used by the api package to log internal errors) into a file so
	// GET /api/logs/server can surface the server's own runtime errors in the UI.
	if err := teeServerLog(cfg.DataDir); err != nil {
		fmt.Fprintf(os.Stderr, "server: could not open log file: %v\n", err)
	}

	// db.Open applies the DSN pragmas and runs any pending migrations, so a
	// fresh checkout serves an empty list rather than failing on a missing
	// schema.
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "server: open database %s: %v\n", cfg.DBPath, err)
		return 1
	}
	defer database.Close()

	// A fresh server process means any run still marked 'running' was killed with the previous
	// process (its reaper never ran), so close those rows out rather than leaving them stuck.
	if n, err := store.ReconcileStaleRuns(context.Background(), database); err != nil {
		fmt.Fprintf(os.Stderr, "server: reconcile stale runs: %v\n", err)
	} else if n > 0 {
		fmt.Fprintf(os.Stderr, "server: marked %d stale running run(s) as interrupted by restart\n", n)
	}

	scrapeCmd := strings.Fields(cfg.ScrapeCommand)

	srv := &http.Server{
		Addr:              cfg.ServerAddr,
		Handler:           api.NewRouter(database, cfg.DataDir, scrapeCmd),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Shutdown runs off the main goroutine. ListenAndServe returns
	// http.ErrServerClosed once Shutdown completes, which run() treats as the
	// clean exit path.
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "server: shutdown: %v\n", err)
		}
	}()

	fmt.Fprintf(os.Stderr, "server: listening on http://%s\n", cfg.ServerAddr)

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "server: listen on %s: %v\n", cfg.ServerAddr, err)
		return 1
	}
	return 0
}
