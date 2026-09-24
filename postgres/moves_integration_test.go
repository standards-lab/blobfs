//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/standards-lab/sqlate"
	sqlatepg "github.com/standards-lab/sqlate/postgres"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data"
	"github.com/standards-lab/blobfs/postgres"
	"github.com/standards-lab/blobfs/postgres/internal/dbtest"
)

// TestOpposingRepeatableReadMoves proves what Directories.Move documents
// for repeatable read on PostgreSQL, over the engine's variant and over the
// baseline: transaction A moves X under Y and transaction B moves Y under
// X, each at repeatable read. B's snapshot predates A's commit, so B's
// cycle check passes on either variant: over the engine B's first
// statement is the tree lock, which takes the snapshot before it blocks
// behind A, and over the baseline the lock is a no-op and A has not
// committed. The tree lock therefore does not keep the cycle out at this
// level; the engine does, at B's update, whose foreign-key check locks X,
// the row A's move updated in a key column, parent_id, after B's snapshot:
// B is refused with a serialization failure, and no cycle forms. A caller
// at repeatable read retries a refused move in a new transaction.
func TestOpposingRepeatableReadMoves(t *testing.T) {
	const unblocked = 5 * time.Second
	ctx := context.Background()
	d := dbtest.Migrated(t)
	db := d.Session(sqlatepg.Dialect{})
	c, err := query.NewCatalog(sqlatepg.Patterns(), data.Patterns())
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	repeatable := sqlate.Isolation(sql.LevelRepeatableRead)
	for _, v := range []struct {
		name string
		opts []data.Option
	}{{"Postgres", []data.Option{data.WithEngine(postgres.Engine)}}, {"Standard", nil}} {
		t.Run(v.name, func(t *testing.T) {
			store, err := data.New(c, db.Dialect(), v.opts...)
			if err != nil {
				t.Fatalf("data.New: %v", err)
			}
			x, err := store.Directories.Create(ctx, db, blobfs.RootID, "rr-x-"+v.name)
			if err != nil {
				t.Fatalf("Create x: %v", err)
			}
			y, err := store.Directories.Create(ctx, db, blobfs.RootID, "rr-y-"+v.name)
			if err != nil {
				t.Fatalf("Create y: %v", err)
			}

			a, err := db.Begin(ctx, repeatable)
			if err != nil {
				t.Fatalf("Begin A: %v", err)
			}
			defer func() { _ = a.Rollback() }()
			if _, err := store.Directories.Move(ctx, a, x.ID, y.ID, x.Name, x.Version); err != nil {
				t.Fatalf("A's move: %v", err)
			}
			b, err := db.Begin(ctx, repeatable)
			if err != nil {
				t.Fatalf("Begin B: %v", err)
			}
			defer func() { _ = b.Rollback() }()
			moved := make(chan error, 1)
			go func() {
				_, err := store.Directories.Move(ctx, b, y.ID, x.ID, y.Name, y.Version)
				moved <- err
			}()
			// B waits, on the tree lock over the engine and on X's row lock
			// over the baseline, until A commits.
			select {
			case err := <-moved:
				t.Fatalf("B's move returned (%v) while A was open", err)
			case <-time.After(500 * time.Millisecond):
			}
			if err := a.Commit(); err != nil {
				t.Fatalf("A's commit: %v", err)
			}
			select {
			case err := <-moved:
				if !errors.Is(err, sqlate.ErrSerializationFailure) {
					t.Errorf("B's move = %v, want a serialization failure", err)
				}
			case <-time.After(unblocked):
				t.Fatal("B's move did not return after A committed")
			}
			_ = b.Rollback()
			if p, err := store.Directories.Path(ctx, db, x.ID); err != nil || p != "/"+y.Name+"/"+x.Name {
				t.Errorf("Path(x) = %q, %v, want x under y and no cycle", p, err)
			}
			if p, err := store.Directories.Path(ctx, db, y.ID); err != nil || p != "/"+y.Name {
				t.Errorf("Path(y) = %q, %v, want y under the root", p, err)
			}
		})
	}
}
