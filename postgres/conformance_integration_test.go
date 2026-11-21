//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/standards-lab/sqlate"
	sqlatepg "github.com/standards-lab/sqlate/postgres"
	"github.com/standards-lab/sqlate/postgres/pgtest"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs/data"
	"github.com/standards-lab/blobfs/data/datatest"
	"github.com/standards-lab/blobfs/postgres"
	"github.com/standards-lab/blobfs/postgres/internal/dbtest"
)

var returningForms = []struct {
	name    string
	dialect sqlate.Dialect
}{
	{"SingleStatement", sqlatepg.Dialect{}},
	{"Fallback", fallback{sqlatepg.Dialect{}}},
}

func TestConformance(t *testing.T) {
	for _, form := range returningForms {
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

func conform(t *testing.T, dialect sqlate.Dialect, patterns query.Source, native bool) {
	d := pgtest.Migrated(t, dbtest.Env, dbtest.Prefix, dbtest.Migrations(t))
	c, err := query.NewCatalog(patterns, data.Patterns())
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	var engine data.Engine
	if native {
		engine = postgres.Engine
	}
	store, err := data.New(c, dialect, data.Options{Engine: engine})
	if err != nil {
		t.Fatalf("data.New: %v", err)
	}
	if store.Directories.Serializes() != native {
		t.Fatalf("the store reports Serializes %v over the %s", store.Directories.Serializes(), map[bool]string{true: "Postgres engine", false: "baseline"}[native])
	}
	datatest.Run(t, sqlate.Wrap(d.Pool, dialect), c, engine)
}

func TestTreeLockIsAnAdvisoryLock(t *testing.T) {
	ctx := context.Background()
	d := pgtest.Migrated(t, dbtest.Env, dbtest.Prefix, dbtest.Migrations(t))
	db := d.Session()
	c, err := query.NewCatalog(sqlatepg.Patterns(), data.Patterns())
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	store, err := data.New(c, db.Dialect(), data.Options{Engine: postgres.Engine})
	if err != nil {
		t.Fatalf("data.New: %v", err)
	}
	held := func() int {
		return dbtest.Int(ctx, t, db, "SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND granted AND objsubid = 1"+
			" AND database = (SELECT oid FROM pg_database WHERE datname = current_database())"+
			" AND ((CAST(classid AS bigint) << 32) | CAST(objid AS bigint)) = $1", postgres.TreeLockKey)
	}
	if n := held(); n != 0 {
		t.Fatalf("%d tree locks held before the test", n)
	}
	tx, err := db.Begin(ctx, nil)
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
