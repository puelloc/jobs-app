// db_test.go: tests for Open, the migration runner, and the schema constraints
// the scraper's idempotency contract depends on.
package db

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// openTest opens a migrated database in a temp dir.
func openTest(t *testing.T) *sql.DB {
	t.Helper()
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestOpenAppliesEveryEmbeddedMigration(t *testing.T) {
	database := openTest(t)

	files, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		t.Fatalf("glob embedded migrations: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no embedded migrations found")
	}

	var applied int
	if err := database.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if applied != len(files) {
		t.Errorf("schema_migrations has %d rows, want %d (one per embedded file)", applied, len(files))
	}
}

func TestOpenCreatesEveryTable(t *testing.T) {
	database := openTest(t)

	want := []string{
		"companies",
		"company_application_platforms",
		"job_listings",
		"platforms",
		"schema_migrations",
		"scrape_runs",
	}
	rows, err := database.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate tables: %v", err)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("tables = %v, want %v", got, want)
	}
}

func TestOpenSeedsRemoteOKPlatform(t *testing.T) {
	database := openTest(t)

	var name, kind, baseURL string
	if err := database.QueryRow(
		`SELECT name, platform_type, base_url_pattern FROM platforms WHERE id = 1`).
		Scan(&name, &kind, &baseURL); err != nil {
		t.Fatalf("read platform 1: %v", err)
	}
	if name != "remoteok" {
		t.Errorf("name = %q, want remoteok", name)
	}
	if kind != "job_board" {
		t.Errorf("platform_type = %q, want job_board", kind)
	}
	// The JSON feed, not the HTML listing page.
	if baseURL != "https://remoteok.com/api" {
		t.Errorf("base_url_pattern = %q, want https://remoteok.com/api", baseURL)
	}
}

// design: migration runner - "Already-applied versions are skipped."
func TestMigrateIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	var firstApplied int
	if err := first.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&firstApplied); err != nil {
		t.Fatalf("count after first open: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}

	// A second Open on the same file must not re-run anything; re-running
	// 001_init.sql would fail with "table already exists".
	second, err := Open(path)
	if err != nil {
		t.Fatalf("second Open re-ran a migration: %v", err)
	}
	defer second.Close()

	var secondApplied int
	if err := second.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&secondApplied); err != nil {
		t.Fatalf("count after second open: %v", err)
	}
	if secondApplied != firstApplied {
		t.Errorf("schema_migrations grew from %d to %d on a no-op reopen", firstApplied, secondApplied)
	}
}

