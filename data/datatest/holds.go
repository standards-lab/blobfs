package datatest

import (
	"errors"
	"testing"
	"time"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data"
)

// blocked is how long a check waits to conclude that a call blocks, and
// unblocked how long it waits for a blocked call to return once the
// transaction it waits on ends.
const (
	blocked   = 500 * time.Millisecond
	unblocked = 5 * time.Second
)

// holds checks Hold against its contract and against Delete: the two must
// take the same row lock, whatever statement form the dialect runs
// Delete's update in.
func (s *suite) holds(t *testing.T) {
	dir := s.mkdir(t, "hold-"+t.Name())
	t.Run("HeldRowIsUnchanged", func(t *testing.T) { s.heldRowUnchanged(t, dir.ID) })
	t.Run("Refusals", func(t *testing.T) { s.holdRefusals(t, dir.ID) })
	t.Run("HoldMakesTheDeleteWait", func(t *testing.T) { s.holdThenDelete(t, dir.ID) })
	t.Run("DeleteMakesTheHoldWait", func(t *testing.T) { s.deleteThenHold(t, dir.ID) })
}

// heldRowUnchanged checks a hold of a pending and of an available row,
// plain and at the row's version: each succeeds and changes nothing, the
// version included.
func (s *suite) heldRowUnchanged(t *testing.T, dir string) {
	for _, status := range []blobfs.Status{blobfs.StatusAvailable, blobfs.StatusPending} {
		id := s.insertFile(t, dir, "held-"+status.String()+".txt", status)
		before := s.file(t, id)
		for _, opts := range [][]data.HoldOption{nil, {data.AtVersion(before.Version)}} {
			if err := s.holdIn(id, opts...); err != nil {
				t.Fatalf("Hold of a %s row with %d options: %v", status, len(opts), err)
			}
		}
		if after := s.file(t, id); !equalFile(before, after) {
			t.Errorf("the holds changed the %s row to\n%+v\nfrom\n%+v", status, after, before)
		}
	}
}

// holdRefusals checks the hold's refusals against the baseline: a stale
// version is ErrVersionMismatch; a deleting row is ErrDeleting whatever
// version is asked for, and never a version mismatch; and a missing row
// is ErrNotFound. None changes the row.
func (s *suite) holdRefusals(t *testing.T, dir string) {
	stale := s.insertFile(t, dir, "stale.txt", blobfs.StatusAvailable)
	deletingID := s.insertFile(t, dir, "deleting.txt", blobfs.StatusAvailable)
	before := s.file(t, deletingID)
	deleting := s.beginDelete(t, deletingID)
	for _, c := range []struct {
		name string
		id   string
		opts []data.HoldOption
		want error
		not  error
	}{
		{"StaleVersion", stale, []data.HoldOption{data.AtVersion(2)}, query.ErrVersionMismatch, blobfs.ErrDeleting},
		{"Deleting", deletingID, nil, blobfs.ErrDeleting, query.ErrVersionMismatch},
		{"DeletingAtItsOldVersion", deletingID, []data.HoldOption{data.AtVersion(before.Version)}, blobfs.ErrDeleting, query.ErrVersionMismatch},
		{"DeletingAtItsVersion", deletingID, []data.HoldOption{data.AtVersion(deleting.Version)}, blobfs.ErrDeleting, query.ErrVersionMismatch},
		{"Missing", blobfs.NewID(), nil, blobfs.ErrNotFound, blobfs.ErrDeleting},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := s.holdIn(c.id, c.opts...)
			if !errors.Is(err, c.want) || errors.Is(err, c.not) {
				t.Errorf("Hold = %v, want %v and not %v", err, c.want, c.not)
			}
			_, base := s.db.Transact(s.ctx, func(tx *sqlate.Tx) (struct{}, error) {
				return struct{}{}, s.baseline.Files.Hold(s.ctx, tx, c.id, c.opts...)
			})
			wantSameError(t, err, base)
		})
	}
	if after := s.file(t, stale); after.Version != 1 || after.Status != blobfs.StatusAvailable {
		t.Errorf("the refused hold changed the row to %+v", after)
	}
	if after := s.file(t, deletingID); !equalFile(deleting, after) {
		t.Errorf("the refused holds changed the deleting row to\n%+v\nfrom\n%+v", after, deleting)
	}
}

