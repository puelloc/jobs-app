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
	// Strip the "migrations/" prefix so migration names are bare filenames.
	migrations, err := fs.Sub(migrationsFS, migrationsDir)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("db: open migrations dir: %w", err)
	}
	if err := migrate(db, migrations); err != nil {
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

// migrate applies every migration in migrations that is not yet recorded in schema_migrations.
//
// Ordering is lexical by filename (001_..., 002_..., 010_...), so zero-pad the numeric prefix.
// Each migration runs in its own transaction together with the INSERT that records its version,
// so a failing migration leaves neither partial schema nor a version row.
//
// Everything here runs on one dedicated connection. That is not incidental: some migrations must
// run PRAGMA foreign_keys=OFF, which is a per-connection setting, and the pragma and the
// transaction that depends on it have to land on the same physical connection. Reading it off the
// pool would only work while SetMaxOpenConns(1); raising the pool size would make the pragma and
// the transaction land on different connections, and the migration would then fail with an opaque
// foreign-key error instead. Taking the connection explicitly keeps that true by construction.
func migrate(db *sql.DB, migrations fs.FS) error {
	ctx := context.Background()

	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("db: acquire migration connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	const schemaMigrationsDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    TEXT PRIMARY KEY,
    applied_at TEXT NOT NULL
)`
	if _, err := conn.ExecContext(ctx, schemaMigrationsDDL); err != nil {
		return fmt.Errorf("db: create schema_migrations: %w", err)
	}

	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return err
	}

	files, err := fs.Glob(migrations, "*.sql")
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
		sqlBytes, err := fs.ReadFile(migrations, name)
		if err != nil {
			return fmt.Errorf("db: read migration %s: %w", name, err)
		}
		if err := applyMigration(ctx, conn, version, string(sqlBytes)); err != nil {
			return fmt.Errorf("db: migration %s failed: %w", name, err)
		}
	}
	return nil
}

// directivePrefix introduces a runner directive in a migration's leading comment block.
const directivePrefix = "migrate:"

// migrationDirectives holds the runner directives parsed out of a migration's header.
type migrationDirectives struct {
	// foreignKeysOff asks the runner to disable foreign-key enforcement ("-- migrate:fk_off").
	// Needed to rebuild a parent table: dropping it while children reference it fails with
	// "FOREIGN KEY constraint failed" under enforcement.
	foreignKeysOff bool
}

// parseDirectives reads the migration's leading comment block. Only the first line that is not
// blank and not a comment ends the header, so directives must sit above the first statement.
//
// An unrecognised "-- migrate:..." directive is an error rather than being ignored as an ordinary
// comment. A typo such as "-- migrate:fk-off" would otherwise run with foreign keys still enabled
// and fail later with an opaque constraint error, giving the author no signal about the cause.
func parseDirectives(body string) (migrationDirectives, error) {
	var d migrationDirectives
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "--") {
			break
		}
		comment := strings.TrimSpace(strings.TrimPrefix(trimmed, "--"))
		// Accept "--migrate:x" as well as "-- migrate: x".
		rest, found := strings.CutPrefix(comment, directivePrefix)
		if !found {
			continue
		}
		switch name := strings.TrimSpace(rest); name {
		case "fk_off":
			d.foreignKeysOff = true
		default:
			return migrationDirectives{}, fmt.Errorf("unknown directive %q (known directives: fk_off)", directivePrefix+name)
		}
	}
	return d, nil
}

// withForeignKeysOff turns enforcement off for the duration of fn and guarantees it is turned back
// on, including when fn returns an error.
//
// The pragma runs outside any transaction: SQLite's documented procedure for schema changes puts
// it there, and a connection left with enforcement disabled would silently accept foreign-key
// violations for the rest of the process's life. Because db.Open caps the pool at one connection,
// that connection is the only one there is.
func withForeignKeysOff(ctx context.Context, conn *sql.Conn, fn func() error) (err error) {
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return fmt.Errorf("disable foreign_keys: %w", err)
	}
	defer func() {
		if _, rerr := conn.ExecContext(ctx, "PRAGMA foreign_keys=ON"); rerr != nil {
			if err == nil {
				err = fmt.Errorf("restore foreign_keys: %w", rerr)
			}
		}
	}()
	return fn()
}

// checkForeignKeys runs PRAGMA foreign_key_check and reports the first violation. It runs after
// every migration, not just the ones that disable enforcement: enforcement is off during a rebuild,
// so a violation introduced there is not caught by the statements themselves.
func checkForeignKeys(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("foreign_key_check: %w", err)
	}
	defer rows.Close()

	// Columns: table, rowid, parent, fkid. Any row at all is a violation.
	if rows.Next() {
		var table, parent string
		var rowid sql.NullInt64
		var fkid sql.NullInt64
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return fmt.Errorf("foreign_key_check: scan: %w", err)
		}
		return fmt.Errorf("foreign key violation: table %s references %s", table, parent)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("foreign_key_check: %w", err)
	}
	return nil
}

// versionOf extracts the migration version from a path such as
// "001_init.sql" -> "001". The numeric prefix is the primary key in
// schema_migrations, so renaming an already-applied file would re-run it.
func versionOf(name string) (string, error) {
	base := strings.TrimSuffix(path.Base(name), ".sql")
	version, _, found := strings.Cut(base, "_")
	if !found || version == "" {
		return "", fmt.Errorf("db: migration %s: name must look like NNN_short_description.sql", name)
	}
	return version, nil
}

func appliedVersions(ctx context.Context, conn *sql.Conn) (map[string]struct{}, error) {
	rows, err := conn.QueryContext(ctx, `SELECT version FROM schema_migrations`)
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

// applyMigration runs one migration on conn, in a single transaction together with the row that
// records its version, followed by a foreign-key check on the same connection.
//
// The foreign-key check runs only when the migration committed. Running it after a failure would
// report a violation left by an aborted transaction, or mask the actual failure with an unrelated
// one.
func applyMigration(ctx context.Context, conn *sql.Conn, version, body string) (err error) {
	directives, err := parseDirectives(body)
	if err != nil {
		return err
	}

	// Defined before the defer below so the deferred call sees its effect. Note the deferred func
	// closes over this same err: an `err :=` inside an inner function would shadow it, and the
	// rollback would then fire on success and discard the migration.
	var run func() error
	defer func() {
		if err == nil {
			err = checkForeignKeys(ctx, conn)
		}
	}()

	run = func() error {
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin: %w", err)
		}
		defer func() {
			if err != nil {
				_ = tx.Rollback()
			}
		}()

		if _, err = tx.ExecContext(ctx, body); err != nil {
			return fmt.Errorf("exec: %w", err)
		}
		if _, err = tx.ExecContext(
			ctx,
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

	if directives.foreignKeysOff {
		return withForeignKeysOff(ctx, conn, run)
	}
	return run()
}

// ErrNoMigrations is returned by Open when the embedded FS contains no *.sql files, which
// almost always means the //go:embed pattern in embed.go stopped matching.
var ErrNoMigrations = errors.New("db: no embedded migrations found")
