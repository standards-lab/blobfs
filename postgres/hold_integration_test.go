//go:build integration

package postgres_test

import (
	"context"
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

// TestHoldWritesNoTuple proves the variant's hold takes the row lock
// without writing a row version, and that the lock is still the one Delete
// waits on. For each form of the hold, plain and at the row's version, a
// transaction holds an available row through the store over the engine;
// while it is open the row's ctid and xmin, read from another session, are
// the ones the row had before the hold, and a Delete on another
// transaction blocks; the ctid and xmin are unchanged still after the
// holder commits, and the Delete then returns the deleting row. The same
// hold through the baseline's store, the self-assigning update, moves the
// row to a new ctid and xmin, so the probe sees a write when there is one.
func TestHoldWritesNoTuple(t *testing.T) {
	const (
		blocked   = 500 * time.Millisecond
		unblocked = 5 * time.Second
	)
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
	baseline, err := data.New(c, db.Dialect())
	if err != nil {
		t.Fatalf("data.New over the baseline: %v", err)
	}
	dir, err := store.Directories.Create(ctx, db, blobfs.RootID, "hold-writes-no-tuple")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// version reads the row's physical location and the transaction that
	// wrote its current version, from the pool, outside any holder.
	version := func(id string) string {
		t.Helper()
		rows, err := db.QueryContext(ctx, "SELECT ctid::text || ' ' || xmin::text FROM blobfs_file WHERE id = $1", id)
		if err != nil {
			t.Fatalf("read the row version: %v", err)
		}
		defer func() { _ = rows.Close() }()
		var v string
		if !rows.Next() {
			t.Fatalf("read the row version: no row: %v", rows.Err())
		}
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("read the row version: %v", err)
		}
		return v
	}

	for _, c := range []struct {
		name string
		opts []data.HoldOption
	}{{"Plain", nil}, {"AtVersion", []data.HoldOption{data.AtVersion(1)}}} {
		t.Run(c.name, func(t *testing.T) {
			id, err := insertFile(ctx, db, dir.ID, "held-"+c.name+".txt", "available")
			if err != nil {
				t.Fatalf("insert: %v", err)
			}
			before := version(id)
			holder, err := db.Begin(ctx)
			if err != nil {
				t.Fatalf("Begin: %v", err)
			}
			ended := false
			defer func() {
				if !ended {
					_ = holder.Rollback()
				}
			}()
			if err := store.Files.Hold(ctx, holder, id, c.opts...); err != nil {
				t.Fatalf("Hold: %v", err)
			}
			if got := version(id); got != before {
				t.Errorf("the held row's ctid and xmin are %s, want %s as before the hold: the hold wrote a row version", got, before)
			}
			deleted := make(chan error, 1)
			go func() {
				_, err := db.Transact(ctx, func(tx *sqlate.Tx) (blobfs.File, error) {
					return store.Files.Delete(ctx, tx, id)
				})
				deleted <- err
			}()
			select {
			case err := <-deleted:
				t.Fatalf("the Delete returned (%v) while the hold's transaction was open", err)
			case <-time.After(blocked):
			}
			if got := version(id); got != before {
				t.Errorf("while the Delete waits, the row's ctid and xmin are %s, want %s", got, before)
			}
			ended = true
			if err := holder.Commit(); err != nil {
				t.Fatalf("Commit: %v", err)
			}
			select {
			case err := <-deleted:
				if err != nil {
					t.Fatalf("the Delete after the hold committed: %v", err)
				}
			case <-time.After(unblocked):
				t.Fatal("the Delete still blocks after the hold's transaction ended")
			}
			if f, err := store.Files.Find(ctx, db, id); err != nil || f.Status != blobfs.StatusDeleting {
				t.Errorf("after the Delete the row is %+v, %v, want it deleting", f, err)
			}
		})
	}

	t.Run("BaselineWrites", func(t *testing.T) {
		id, err := insertFile(ctx, db, dir.ID, "held-by-the-baseline.txt", "available")
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		before := version(id)
		if _, err := db.Transact(ctx, func(tx *sqlate.Tx) (struct{}, error) {
			return struct{}{}, baseline.Files.Hold(ctx, tx, id)
		}); err != nil {
			t.Fatalf("the baseline's Hold: %v", err)
		}
		if got := version(id); got == before {
			t.Errorf("the baseline's hold left the ctid and xmin at %s; the probe cannot tell a write from none", got)
		}
	})

	t.Run("RefusedHoldTakesNoLock", func(t *testing.T) {
		id, err := insertFile(ctx, db, dir.ID, "deleting.txt", "deleting")
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		holder, err := db.Begin(ctx)
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		defer func() { _ = holder.Rollback() }()
		if err := store.Files.Hold(ctx, holder, id); !errors.Is(err, blobfs.ErrDeleting) {
			t.Fatalf("Hold of a deleting row = %v, want ErrDeleting", err)
		}
		// The refused hold locked nothing, so a Purge on another session
		// removes the row at once.
		purged := make(chan error, 1)
		go func() { purged <- store.Files.Purge(ctx, db, id) }()
		select {
		case err := <-purged:
			if err != nil {
				t.Errorf("Purge beside a refused hold: %v", err)
			}
		case <-time.After(unblocked):
			t.Fatal("Purge blocks on a refused hold: the refused hold took the row's lock")
		}
	})
}
