// db.go: opens the SQLite database and applies embedded migrations.
//
// Driver choice: modernc.org/sqlite (pure Go, no cgo). A NAS build must cross-compile to
// linux/amd64, linux/arm64 (Raspberry Pi) and darwin/arm64 from one machine without a C
// toolchain; mattn/go-sqlite3 needs cgo + a target C compiler, modernc does not. It also
// accepts the _pragma DSN parameters used below.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

const migrationsDir = "migrations"

// dbDSN builds the modernc.org/sqlite DSN. Every _pragma entry is applied by the driver to
// every new connection, which matters because foreign_keys and busy_timeout are per-connection
// settings (unlike journal_mode, which is persisted in the database file).
func dbDSN(dbPath string) string {
	pragmas := []string{
		"_pragma=journal_mode(WAL)",   // readers never block the writer
		"_pragma=busy_timeout(5000)",  // wait 5s instead of failing with SQLITE_BUSY
		"_pragma=foreign_keys(ON)",    // FKs are per-connection; declared in SQL, enabled here
		"_pragma=synchronous(NORMAL)", // safe with WAL, much faster than FULL
	}
	return "file:" + dbPath + "?" + strings.Join(pragmas, "&")
}

// Open opens (creating if needed) the SQLite database at path, applies the DSN pragmas to
// every connection, and runs all not-yet-applied embedded migrations in lexical order.
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbDSN(path))
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}

	// Single connection for the whole pool.
	//
	// Tradeoff: a NAS app has exactly one writer (the scraper), so MaxOpenConns(1) makes all
	// writes serial and removes SQLITE_BUSY between this process's own connections entirely.
	// The cost is that reads are serialized behind writes; with WAL that is usually negligible
	// (WAL readers do not block the writer, and statements here are tiny). Raising this limit
	// would allow concurrent reads, but then every extra connection must still carry the DSN
	// pragmas above or it would run with foreign_keys=OFF.
	//
	// If read concurrency ever becomes the bottleneck, keep this pool as the writer and open a
	// second read-only pool (mode=ro) with the same pragmas: one writer + N readers.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := ping(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("db: ping %s: %w", path, err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func ping(db *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return db.PingContext(ctx)
}

// migrate applies every embedded migration that is not yet recorded in schema_migrations.
//
// Ordering is lexical by filename (001_..., 002_..., 010_...), so zero-pad the numeric prefix.
// Each migration runs in its own transaction together with the INSERT that records its version,
// so a failing migration leaves neither partial schema nor a version row.
func migrate(db *sql.DB) error {
	const schemaMigrationsDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    TEXT PRIMARY KEY,
    applied_at TEXT NOT NULL
)`
	if _, err := db.Exec(schemaMigrationsDDL); err != nil {
		return fmt.Errorf("db: create schema_migrations: %w", err)
	}

	applied, err := appliedVersions(db)
	if err != nil {
		return err
	}

	files, err := fs.Glob(migrationsFS, path.Join(migrationsDir, "*.sql"))
	if err != nil {
		return fmt.Errorf("db: list embedded migrations: %w", err)
	}
	if len(files) == 0 {
		// Almost always means the //go:embed pattern in embed.go stopped matching.
		return ErrNoMigrations
	}
	sort.Strings(files)

	for _, name := range files {
		version, err := versionOf(name)
		if err != nil {
			return err
		}
		if _, done := applied[version]; done {
			continue
		}
		sqlBytes, err := migrationsFS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("db: read migration %s: %w", name, err)
		}
		if err := applyMigration(db, version, string(sqlBytes)); err != nil {
			return fmt.Errorf("db: migration %s failed: %w", name, err)
		}
	}
	return nil
}

// versionOf extracts the migration version from an embedded path such as
// "migrations/001_init.sql" -> "001". The numeric prefix is the primary key in
// schema_migrations, so renaming an already-applied file would re-run it.
func versionOf(name string) (string, error) {
	base := strings.TrimSuffix(path.Base(name), ".sql")
	version, _, found := strings.Cut(base, "_")
	if !found || version == "" {
		return "", fmt.Errorf("db: migration %s: name must look like NNN_short_description.sql", name)
	}
	return version, nil
}

func appliedVersions(db *sql.DB) (map[string]struct{}, error) {
	rows, err := db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("db: read schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[string]struct{})
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("db: scan schema_migrations: %w", err)
		}
		applied[version] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate schema_migrations: %w", err)
	}
	return applied, nil
}

func applyMigration(db *sql.DB, version, body string) (err error) {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.Exec(body); err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	if _, err = tx.Exec(
		`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		// Same shape the DDL default produces (millisecond precision, Z suffix).
		version, time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	); err != nil {
		return fmt.Errorf("record version: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// ErrNoMigrations is returned by Open when the embedded FS contains no *.sql files, which
// almost always means the //go:embed pattern in embed.go stopped matching.
var ErrNoMigrations = errors.New("db: no embedded migrations found")