// holdThenDelete checks the first interleaving: a holder inserts its
// reference under the hold; a Delete that starts meanwhile waits for the
// holder to commit and then runs, and the reference is there for the
// consumer's own check after the Delete to see, and for the foreign key
// to refuse Purge on.
func (s *suite) holdThenDelete(t *testing.T, dir string) {
	s.createFileReferences(t)
	id := s.insertFile(t, dir, "held-then-deleted.txt", blobfs.StatusAvailable)
	holder := s.beginTx(t)
	ended := false
	defer func() {
		if !ended {
			_ = holder.Rollback()
		}
	}()
	if err := s.store.Files.Hold(s.ctx, holder, id); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	s.reference(t, holder, id)
	deleted := make(chan deleteResult, 1)
	go func() {
		f, err := s.deleteIn(id)
		deleted <- deleteResult{file: f, err: err}
	}()
	select {
	case r := <-deleted:
		t.Fatalf("the Delete returned (%+v, %v) while the hold's transaction was open", r.file, r.err)
	case <-time.After(blocked):
	}
	ended = true
	if err := holder.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	var r deleteResult
	select {
	case r = <-deleted:
	case <-time.After(unblocked):
		t.Fatal("the Delete still blocks after the hold's transaction ended")
	}
	if r.err != nil || r.file.Status != blobfs.StatusDeleting {
		t.Fatalf("the Delete after the hold committed = %+v, %v; want the deleting row", r.file, r.err)
	}
	if n := s.references(t, id); n != 1 {
		t.Errorf("%d references exist after the Delete, want the one the holder committed", n)
	}
	if err := s.store.Files.Purge(s.ctx, s.db, id); !errors.Is(err, blobfs.ErrReferenced) {
		t.Errorf("Purge while the reference remains = %v, want ErrReferenced", err)
	}
	s.unreference(t, id)
	s.purge(t, id)
	s.wantGone(t, id)
}

// deleteThenHold checks the second interleaving: a Delete runs first and
// a hold that starts meanwhile waits; a commit of the Delete leaves the
// hold refusing the deleting row, and a rollback leaves it holding the
// row.
func (s *suite) deleteThenHold(t *testing.T, dir string) {
	for _, end := range []struct {
		name string
		end  func(*sqlate.Tx) error
		want error
	}{
		{"Commit", (*sqlate.Tx).Commit, blobfs.ErrDeleting},
		{"Rollback", (*sqlate.Tx).Rollback, nil},
	} {
		t.Run(end.name, func(t *testing.T) {
			id := s.insertFile(t, dir, "deleted-then-held-"+end.name+".txt", blobfs.StatusAvailable)
			deleter := s.beginTx(t)
			ended := false
			defer func() {
				if !ended {
					_ = deleter.Rollback()
				}
			}()
			if _, err := s.store.Files.Delete(s.ctx, deleter, id); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			holder := s.beginTx(t)
			defer func() { _ = holder.Rollback() }()
			held := make(chan error, 1)
			go func() { held <- s.store.Files.Hold(s.ctx, holder, id) }()
			select {
			case err := <-held:
				t.Fatalf("the hold returned (%v) while the Delete's transaction was open", err)
			case <-time.After(blocked):
			}
			ended = true
			if err := end.end(deleter); err != nil {
				t.Fatalf("ending the Delete's transaction: %v", err)
			}
			select {
			case err := <-held:
				if !errors.Is(err, end.want) || (end.want == nil && err != nil) {
					t.Errorf("the hold after the Delete's %s = %v, want %v", end.name, err, end.want)
				}
			case <-time.After(unblocked):
				t.Fatal("the hold still blocks after the Delete's transaction ended")
			}
		})
	}
}

// deleteResult is what a Delete on a goroutine reports.
type deleteResult struct {
	file blobfs.File
	err  error
}

// holdIn runs Hold through the store under test in a transaction of its
// own and commits it.
func (s *suite) holdIn(id string, opts ...data.HoldOption) error {
	_, err := s.db.Transact(s.ctx, func(tx *sqlate.Tx) (struct{}, error) {
		return struct{}{}, s.store.Files.Hold(s.ctx, tx, id, opts...)
	})
	return err
}
