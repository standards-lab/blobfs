// Package datatest is the conformance suite of the data package: Run, the
// checks every data.Store must pass against a live database, whatever the
// engine, the returning commands' form, and the variant. An engine
// sub-module's integration tier runs it over the baseline and its own
// variant; a consumer with an Engine of its own runs it over that. It is a package of
// its own because another module's tests cannot import a _test.go helper.
//
// The suite imports no engine and no driver, and every statement it runs
// outside the store is standard SQL. Where an outcome belongs to a
// variation point or a returning command, it compares the store under test
// with a second store over the baseline on the same database, rows and
// refusals in text. It creates two tables of its own, standing in for a
// consumer's foreign keys into blobfs_file and blobfs_directory, and an
// index on blobfs_file (directory_id, created_at), which it drops again.
// See "datatest: the conformance suite" in docs/features.md.
//
// Beside the suite, FileRows and DirectoryRows script blobfs's rows for
// the query library's scripted driver, sqltest, so a consumer's unit tests
// of code over the store read the rows its statements scan.
package datatest

import (
	"context"
	"testing"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs/data"
)

// Run runs the suite as subtests of t, over a store built from catalog and
// db's dialect with engine, and over a second store on the baseline to
// compare against. A nil engine is the baseline itself. catalog carries
// data.Patterns() beside the query library's patterns or an engine's
// overlay of them. db is a throwaway database with blobfs's migration set
// applied, and Run is called once per database; a dialect that renders
// RETURNING covers the single-statement form, and one that does not the
// fallback.
//
// The groups run in the order docs/features.md lists: Verify, Directories,
// Paths, Files, Writes, Deletes, Protocols, Holds, Moves, Listing, Keyset,
// Branches, and Sweeps. Protocols runs before any group marks a branch,
// and sweeps the branches it marks itself. Branches runs after the others
// because the branches it marks stay in the tree; Sweeps runs last because
// its first pass removes them.
func Run(t *testing.T, db *sqlate.DB, catalog *query.Catalog, engine data.Engine) {
	t.Helper()
	dialect := db.Dialect()
	if engine == nil {
		engine = baselineEngine
	}
	store, err := data.New(catalog, dialect, data.WithEngine(engine))
	if err != nil {
		t.Fatalf("data.New over the engine under test: %v", err)
	}
	baseline, err := data.New(catalog, dialect)
	if err != nil {
		t.Fatalf("data.New over the baseline: %v", err)
	}
	s := &suite{
		ctx:      context.Background(),
		db:       db,
		catalog:  catalog,
		engine:   engine,
		store:    store,
		baseline: baseline,
	}
	t.Run("Verify", s.verify)
	t.Run("Directories", s.directories)
	t.Run("Paths", s.paths)
	t.Run("Files", s.files)
	t.Run("Writes", s.writes)
	t.Run("Deletes", s.deletes)
	t.Run("Protocols", s.protocols)
	t.Run("Holds", s.holds)
	t.Run("Moves", s.moves)
	t.Run("Listing", s.listing)
	t.Run("Keyset", s.keyset)
	t.Run("Branches", s.branches)
	t.Run("Sweeps", s.sweeps)
}

// suite is one run's state.
type suite struct {
	ctx     context.Context
	db      *sqlate.DB
	catalog *query.Catalog
	// engine is the engine under test, the baseline's when Run was given
	// none, which the move group wraps to interleave two moves.
	engine data.Engine
	// store is the store under test.
	store *data.Store
	// baseline is a store over the standard baseline on the same database,
	// which the groups compare against.
	baseline *data.Store
	// fileReferences and directoryReferences record that the suite's
	// stand-ins for a consumer's tables exist, so the groups that need them
	// share one of each.
	fileReferences      bool
	directoryReferences bool
}

// verify checks the store against the migrated schema: every statement
// and each returning command's single-statement form prepared, both
// listings' field contracts and keyset pages probed, and the variant's
// own statements, when it has any, in the same pass.
func (s *suite) verify(t *testing.T) {
	if err := s.store.Verify(s.ctx, s.db); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if err := s.baseline.Verify(s.ctx, s.db); err != nil {
		t.Fatalf("Verify of the baseline: %v", err)
	}
}

// baselineEngine is the engine of a run given none: the baseline itself.
func baselineEngine(_ *query.Catalog, _ sqlate.Dialect, base data.Variant) (data.Variant, error) {
	return base, nil
}