// design: Failures must name the failing migration file.
func TestOpenErrorNamesTheFileWhenAMigrationCannotApply(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	// Pre-create a table that 001_init.sql also creates, so its first statement
	// fails. The seed rows are absent from schema_migrations, so the runner will
	// try to apply 001 against this database.
	pre, err := sql.Open("sqlite", dbDSN(path))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := pre.Exec(`CREATE TABLE platforms (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("pre-create conflicting table: %v", err)
	}
	if err := pre.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	_, err = Open(path)
	if err == nil {
		t.Fatal("Open succeeded despite a conflicting schema, want an error")
	}
	if !strings.Contains(err.Error(), "001") {
		t.Errorf("error = %q, want it to name the failing migration file", err)
	}
}

func TestMigrationVersionsAreUniqueAndOrdered(t *testing.T) {
	files, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	sort.Strings(files)

	seen := map[string]string{}
	for _, f := range files {
		version, err := versionOf(f)
		if err != nil {
			t.Errorf("versionOf(%s): %v", f, err)
			continue
		}
		if !strings.HasSuffix(f, ".sql") {
			t.Errorf("%s does not end in .sql", f)
		}
		if prev, dup := seen[version]; dup {
			t.Errorf("version %s is used by both %s and %s", version, prev, f)
		}
		seen[version] = f
	}
	if len(files) < 2 {
		t.Errorf("only %d migrations embedded, want at least the initial schema and the seed", len(files))
	}
}

func TestVersionOf(t *testing.T) {
	for _, tc := range []struct {
		name    string
		want    string
		wantErr bool
	}{
		{"migrations/001_init.sql", "001", false},
		{"migrations/003_fix_remoteok_endpoint.sql", "003", false},
		{"migrations/010_add_index.sql", "010", false},
		{"migrations/nounderscore.sql", "", true},
		{"migrations/_leading.sql", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := versionOf(tc.name)
			if tc.wantErr {
				if err == nil {
					t.Errorf("versionOf(%q) = %q with no error, want an error", tc.name, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("versionOf(%q): %v", tc.name, err)
			}
			if got != tc.want {
				t.Errorf("versionOf(%q) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// --- DSN pragmas ---------------------------------------------------------

func TestOpenAppliesPerConnectionPragmas(t *testing.T) {
	database := openTest(t)
	ctx := context.Background()

	for _, tc := range []struct{ pragma, want string }{
		{"journal_mode", "wal"},
		{"foreign_keys", "1"},
		{"synchronous", "1"}, // NORMAL == 1
		{"busy_timeout", "5000"},
	} {
		var got string
		if err := database.QueryRowContext(ctx, "PRAGMA "+tc.pragma).Scan(&got); err != nil {
			t.Fatalf("PRAGMA %s: %v", tc.pragma, err)
		}
		if got != tc.want {
			t.Errorf("PRAGMA %s = %q, want %q", tc.pragma, got, tc.want)
		}
	}
}

// design: Foreign keys are declared in SQL and enabled per connection.
func TestForeignKeysAreEnforced(t *testing.T) {
	database := openTest(t)

	_, err := database.Exec(
		`INSERT INTO job_listings (company_id, title, listing_url) VALUES (999, 't', 'u')`)
	if err == nil {
		t.Fatal("an FK violation was accepted; foreign_keys is not enforced")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		t.Errorf("error = %v, want a foreign key violation", err)
	}
}

// --- schema constraints --------------------------------------------------

// insertCompany is a helper for constraint tests.
func insertCompany(t *testing.T, database *sql.DB, id int64, slug string) {
	t.Helper()
	if _, err := database.Exec(
		`INSERT INTO companies (id, slug, name) VALUES (?, ?, ?)`, id, slug, slug); err != nil {
		t.Fatalf("insert company %d: %v", id, err)
	}
}

func TestPlatformTypeCheck(t *testing.T) {
	database := openTest(t)
	if _, err := database.Exec(
		`INSERT INTO platforms (id, name, platform_type) VALUES (500, 'x', 'not_a_type')`); err == nil {
		t.Fatal("platforms.platform_type CHECK accepted an invalid value")
	}
}

func TestJobListingEnumChecks(t *testing.T) {
	database := openTest(t)
	insertCompany(t, database, 1, "acme")

	cases := []struct {
		name string
		sql  string
		args []any
	}{
		{"status", `INSERT INTO job_listings (company_id, title, listing_url, status) VALUES (?, 't', 'u', 'pending')`, []any{1}},
		{"employment_type", `INSERT INTO job_listings (company_id, title, listing_url, employment_type) VALUES (?, 't', 'u', 'gig')`, []any{1}},
		{"salary_period", `INSERT INTO job_listings (company_id, title, listing_url, salary_period) VALUES (?, 't', 'u', 'weekly')`, []any{1}},
		{"is_remote", `INSERT INTO job_listings (company_id, title, listing_url, is_remote) VALUES (?, 't', 'u', 2)`, []any{1}},
		{"is_us", `INSERT INTO job_listings (company_id, title, listing_url, is_us) VALUES (?, 't', 'u', 5)`, []any{1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := database.Exec(tc.sql, tc.args...); err == nil {
				t.Errorf("CHECK on %s accepted an invalid value", tc.name)
			}
		})
	}
}

func TestJobListingRequiredColumns(t *testing.T) {
	database := openTest(t)
	insertCompany(t, database, 1, "acme")

	if _, err := database.Exec(
		`INSERT INTO job_listings (company_id, title) VALUES (?, 'no url')`, 1); err == nil {
		t.Error("listing_url NOT NULL was not enforced")
	}
	if _, err := database.Exec(
		`INSERT INTO job_listings (company_id, listing_url) VALUES (?, 'u')`, 1); err == nil {
		t.Error("title NOT NULL was not enforced")
	}
}

func TestJSONColumnsValidate(t *testing.T) {
	database := openTest(t)
	insertCompany(t, database, 1, "acme")

	if _, err := database.Exec(
		`INSERT INTO job_listings (company_id, title, listing_url, tags_json) VALUES (?, 't', 'u', '{oops')`, 1); err == nil {
		t.Error("tags_json accepted invalid JSON")
	}
	if _, err := database.Exec(
		`INSERT INTO job_listings (company_id, title, listing_url, raw_data) VALUES (?, 't', 'u', 'not json')`, 1); err == nil {
		t.Error("raw_data accepted invalid JSON")
	}
	// A valid array and an explicit NULL must both be accepted.
	if _, err := database.Exec(
		`INSERT INTO job_listings (company_id, title, listing_url, tags_json, raw_data) VALUES (?, 't', 'u', '["go"]', NULL)`, 1); err != nil {
		t.Errorf("valid JSON or NULL was rejected: %v", err)
	}
}

// design: Idempotency contract - the upsert target for board-sourced rows.
func TestPartialUniqueIndexOnDiscovery(t *testing.T) {
	database := openTest(t)
	insertCompany(t, database, 1, "acme")

	insert := `INSERT INTO job_listings (company_id, title, listing_url, discovery_platform_id, external_id)
	           VALUES (?, ?, ?, 1, ?)`
	if _, err := database.Exec(insert, 1, "first", "u1", "1137412"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	// Same (discovery_platform_id, external_id) must collide.
	if _, err := database.Exec(insert, 1, "second", "u2", "1137412"); err == nil {
		t.Error("duplicate (discovery_platform_id, external_id) was accepted; the partial unique index is missing or not unique")
	}
	// A different external id is fine.
	if _, err := database.Exec(insert, 1, "third", "u3", "1137413"); err != nil {
		t.Errorf("distinct external_id rejected: %v", err)
	}
}

// The index is partial: rows where external_id IS NULL are outside it, which is
// exactly why an element with no identifier must be skipped rather than stored.
func TestPartialUniqueIndexIgnoresNullExternalID(t *testing.T) {
	database := openTest(t)
	insertCompany(t, database, 1, "acme")

	insert := `INSERT INTO job_listings (company_id, title, listing_url, discovery_platform_id, external_id)
	           VALUES (?, ?, ?, 1, NULL)`
	for i, title := range []string{"a", "b"} {
		if _, err := database.Exec(insert, 1, title, "u"+title); err != nil {
			t.Fatalf("insert %d with NULL external_id: %v", i, err)
		}
	}
	var n int
	if err := database.QueryRow(
		`SELECT count(*) FROM job_listings WHERE discovery_platform_id = 1 AND external_id IS NULL`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("got %d NULL-external_id rows, want 2 (the partial index must not constrain them)", n)
	}
}

func TestPartialUniqueIndexOnApplicationSystem(t *testing.T) {
	database := openTest(t)
	insertCompany(t, database, 1, "acme")
	if _, err := database.Exec(
		`INSERT INTO company_application_platforms (id, company_id, platform_id) VALUES (1, 1, 10)`); err != nil {
		t.Fatalf("insert application platform: %v", err)
	}

	insert := `INSERT INTO job_listings (company_id, title, listing_url, company_application_platform_id, external_id)
	           VALUES (1, ?, ?, 1, 'REQ-1')`
	if _, err := database.Exec(insert, "first", "u1"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := database.Exec(insert, "second", "u2"); err == nil {
		t.Error("duplicate (company_application_platform_id, external_id) was accepted")
	}
}

func TestCompanyPlatformPairIsUnique(t *testing.T) {
	database := openTest(t)
	insertCompany(t, database, 1, "acme")

	insert := `INSERT INTO company_application_platforms (company_id, platform_id) VALUES (1, 10)`
	if _, err := database.Exec(insert); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := database.Exec(insert); err == nil {
		t.Error("duplicate (company_id, platform_id) was accepted")
	}
}

func TestCompanySlugIsUnique(t *testing.T) {
	database := openTest(t)
	insertCompany(t, database, 1, "acme")
	if _, err := database.Exec(
		`INSERT INTO companies (id, slug, name) VALUES (2, 'acme', 'Acme Two')`); err == nil {
		t.Error("duplicate companies.slug was accepted")
	}
}

func TestScrapeRunStatusCheck(t *testing.T) {
	database := openTest(t)
	if _, err := database.Exec(
		`INSERT INTO scrape_runs (platform_id, started_at, status) VALUES (1, '2026-01-01T00:00:00Z', 'done')`); err == nil {
		t.Error("scrape_runs.status CHECK accepted 'done'")
	}
}

func TestTimestampDefaultsAreRFC3339UTC(t *testing.T) {
	database := openTest(t)
	insertCompany(t, database, 1, "acme")
	if _, err := database.Exec(
		`INSERT INTO job_listings (company_id, title, listing_url) VALUES (1, 't', 'u')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var firstSeen, lastSeen, created, updated string
	if err := database.QueryRow(
		`SELECT first_seen_at, last_seen_at, created_at, updated_at FROM job_listings LIMIT 1`).
		Scan(&firstSeen, &lastSeen, &created, &updated); err != nil {
		t.Fatalf("read timestamps: %v", err)
	}
	for name, value := range map[string]string{
		"first_seen_at": firstSeen,
		"last_seen_at":  lastSeen,
		"created_at":    created,
		"updated_at":    updated,
	} {
		if !strings.HasSuffix(value, "Z") {
			t.Errorf("%s = %q, want a trailing Z (UTC)", name, value)
		}
		if len(value) != len("2006-01-02T15:04:05.000Z") {
			t.Errorf("%s = %q, want millisecond precision", name, value)
		}
	}
}

// design: updated_at is maintained by application code, never by a trigger.
func TestNoTriggersAreDefined(t *testing.T) {
	database := openTest(t)
	var n int
	if err := database.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type = 'trigger'`).Scan(&n); err != nil {
		t.Fatalf("count triggers: %v", err)
	}
	if n != 0 {
		t.Errorf("found %d triggers, want 0 (updated_at is set by application code)", n)
	}
}

func TestIndexesExist(t *testing.T) {
	database := openTest(t)

	want := []string{
		"index_company_application_platforms_company",
		"index_job_listings_company",
		"index_job_listings_posted_at",
		"index_job_listings_status",
		"index_job_listings_us_remote_posted",
		"unique_job_listings_by_application_system",
		"unique_job_listings_by_discovery",
	}
	rows, err := database.Query(
		`SELECT name FROM sqlite_master WHERE type = 'index' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	defer rows.Close()

	got := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan index name: %v", err)
		}
		got[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate indexes: %v", err)
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("index %s is missing", name)
		}
	}
}
