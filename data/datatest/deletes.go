package datatest

import (
	"errors"
	"strings"
	"testing"

	"github.com/standards-lab/sqlate"

	"github.com/standards-lab/blobfs"
)

// deletes checks the delete protocol: Delete, then the object delete the
// consumer runs, then Purge.
func (s *suite) deletes(t *testing.T) {
	dir := s.mkdir(t, "delete-file-"+t.Name())
	t.Run("DeleteReturnsTheDeletingRow", func(t *testing.T) { s.deleteReturnsTheRow(t, dir.ID) })
	t.Run("RetryConverges", func(t *testing.T) { s.deleteRetryConverges(t, dir.ID) })
	t.Run("RollbackUndoesIt", func(t *testing.T) { s.deleteRollback(t, dir.ID) })
	t.Run("MissingIsNotFound", s.deleteMissing)
	t.Run("PurgeRefusesARowNotDeleting", func(t *testing.T) { s.purgeNotDeleting(t, dir.ID) })
	t.Run("ReferencedRowStaysDeleting", func(t *testing.T) { s.purgeReferenced(t, dir.ID) })
}

// deleteReturnsTheRow checks Delete from both statuses a delete may begin
// in, against the baseline: the row moved to deleting, its version
// advanced once and updated_at stamped, every other column unchanged, and
// the returned row the one the database holds; then Purge removes it.
func (s *suite) deleteReturnsTheRow(t *testing.T, dir string) {
	for _, status := range []blobfs.Status{blobfs.StatusAvailable, blobfs.StatusPending} {
		id := s.insertFile(t, dir, "returned-"+status.String()+".txt", status)
		before := s.file(t, id)
		got := s.beginDelete(t, id)
		wantDeleting(t, before, got)
		if stored := s.file(t, id); !equalFile(got, stored) {
			t.Errorf("Delete returned\n%+v\nbut the database holds\n%+v", got, stored)
		}
		baseID := s.insertFile(t, dir, "baseline-"+status.String()+".txt", status)
		base, err := s.db.Transact(s.ctx, func(tx *sqlate.Tx) (blobfs.File, error) {
			return s.baseline.Files.Delete(s.ctx, tx, baseID)
		})
		if err != nil || !sameFileShape(got, base) {
			t.Errorf("the baseline's Delete = %+v, %v, want the same shape as\n%+v", base, err, got)
		}
		s.purge(t, id)
		s.wantGone(t, id)
	}
}

// deleteRetryConverges checks a retry after a stop at each step: a second
// Delete returns the same row unchanged, a second Purge finds the row gone
// and succeeds, a Delete after the Purge reports the file gone, and a
// Purge of an id that never existed is the same success.
func (s *suite) deleteRetryConverges(t *testing.T, dir string) {
	id := s.insertFile(t, dir, "retried.txt", blobfs.StatusAvailable)
	once := s.beginDelete(t, id)
	again := s.beginDelete(t, id)
	if !equalFile(once, again) {
		t.Errorf("the retried Delete returned\n%+v\nwant the same row\n%+v", again, once)
	}
	if stored := s.file(t, id); !equalFile(once, stored) {
		t.Errorf("the retried Delete changed the row to\n%+v\nfrom\n%+v", stored, once)
	}
	s.purge(t, id)
	s.purge(t, id)
	s.wantGone(t, id)
	if _, err := s.deleteIn(id); !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("a Delete after the Purge = %v, want ErrNotFound", err)
	}
	s.purge(t, blobfs.NewID())
}

// deleteRollback checks Delete composes into the caller's transaction: a
// rollback leaves the row as it was.
func (s *suite) deleteRollback(t *testing.T, dir string) {
	id := s.insertFile(t, dir, "rolled-back.txt", blobfs.StatusAvailable)
	before := s.file(t, id)
	tx := s.beginTx(t)
	got, err := s.store.Files.Delete(s.ctx, tx, id)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("Delete: %v", err)
	}
	if got.Status != blobfs.StatusDeleting {
		t.Errorf("inside the transaction the row is %s, want deleting", got.Status)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if after := s.file(t, id); !equalFile(before, after) {
		t.Errorf("after the rollback the row is\n%+v\nwant it unchanged\n%+v", after, before)
	}
}

// deleteMissing checks a Delete of a file that does not exist, against
// the baseline.
func (s *suite) deleteMissing(t *testing.T) {
	id := blobfs.NewID()
	_, err := s.deleteIn(id)
	if !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("Delete(missing) = %v, want ErrNotFound", err)
	}
	_, base := s.db.Transact(s.ctx, func(tx *sqlate.Tx) (blobfs.File, error) {
		return s.baseline.Files.Delete(s.ctx, tx, id)
	})
	wantSameError(t, err, base)
}

