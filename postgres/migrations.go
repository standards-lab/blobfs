package postgres

import (
	"embed"
	"fmt"

	"github.com/standards-lab/sqlate/migrate"
)

const (
	// Source is the migration set's name and the prefix of every object
	// the set owns.
	Source = "blobfs"

	// Table is the history table the set's migrations are recorded in. It
	// is distinct from a consumer's own history table, so the two sets keep
	// separate version lines.
	Table = "blobfs_schema_version"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations returns blobfs's migration set for PostgreSQL, named Source,
// recorded in Table, and read from the embedded migrations directory. A
// consumer declares it below its own set in migrate.New.
func Migrations() (migrate.Set, error) {
	files, err := migrate.Files(migrationFiles, "migrations")
	if err != nil {
		return migrate.Set{}, fmt.Errorf("blobfs/postgres: %w", err)
	}
	return migrate.Set{Name: Source, Table: Table, Migrations: files}, nil
}
