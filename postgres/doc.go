// Package postgres is the PostgreSQL engine of blobfs: the Postgres
// variant of the persistence package's variation points, the native-tier
// statements the variant runs, and the DDL of the two tables, exported as
// a migration set. A consumer selects the engine by importing this
// package, as it imports sqlate's postgres module for the dialect; there
// is no registry, no init, and no flag, and the standard baseline is what
// data.New builds when no engine package is used. The package imports the
// persistence package, the root package, and sqlate only, and names no
// driver.
//
// # The variant
//
// Variant implements data.Variant over two native-tier statements, with
// the standard baseline embedded:
//
//   - The tree lock (lock_tree) is a transaction-scoped advisory lock,
//     pg_advisory_xact_lock over the fixed key TreeLockKey, so two
//     transactions that move directories run one after the other and
//     their cycle checks cannot both pass. Serializes reports true.
//   - Path resolution (resolve_path) walks every segment in one recursive
//     statement that indexes a text[] parameter by depth: one round trip
//     for any depth instead of one per segment. The segments bind as one
//     parameter, which the driver encodes from the Go slice, so no name is
//     spliced into the text.
//
// The returning commands need no variant: under sqlate's postgres
// dialect, which renders RETURNING, the store runs each of them as one
// statement from its standard-tier file. The keyset predicate needs none
// either: a consumer that registers sqlate's postgres.Patterns() in its
// catalog in place of query.Patterns() continues its pages by the
// row-value comparison, which Postgres serves as an index condition.
//
// Every statement file declares its tier as native and carries a port
// note: the engine feature it uses and what a port to another engine must
// provide. A consumer composes the variant at its composition root, over
// the one catalog and the dialect its store uses:
//
//	v, err := postgres.New(catalog, dialect)
//	store, err := data.New(catalog, dialect, data.WithVariant(v))
//
// The datatest package's suite proves the variant against the same
// contract the baseline satisfies, comparing each outcome with the
// baseline's on the same database; this module's integration tier runs it
// under both forms of the returning commands.
//
// # The migration set
//
// Migrations returns blobfs's migration set: the name Source, the history
// table Table, and two migrations, directory and file, embedded from the
// migrations directory. A consumer declares the set in migrate.New below
// its own, so blobfs's schema is at its head before the consumer's
// migrations reference it.
//
// The directory migration seeds the one root directory, the row with no
// parent, named /, and the id blobfs.RootID, and a partial unique index
// allows no second row without a parent. A check constraint states that a
// directory is named / exactly when it has no parent, so a root under
// another name or a non-root named / is refused.
//
// The set ships no index on blobfs_file (directory_id, created_at). The
// unique constraint on (directory_id, name) orders a sort by name, and a
// sort by created_at without that index sorts the directory. A consumer
// that lists by creation time adds the index in its own migration set,
// for example CREATE INDEX ix_blobfs_file_directory_created ON
// blobfs_file (directory_id, created_at).
//
// The set owns every object it creates, and every object's name starts
// with the set's name and an underscore: the tables blobfs_directory and
// blobfs_file, their constraints and indexes, and the history table
// blobfs_schema_version. Constraint and index names are public API,
// because a violation reaches a consumer as
// sqlate.ConstraintError.Constraint, and the persistence layer maps
// blobfs's own constraints to its sentinel errors through the root
// package's constants. The scheme is blobfs_<kind>_<table>_<detail>,
// where kind is pk, fk, uq, cc, or ix (a plain index), table is the table
// name without its blobfs_ prefix, and detail names the referenced
// relation, the indexed columns, or the checked rule.
//
// A released migration file never changes in text or name; the
// golden-hash test in this package pins each one. A second engine is a
// sub-module of its own with the same file names in its own migrations
// directory.
package postgres
