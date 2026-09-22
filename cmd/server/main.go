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
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"jobsapp/internal/api"
	"jobsapp/internal/config"
	"jobsapp/internal/db"
)

// shutdownTimeout bounds how long in-flight requests may finish after a signal.
const shutdownTimeout = 5 * time.Second

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

	// db.Open applies the DSN pragmas and runs any pending migrations, so a
	// fresh checkout serves an empty list rather than failing on a missing
	// schema.
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "server: open database %s: %v\n", cfg.DBPath, err)
		return 1
	}
	defer database.Close()

	srv := &http.Server{
		Addr:              cfg.ServerAddr,
		Handler:           api.NewRouter(database),
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
