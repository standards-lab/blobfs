// Package datatest is the conformance suite of the data package: the checks
// every data.Store must pass against a live database, whatever the engine,
// the form the dialect gives the returning commands, and the variant the
// store forwards its variation points to. An engine sub-module's integration
// tier runs it over the standard baseline and over its own Engine's variant,
// under both forms of the returning commands, and a consumer that supplies
// an Engine of its own runs it over that. It lives in a package of its own
// because a test helper in a _test.go file cannot be imported by another
// module's tests.
//
// The suite is engine-agnostic: it imports no engine package and no
// driver, and every statement it runs outside the store is standard SQL
// with the dialect's placeholders. It takes the database from the caller:
// a throwaway database with blobfs's migration set applied, opened however
// the caller's test tier opens one, and Run is called once per database.
// It seeds directories through the store and file rows through plain SQL
// or through the write steps with a key validator that accepts every key.
// It creates two tables of its own, one that references blobfs_file and
// one that references blobfs_directory, to stand in for a consumer's
// foreign keys, and one index on blobfs_file (directory_id, created_at)
// that it drops again, to stand in for the one a consumer adds.
//
// Where an outcome belongs to a variation point or a returning command,
// the suite compares the store under test with a second store over the
// standard baseline against the same database, and asserts the same rows
// and the same refusals, in text, for the same inputs: a variant passes
// only when it is indistinguishable from the baseline in what it returns.
// The concurrency groups check the one outcome the variant decides,
// whether the tree lock serializes, by what Serializes reports.
package datatest

import (
	"context"
	"testing"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs/data"
)

// Run runs the suite as subtests of t, over a store it builds from catalog
// and db's dialect with engine, and over a second store it builds over the
// standard baseline to compare against. A nil engine is the store data.New
// builds without WithEngine, the baseline itself. catalog is the consumer's
// catalog: data.Patterns() registered beside the query library's patterns or
// an engine's overlay of them, which the caller chooses so a run covers the
// keyset spelling it ships. db is a migrated throwaway database, and its
// dialect is the one the store and the engine's statements are compiled for;
// a dialect that renders RETURNING runs every returning command in the
// single-statement form, and one that does not runs the fallback.
//
// The groups, in order: Verify (the store's statements, listings, and the
// engine's own statements prepared against the migrated schema);
// Directories (the root, reads by id and by name, Create and Ensure with
// their refusals by sentinel and constraint, a concurrent Ensure on the
// pool with the race forced, and Delete); Paths (FindByPath at every
// depth, a missing segment and a missing start, the refused forms, and
// names that would break a spliced path, against the baseline; Path after
// moves); Files (reads and Move, a deleting row refused whatever the
// version); Writes (Create, Ensure, and Complete of the write protocol
// against the baseline, a concurrent Ensure with the race forced, a
// complete after a concurrent delete refused as ErrDeleting, a write
// inside the caller's transaction, a retry after a stop, and
// caller-supplied ids); Deletes (Delete and Purge, retries at each step,
// and a consumer's reference); Holds (Hold's refusals through the
// variant's HoldFile against the baseline, and the two interleavings of a
// hold and a delete on the row lock); Moves (the tree lock, the directory
// move's sequential contract, and two opposing concurrent moves, which
// serialize or form a cycle as Serializes says, with IsWithin and Path
// terminating on the cycle and a Move repairing it, and which serializable
// isolation refuses on every variant); Listing (both listings against a
// plain query, with deleting rows hidden by default and shown with
// IncludeDeleting, the page boundaries, the counted total's rules for empty
// and continued pages, the total under concurrent inserts, and the
// refusals); Keyset (every cursorable sort walked to the end against the
// baseline and an offset walk, without and then with a sort index); Branches
// (MarkDeleting's counts over a three-level branch against the baseline, the
// marked rows read back deleting, the root and a missing directory refused,
// a repeated mark converging and reaching stragglers, every create, ensure,
// and move a deleting directory refuses, with the same refusals as the
// baseline, a marked branch hidden from the listings and shown by
// IncludeDeleting, the listing of a deleting directory refused, a cursor
// issued before a mark continuing without the marked rows, and Deleting's
// roots against a plain query); and Sweeps (Store.Sweep draining the
// branches the earlier groups left, then, on both stores, nothing to do, a
// full sweep of a three-level branch with its hook, a crash between an
// object's delete and its row's purge finished by the next pass, stragglers,
// the batch bound's More, a hook that aborts a removal, a refused branch
// that holds back none behind it, and the stale reclaim's age over abandoned
// writes and stopped deletes). Branches runs after the other groups because
// the branches it marks stay in the tree, and Deleting reads them all;
// Sweeps runs last because its first pass removes them.
func Run(t *testing.T, db *sqlate.DB, catalog *query.Catalog, engine data.Engine) {
	t.Helper()
	dialect := db.Dialect()
	if engine == nil {
		engine = baselineEngine
	}
	var variant data.Variant
	store, err := data.New(catalog, dialect, data.WithEngine(func(c *query.Catalog, d sqlate.Dialect, base *data.Standard) (data.Variant, error) {
		v, err := engine(c, d, base)
		variant = v
		return v, err
	}))
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
		variant:  variant,
		store:    store,
		baseline: baseline,
	}
	t.Run("Verify", s.verify)
	t.Run("Directories", s.directories)
	t.Run("Paths", s.paths)
	t.Run("Files", s.files)
	t.Run("Writes", s.writes)
	t.Run("Deletes", s.deletes)
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
	// variant is the variant the engine built for the store under test.
	variant data.Variant
	// store is the store under test.
	store *data.Store
	// baseline is a store over the standard baseline against the same
	// database, which the groups whose outcomes must match the baseline's
	// compare against; over the baseline itself it is a second store of the
	// same kind.
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
func baselineEngine(_ *query.Catalog, _ sqlate.Dialect, base *data.Standard) (data.Variant, error) {
	return base, nil
}
