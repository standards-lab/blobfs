//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/standards-lab/sqlate"
	sqlatepg "github.com/standards-lab/sqlate/postgres"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs/data"
	"github.com/standards-lab/blobfs/data/datatest"
	"github.com/standards-lab/blobfs/postgres"
	"github.com/standards-lab/blobfs/postgres/internal/dbtest"
)

// forms are the two forms a returning command runs in on Postgres: one
// statement under sqlate's postgres dialect, which renders RETURNING, and
// the command followed by its read under the same dialect with its
// capabilities hidden.
var forms = []struct {
	name    string
	dialect sqlate.Dialect
}{
	{"SingleStatement", sqlatepg.Dialect{}},
	{"Fallback", fallback{sqlatepg.Dialect{}}},
}

// TestConformance runs the conformance suite over the matrix of the two
// forms of the returning commands and the two variants, the standard
// baseline (the store data.New builds without WithEngine) and the
// Postgres variant Engine builds, each in a throwaway database of its
// own, with the catalog built from the engine's overlay of the library's
// patterns; and once more over the Postgres engine with the library's own
// patterns, so the standard spelling of the keyset predicate runs on the
// engine too. The baseline reports that it does not serialize, and the
// suite proves the cycle two opposing moves form on it; the Postgres
// variant reports that it does, and the suite proves the second move
// refused.
func TestConformance(t *testing.T) {
	for _, form := range forms {
		for _, variant := range []string{"Standard", "Postgres"} {
			t.Run(form.name+"/"+variant, func(t *testing.T) {
				t.Parallel()
				conform(t, form.dialect, sqlatepg.Patterns(), variant == "Postgres")
			})
		}
	}
	t.Run("StandardKeyset/Postgres", func(t *testing.T) {
		t.Parallel()
		conform(t, sqlatepg.Dialect{}, query.Patterns(), true)
	})
}

// conform runs the suite in a throwaway database under dialect, with the
// catalog built from patterns and blobfs's own, over the Postgres engine
// or the baseline.
func conform(t *testing.T, dialect sqlate.Dialect, patterns query.Source, native bool) {
	d := dbtest.Migrated(t)
	c, err := query.NewCatalog(patterns, data.Patterns())
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	var (
		engine data.Engine
		opts   []data.Option
	)
	if native {
		engine = postgres.Engine
		opts = append(opts, data.WithEngine(engine))
	}
	store, err := data.New(c, dialect, opts...)
	if err != nil {
		t.Fatalf("data.New: %v", err)
	}
	if store.Directories.Serializes() != native {
		t.Fatalf("the store reports Serializes %v over the %s", store.Directories.Serializes(), map[bool]string{true: "Postgres engine", false: "baseline"}[native])
	}
	datatest.Run(t, d.Session(dialect), c, engine)
}

// TestTreeLockIsAnAdvisoryLock proves the lock LockTree takes is a
// transaction-scoped advisory lock under TreeLockKey, which the engine
// reports in pg_locks while the transaction runs and releases when it
// commits.
func TestTreeLockIsAnAdvisoryLock(t *testing.T) {
	ctx := context.Background()
	d := dbtest.Migrated(t)
	db := d.Session(sqlatepg.Dialect{})
	c, err := query.NewCatalog(sqlatepg.Patterns(), data.Patterns())
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	store, err := data.New(c, db.Dialect(), data.WithEngine(postgres.Engine))
	if err != nil {
		t.Fatalf("data.New: %v", err)
	}
	// An advisory lock on a bigint key reports its high half as classid and
	// its low half as objid, with objsubid 1.
	held := func() int {
		return dbtest.Int(ctx, t, db, "SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND granted AND objsubid = 1"+
			" AND database = (SELECT oid FROM pg_database WHERE datname = current_database())"+
			" AND ((CAST(classid AS bigint) << 32) | CAST(objid AS bigint)) = $1", postgres.TreeLockKey)
	}
	if n := held(); n != 0 {
		t.Fatalf("%d tree locks held before the test", n)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := store.Directories.LockTree(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatalf("LockTree: %v", err)
	}
	if n := held(); n != 1 {
		t.Errorf("%d tree locks held inside the transaction, want 1", n)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if n := held(); n != 0 {
		t.Errorf("%d tree locks held after the commit, want 0", n)
	}
}
