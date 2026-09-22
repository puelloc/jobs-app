# Migrations

Schema migrations for the SQLite database. Runner lives in `db.go`; files live in
`migrations/` and are compiled into the binary by `embed.go`.

## Naming convention

```
NNN_short_description.sql      e.g. 001_init.sql, 002_seed_platforms.sql, 010_add_salary_currency.sql
```

- The numeric prefix is zero-padded and is the migration **version** (`001`, `002`, ... `010`).
  It is the primary key in `schema_migrations`, so it must be unique.
- Files are applied in **lexical filename order**. Zero-padding is what keeps that order
  correct past 9: `010` sorts after `009`, while unpadded `10` would sort before `002`.
- The version is everything before the first `_`; the rest of the filename is documentation.
- Only `migrations/*.sql` is embedded.

## Immutability

**A migration file is immutable once it has been applied anywhere** (your machine, the NAS,
a teammate's box). The runner keys off filenames: it has no checksums, so editing or renaming
an applied file does **not** re-run it, and the databases silently diverge. To change the
schema, add a new migration that does `ALTER TABLE` / `CREATE TABLE` / `CREATE INDEX`.

The one exception is `002_seed_platforms.sql`-style seed data, which is written with
`INSERT OR IGNORE` so re-running is harmless — seeding is still only executed once per
database, but it is safe to apply to a database that already has rows.

## How the runner works

1. `embed.go` publishes `migrations/*.sql` as an `embed.FS`. Nothing is read from disk at
   runtime, so the deployed binary is self-contained.
2. `Open(path)` builds the DSN with the pragmas (`journal_mode(WAL)`, `busy_timeout(5000)`,
   `foreign_keys(ON)`, `synchronous(NORMAL)`), then calls `migrate`.
3. `migrate` creates the tracking table if missing:

   ```sql
   CREATE TABLE IF NOT EXISTS schema_migrations (
       version    TEXT PRIMARY KEY,
       applied_at TEXT NOT NULL      -- RFC3339 UTC
   );
   ```

4. It globs `migrations/*.sql`, sorts the names, and for each file whose version is not in
   `schema_migrations` runs the file body and inserts the version row **in a single
   transaction**. A failure rolls back both the DDL and the version row, and `Open` returns an
   error naming the failing file, e.g.:

   ```
   db: migration migrations/003_add_fts.sql failed: exec: SQL logic error: ...
   ```

5. Already-applied versions are skipped, so `Open` is safe to call on every process start.

Notes:

- `schema_migrations` is created outside the per-file transactions (it needs to exist before
  the first migration is recorded).
- Because everything runs under `db.SetMaxOpenConns(1)`, migrations cannot race another
  connection in the same process. Two processes starting against the same file at once are
  still serialized by the DDL's write lock plus `busy_timeout`; the losing process should fail
  fast, and the next start will find the migrations already applied.
- SQLite DDL is transactional, so a failed `ALTER TABLE`/`CREATE INDEX` leaves no partial schema.

## Adding migration 003

1. Create `internal/db/migrations/003_short_description.sql`.
2. Write the SQL. Conventions used throughout the schema:
   - timestamps: `TEXT`, RFC3339 UTC, `DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))`
   - booleans: `INTEGER` with `CHECK (col IN (0,1))`
   - JSON: `TEXT` with `CHECK (col IS NULL OR json_valid(col))`
   - money: `INTEGER` cents
   - enums: `TEXT` with an explicit `CHECK` listing the allowed values
   - `updated_at` is written by application code — do **not** add triggers
3. Do not add the file to a list anywhere: `//go:embed migrations/*.sql` picks it up.
4. Rebuild and run the app (or `go test ./internal/db/`). Existing databases get `003`
   applied on the next `Open`; fresh databases get `001`, `002`, `003` in order.

Check what a given database has applied:

```sh
sqlite3 jobs.db 'SELECT version, applied_at FROM schema_migrations ORDER BY version;'
```

## If you need full-text search later (not now)

`description` is plain `TEXT`; there is no FTS5 table. When description search is actually
needed, add something like `00N_fts_job_listings.sql` containing an FTS5 virtual table:

```sql
CREATE VIRTUAL TABLE job_listings_fts USING fts5(title, description, content='job_listings', content_rowid='id');
```

Deliberately deferred, because it changes the build story:

- FTS5 must be compiled into the SQLite library. With `modernc.org/sqlite` it is enabled in
  the default pure-Go build, but on platforms/runtime combinations where it is not, enabling
  it requires a build tag (`-tags sqlite_fts5` with `mattn/go-sqlite3`), so the whole project's
  build command changes and cross-compilation must be re-verified for each NAS target.
- The external-content table above needs its own sync: either triggers on `job_listings`
  (which conflicts with the "no triggers for `updated_at`" preference only in spirit — those
  would be separate, dedicated triggers) or an explicit rebuild step after each scrape.
- Migration ordering still holds: an FTS migration is just another numbered file, applied in
  lexical order like any other. It must be added as its own file; never edit `001_init.sql`.
