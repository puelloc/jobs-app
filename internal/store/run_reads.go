// run_reads.go: the read-only query behind GET /api/runs.
//
// Nothing here writes: the viewer's whole contract with the database is SELECT.
// Column semantics come from the scrape_runs writers in runs.go and the design
// notes there - a run writes its row before touching the network, so a stuck
// 'running' row with a NULL finished_at is a crashed or abandoned run.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// RunRow mirrors one scrape_runs row joined to its platform name. Nullable
// columns use sql.Null* so the API layer - not this package - decides how to
// present "not known". Timestamps stay strings: SQLite stores them as TEXT and
// parsing is the API layer's job, matching JobRow.
type RunRow struct {
	ID                int64
	Platform          string
	StartedAt         string
	FinishedAt        sql.NullString
	Status            string
	ItemsFound        int64
	ItemsInserted     int64
	ItemsUpdated      int64
	ItemsWrong        int64
	ItemsUnverifiable int64
	DryRun            int64
	ErrorText         sql.NullString
}

// runSelect is the one projection the runs read uses. platform_id is NOT NULL
// with foreign_keys ON, so the JOIN always resolves; it exists to turn the
// numeric platform id into the stable name the UI renders.
const runSelect = `
SELECT
    r.id,
    p.name AS platform,
    r.started_at,
    r.finished_at,
    r.status,
    r.items_found,
    r.items_inserted,
    r.items_updated,
    r.items_wrong,
    r.items_unverifiable,
    r.dry_run,
    r.error_text
FROM scrape_runs r
JOIN platforms p ON p.id = r.platform_id`

func scanRun(s rowScanner) (RunRow, error) {
	var r RunRow
	err := s.Scan(
		&r.ID,
		&r.Platform,
		&r.StartedAt,
		&r.FinishedAt,
		&r.Status,
		&r.ItemsFound,
		&r.ItemsInserted,
		&r.ItemsUpdated,
		&r.ItemsWrong,
		&r.ItemsUnverifiable,
		&r.DryRun,
		&r.ErrorText,
	)
	if err != nil {
		return RunRow{}, err
	}
	return r, nil
}

// GetRun returns one run by id, or sql.ErrNoRows when no such run exists.
func GetRun(ctx context.Context, q Querier, id int64) (RunRow, error) {
	row := q.QueryRowContext(ctx, runSelect+` WHERE r.id = ?`, id)
	return scanRun(row)
}

// ListRuns returns one page of runs, newest first, plus the total number of
// matching rows. search is an optional, case-insensitive substring matched
// against the platform name and the status, so an operator can find the batch
// sweeps ("batch") or every failed run ("error") without paging through history.
// The count and the page are two separate reads rather than one transaction, for
// the same reason ListJobs does it that way: the viewer is read-only, and a torn
// count at worst makes "load more" flicker.
//
// limit and offset are expected to be validated by the caller; they are bound
// as parameters, never interpolated.
func ListRuns(ctx context.Context, q Querier, limit, offset int, search string) ([]RunRow, int64, error) {
	where := ""
	args := []any{}
	if s := strings.TrimSpace(search); s != "" {
		// instr + lower is a literal, case-insensitive substring test: unlike LIKE it treats
		// "_", "%" and "\" in the search text as ordinary characters, so "career_batch" matches
		// exactly the platform name rather than a wildcard pattern.
		where = ` WHERE instr(lower(p.name), lower(?)) > 0 OR instr(lower(r.status), lower(?)) > 0`
		args = append(args, s, s)
	}

	var total int64
	if err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM scrape_runs r JOIN platforms p ON p.id = r.platform_id`+where,
		args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("list runs: count: %w", err)
	}

	pageArgs := append(append([]any{}, args...), limit, offset)
	rows, err := q.QueryContext(ctx, runSelect+where+`
ORDER BY r.started_at DESC, r.id DESC
LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list runs: select: %w", err)
	}
	defer rows.Close()

	// Non-nil even when empty so the JSON envelope carries [] rather than null.
	runs := make([]RunRow, 0, limit)
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("list runs: scan: %w", err)
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list runs: iterate: %w", err)
	}
	return runs, total, nil
}