// purgeNotDeleting checks Purge of a row whose delete has not begun: it is
// ErrNotDeleting naming the status, the same on both stores, and the row
// stays as it was.
func (s *suite) purgeNotDeleting(t *testing.T, dir string) {
	for _, status := range []blobfs.Status{blobfs.StatusAvailable, blobfs.StatusPending} {
		id := s.insertFile(t, dir, "untouched-"+status.String()+".txt", status)
		before := s.file(t, id)
		err := s.store.Files.Purge(s.ctx, s.db, id)
		if !errors.Is(err, blobfs.ErrNotDeleting) || !strings.Contains(err.Error(), status.String()) {
			t.Errorf("Purge of a %s row = %v, want ErrNotDeleting naming the status", status, err)
		}
		wantSameError(t, err, s.baseline.Files.Purge(s.ctx, s.db, id))
		if after := s.file(t, id); !equalFile(before, after) {
			t.Errorf("the refused Purge changed the %s row to\n%+v\nfrom\n%+v", status, after, before)
		}
	}
}

// purgeReferenced checks Purge of a row a consumer's row references: it is
// ErrReferenced over the consumer's constraint, never blobfs's own
// ErrNotEmpty, and the row stays deleting until the reference goes, when
// a retry removes it.
func (s *suite) purgeReferenced(t *testing.T, dir string) {
	s.createFileReferences(t)
	id := s.insertFile(t, dir, "referenced.txt", blobfs.StatusAvailable)
	s.reference(t, s.db, id)
	deleting := s.beginDelete(t, id)
	err := s.store.Files.Purge(s.ctx, s.db, id)
	wantViolation(t, err, blobfs.ErrReferenced, referenceConstraint)
	var ce *sqlate.ConstraintError
	if errors.Is(err, blobfs.ErrNotEmpty) || !errors.As(err, &ce) || !errors.Is(ce.Class, sqlate.ErrForeignKeyViolation) {
		t.Errorf("Purge of a referenced row = %v, want ErrReferenced by the violation's class", err)
	}
	if after := s.file(t, id); !equalFile(deleting, after) {
		t.Errorf("the refused Purge changed the row to\n%+v\nfrom\n%+v", after, deleting)
	}
	s.unreference(t, id)
	s.purge(t, id)
	s.wantGone(t, id)
}

// beginDelete runs Delete through the store under test in a transaction
// of its own, commits it, and returns the deleting row.
func (s *suite) beginDelete(t *testing.T, id string) blobfs.File {
	t.Helper()
	f, err := s.deleteIn(id)
	if err != nil {
		t.Fatalf("Files.Delete(%s): %v", id, err)
	}
	return f
}

// deleteIn runs Delete through the store under test in a transaction of
// its own and commits it.
func (s *suite) deleteIn(id string) (blobfs.File, error) {
	return s.db.Transact(s.ctx, func(tx *sqlate.Tx) (blobfs.File, error) {
		return s.store.Files.Delete(s.ctx, tx, id)
	})
}

// purge runs Purge on the pool and fails the test on any error.
func (s *suite) purge(t *testing.T, id string) {
	t.Helper()
	if err := s.store.Files.Purge(s.ctx, s.db, id); err != nil {
		t.Fatalf("Files.Purge(%s): %v", id, err)
	}
}

// wantGone checks that no row with id exists.
func (s *suite) wantGone(t *testing.T, id string) {
	t.Helper()
	if f, err := s.store.Files.Find(s.ctx, s.db, id); !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("after the Purge the row is %+v, %v; want it gone", f, err)
	}
}

// wantDeleting checks got against before: the same row moved to deleting,
// its version advanced by one, updated_at moved forward, and every other
// column unchanged.
func wantDeleting(t *testing.T, before, got blobfs.File) {
	t.Helper()
	if got.Status != blobfs.StatusDeleting || got.Version != before.Version+1 || !got.UpdatedAt.After(before.UpdatedAt) {
		t.Errorf("Delete returned %s at version %d updated %v, want deleting at %d updated after %v", got.Status, got.Version, got.UpdatedAt, before.Version+1, before.UpdatedAt)
	}
	want := before
	want.Status, want.Version, want.UpdatedAt = got.Status, got.Version, got.UpdatedAt
	if !equalFile(want, got) {
		t.Errorf("Delete changed more than the status, the version, and updated_at:\n%+v\nfrom\n%+v", got, before)
	}
}
