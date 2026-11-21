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
	store, err := data.New(catalog, dialect, data.Options{Engine: engine})
	if err != nil {
		t.Fatalf("data.New over the engine under test: %v", err)
	}
	baseline, err := data.New(catalog, dialect, data.Options{})
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

type suite struct {
	ctx                            context.Context
	db                             *sqlate.DB
	catalog                        *query.Catalog
	engine                         data.Engine
	store                          *data.Store
	baseline                       *data.Store
	fileReferenceTableCreated      bool
	directoryReferenceTableCreated bool
}

func (s *suite) verify(t *testing.T) {
	if err := s.store.Verify(s.ctx, s.db); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if err := s.baseline.Verify(s.ctx, s.db); err != nil {
		t.Fatalf("Verify of the baseline: %v", err)
	}
}

func baselineEngine(_ *query.Catalog, _ sqlate.Dialect, base data.Variant) (data.Variant, error) {
	return base, nil
}
