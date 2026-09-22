// embed.go: embeds the migrations directory into an fs.FS for the migration runner in db.go.
package db

import "embed"

// migrationsFS holds every migrations/*.sql file, compiled into the binary so a
// deployed NAS build needs no SQL files on disk. Paths inside the FS keep the
// "migrations/" prefix, e.g. "migrations/001_init.sql".
//
//go:embed migrations/*.sql
var migrationsFS embed.FS
