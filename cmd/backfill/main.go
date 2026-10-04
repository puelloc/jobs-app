// Command backfill reads the schema.org JobPosting JSON already stored in job_listings.raw_data and
// fills the columns that were left NULL when the row was captured.
//
// The listings scrape has always stored each posting's embedded JSON verbatim, but nothing read it:
// posted_at and employment_type stayed NULL even when the board had said, and the data was sitting in
// the row the whole time. The parse now happens at store time (cmd/scrape), so this exists for the rows
// written before it did.
//
// Dry-run by default - a write to the live database is an explicit act - and idempotent, so re-running
// it after a successful pass finds nothing left to change.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"jobsapp/internal/config"
	"jobsapp/internal/db"
	"jobsapp/internal/jobposting"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// pending is one row whose stored columns disagree with what its raw_data says. An empty
// employmentType or nil postedAt means "this run has nothing to say about that column".
type pending struct {
	id             int64
	employmentType string
	postedAt       *time.Time
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("backfill", flag.ContinueOnError)
	fs.SetOutput(stderr)
	commit := fs.Bool("commit", false, "actually write (default is a read-only dry run)")
	limit := fs.Int("limit", 0, "cap the rows scanned (0 means all)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "backfill: config: %v\n", err)
		return 1
	}
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(stderr, "backfill: open database %s: %v\n", cfg.DBPath, err)
		return 1
	}
	defer func() { _ = database.Close() }()

	ctx := context.Background()
	scanned, parsed, remoteDeclared, todo, err := scan(ctx, database, *limit)
	if err != nil {
		fmt.Fprintf(stderr, "backfill: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "scanned=%d jobpostings=%d remote_declared=%d update=%d commit=%t\n",
		scanned, parsed, remoteDeclared, len(todo), *commit)

	// remote_declared is the measurement, not a write: it is how many rows state their own remote
	// status in structured data, which is what would decide whether that field can ever replace the
	// agent's per-company verdict. It is reported even in a dry run.

	if !*commit {
		for _, p := range todo {
			fmt.Fprintf(stdout, "  id=%d employment_type=%q posted_at=%s\n",
				p.id, p.employmentType, formatInstant(p.postedAt))
		}
		fmt.Fprintln(stdout, "dry run: nothing written (pass -commit to write)")
		return 0
	}

	written := 0
	for _, p := range todo {
		if err := apply(ctx, database, p); err != nil {
			fmt.Fprintf(stderr, "backfill: row %d: %v\n", p.id, err)
			return 1
		}
		written++
	}
	fmt.Fprintf(stdout, "wrote %d rows\n", written)
	return 0
}

// scan walks every row that carries raw_data and works out what its posting says. It reads through
// QueryContext rather than Exec because the store's Querier convention is not in play here - this is a
// one-off maintenance command, and a read-only pass must not need write access.
func scan(ctx context.Context, database *sql.DB, limit int) (scanned, parsed, remoteDeclared int, todo []pending, err error) {
	query := `
SELECT id, COALESCE(raw_data, ''), employment_type, posted_at
  FROM job_listings
 WHERE raw_data IS NOT NULL
 ORDER BY id`
	args := []any{}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := database.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, 0, 0, nil, fmt.Errorf("select rows: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id           int64
			raw          string
			employment   sql.NullString
			postedAtText sql.NullString
		)
		if err := rows.Scan(&id, &raw, &employment, &postedAtText); err != nil {
			return 0, 0, 0, nil, fmt.Errorf("scan row: %w", err)
		}
		scanned++

		posting, ok := jobposting.Parse(raw)
		if !ok {
			continue
		}
		parsed++
		if posting.Remote != nil {
			remoteDeclared++
		}

		var p pending
		p.id = id
		// Only a genuine difference is queued, which is what makes a second pass a no-op.
		if posting.EmploymentType != "" && posting.EmploymentType != employment.String {
			p.employmentType = posting.EmploymentType
		}
		if posting.PostedAt != nil && !sameInstant(postedAtText, posting.PostedAt) {
			p.postedAt = posting.PostedAt
		}
		if p.employmentType != "" || p.postedAt != nil {
			todo = append(todo, p)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, 0, 0, nil, fmt.Errorf("iterate rows: %w", err)
	}
	return scanned, parsed, remoteDeclared, todo, nil
}

// apply writes one row's recovered fields. COALESCE keeps a column the parse had nothing to say about
// exactly as it was.
//
// updated_at moves because the row genuinely changed; last_seen_at does not, because a backfill
// observed nothing - it re-read a capture the scraper had already made, and claiming a fresh sighting
// would corrupt the one honest freshness signal these rows carry.
func apply(ctx context.Context, database *sql.DB, p pending) error {
	var (
		employmentType any
		postedAt       any
	)
	if p.employmentType != "" {
		employmentType = p.employmentType
	}
	if p.postedAt != nil {
		postedAt = p.postedAt.UTC().Format(time.RFC3339)
	}

	var id int64
	return database.QueryRowContext(ctx, `
UPDATE job_listings
   SET employment_type = COALESCE(?, employment_type),
       posted_at       = COALESCE(?, posted_at),
       updated_at      = strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE id = ?
RETURNING id`, employmentType, postedAt, p.id).Scan(&id)
}

// sameInstant reports whether a stored posted_at already equals what the parse read. The stored value
// is RFC3339 (UpsertBrowserJob writes it that way); anything unparseable counts as different, so a
// malformed value gets repaired rather than preserved.
func sameInstant(stored sql.NullString, want *time.Time) bool {
	if !stored.Valid {
		return false
	}
	got, err := time.Parse(time.RFC3339, stored.String)
	if err != nil {
		return false
	}
	return got.Equal(*want)
}

func formatInstant(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return strconv.Quote(t.UTC().Format(time.RFC3339))
}
