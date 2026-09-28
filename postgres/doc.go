// Package postgres is the PostgreSQL engine of blobfs: Engine, which
// builds the PostgreSQL variant of the data package's variation points
// over native-tier statements, and Migrations, blobfs's schema as a
// migration set. A consumer selects it by importing it and installing
// Engine, with no registry, init, or flag:
//
//	store, err := data.New(catalog, dialect, data.WithEngine(postgres.Engine))
//
// The variant embeds the baseline it is given and overrides all three
// variation points: the tree lock is pg_advisory_xact_lock over
// TreeLockKey, path resolution is one recursive statement over a text[]
// parameter, and the file hold is SELECT ... FOR NO KEY UPDATE, which
// writes no row version. Each statement file declares the native tier and
// carries a port note. The package imports only the data package, the
// root package, and sqlate, and names no driver.
//
// A released migration file never changes in text or name; the
// golden-hash test in this package pins each one. See "postgres: the
// PostgreSQL engine" and "The schema" in docs/features.md.
package postgres
