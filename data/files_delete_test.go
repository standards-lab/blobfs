package data_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"
	"github.com/standards-lab/sqlate/sqltest"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data"
)

// TestHoldFile proves the hold against the script, inside a transaction:
// one exec of the self-assigning update bound to the id, with the version
// predicate and its binding only under AtVersion, and a row affected ends
// the call with no read. When no row is affected the row is read once, in
// the same transaction: a missing row is ErrNotFound, a deleting row
// ErrDeleting whatever its version, a row at another version under
// AtVersion query.ErrVersionMismatch naming both versions, and any other
// row an error naming its state.
func TestHoldFile(t *testing.T) {
	ctx := context.Background()
	// hold runs one hold of F inside a transaction over the scripted
	// responses and returns its error and the recorder.
	hold := func(t *testing.T, responses []sqltest.Response, opts ...data.HoldOption) (*sqltest.Recorder, error) {
		t.Helper()
		s, db, rec := openStore(t, fallback, responses...)
		_, err := db.Transact(ctx, func(tx *sqlate.Tx) (struct{}, error) {
			return struct{}{}, s.Files.Hold(ctx, tx, "F", opts...)
		})
		return rec, err
	}

	rec, err := hold(t, []sqltest.Response{{Affected: 1}})
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if got := ops(rec); got != "begin exec commit" {
		t.Errorf("ops = %q, want the one exec and no read", got)
	}
	update := rec.Calls()[1]
	if update.SQL != "UPDATE blobfs_file\nSET updated_at = updated_at\nWHERE id = CAST($1 AS uuid) AND status <> 'deleting'" {
		t.Errorf("the hold is not the self-assigning update:\n%s", update.SQL)
	}
	if !slices.Equal(update.Args, []any{"F"}) {
		t.Errorf("the hold bound %v, want the id alone", update.Args)
	}

	rec, err = hold(t, []sqltest.Response{{Affected: 1}}, data.AtVersion(4))
	if err != nil {
		t.Fatalf("Hold at a version: %v", err)
	}
	update = rec.Calls()[1]
	if !strings.HasSuffix(update.SQL, "WHERE id = CAST($1 AS uuid) AND status <> 'deleting' AND version = CAST($2 AS bigint)") || strings.Contains(update.SQL, "version + 1") {
		t.Errorf("the hold at a version is not the update with the version predicate and no advance:\n%s", update.SQL)
	}
	if !slices.Equal(update.Args, []any{"F", int64(4)}) {
		t.Errorf("the hold at a version bound %v, want the id and the version", update.Args)
	}

	rec, err = hold(t, []sqltest.Response{{Affected: 0}, noFile()})
	if !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("Hold of a missing row = %v, want ErrNotFound", err)
	}
	if got := ops(rec); got != "begin exec query rollback" {
		t.Errorf("ops = %q, want the exec, one read, and the rollback", got)
	}
	if read := rec.Calls()[2]; !strings.HasPrefix(read.SQL, "SELECT f.id, f.directory_id") || !slices.Equal(read.Args, []any{"F"}) {
		t.Errorf("the read is %s with %v, want file_by_id bound to the id", read.SQL, read.Args)
	}

	_, err = hold(t, []sqltest.Response{{Affected: 0}, fileResponse("F", "a.txt", blobfs.StatusDeleting, 3)})
	if !errors.Is(err, blobfs.ErrDeleting) || !strings.Contains(err.Error(), "the row is deleting") {
		t.Errorf("Hold of a deleting row = %v, want ErrDeleting", err)
	}
	_, err = hold(t, []sqltest.Response{{Affected: 0}, fileResponse("F", "a.txt", blobfs.StatusDeleting, 3)}, data.AtVersion(2))
	if !errors.Is(err, blobfs.ErrDeleting) || errors.Is(err, query.ErrVersionMismatch) {
		t.Errorf("Hold of a deleting row at another version = %v, want ErrDeleting and no version mismatch", err)
	}
	_, err = hold(t, []sqltest.Response{{Affected: 0}, fileResponse("F", "a.txt", blobfs.StatusAvailable, 3)}, data.AtVersion(2))
	if !errors.Is(err, query.ErrVersionMismatch) || errors.Is(err, blobfs.ErrDeleting) || !strings.Contains(err.Error(), "expected 2, current 3") {
		t.Errorf("Hold at a stale version = %v, want ErrVersionMismatch naming both versions", err)
	}
	_, err = hold(t, []sqltest.Response{{Affected: 0}, fileResponse("F", "a.txt", blobfs.StatusAvailable, 2)}, data.AtVersion(2))
	if err == nil || errors.Is(err, query.ErrVersionMismatch) || errors.Is(err, blobfs.ErrDeleting) || errors.Is(err, blobfs.ErrNotFound) || !strings.Contains(err.Error(), "available at version 2") {
		t.Errorf("Hold that matched nothing over an available row at the version = %v, want an error naming the row's state", err)
	}
}

// TestDeleteFile proves the first step of a file delete in both forms,
// inside the caller's transaction: the update moves the row to deleting
// and advances its version only when it is not deleting already, bound to
// the id, and returns the row with its key. A row already deleting is
// returned as it is, with no error and no version change, so a retry
// converges. A row that does not exist is ErrNotFound.
func TestDeleteFile(t *testing.T) {
	ctx := context.Background()
	for _, f := range forms {
		t.Run(f.name, func(t *testing.T) {
			del := func(responses ...sqltest.Response) (blobfs.File, string, error) {
				s, db, rec := openStore(t, f, responses...)
				tx := begin(t, db)
				file, err := s.Files.Delete(ctx, tx, "F")
				return file, ops(rec), err
			}

			file, got, err := del(changedFile(f, fileResponse("F", "a.txt", blobfs.StatusDeleting, 2))...)
			if err != nil || file.Status != blobfs.StatusDeleting || file.Version != 2 || file.Key != "F/a.txt" {
				t.Fatalf("Delete = %+v, %v, want the deleting row with its key", file, err)
			}
			want := "begin query"
			if !f.single {
				want = "begin exec query"
			}
			if got != want {
				t.Errorf("ops = %q, want %q", got, want)
			}

			s, db, rec := openStore(t, f, changedFile(f, fileResponse("F", "a.txt", blobfs.StatusDeleting, 2))...)
			if _, err := s.Files.Delete(ctx, begin(t, db), "F"); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			update := callsTo(rec, "UPDATE blobfs_file")[0]
			command, _, _ := strings.Cut(update.SQL, "\nRETURNING")
			if command != "UPDATE blobfs_file\nSET status = 'deleting', version = version + 1, updated_at = CURRENT_TIMESTAMP\nWHERE id = CAST($1 AS uuid) AND status <> 'deleting'" ||
				strings.Contains(update.SQL, "RETURNING") != f.single {
				t.Errorf("the update is %q", update.SQL)
			}
			if !slices.Equal(update.Args, []any{"F"}) {
				t.Errorf("the update bound %v, want the id alone", update.Args)
			}

			file, got, err = del(unchangedFile(f, fileResponse("F", "a.txt", blobfs.StatusDeleting, 2))...)
			if err != nil || file.Status != blobfs.StatusDeleting || file.Version != 2 {
				t.Errorf("Delete of a row already deleting = %+v, %v, want the row as it is", file, err)
			}
			want = "begin query query"
			if !f.single {
				want = "begin exec query"
			}
			if got != want {
				t.Errorf("ops = %q, want %q", got, want)
			}

			if _, _, err := del(unchangedFile(f, noFile())...); !errors.Is(err, blobfs.ErrNotFound) {
				t.Errorf("Delete of a missing row = %v, want ErrNotFound", err)
			}
		})
	}
}

// TestPurgeFile proves the last step of a file delete against the script:
// one exec of purge_file bound to the id removes a deleting row and nothing
// else runs; when it removes nothing the row is read once, and a row that
// is gone is success, a row that is not deleting is ErrNotDeleting naming
// its status with nothing changed, and a row that became deleting in
// between has the removal repeated once. A foreign key blobfs does not own
// classifies as ErrReferenced with the sqlate.ConstraintError reachable.
func TestPurgeFile(t *testing.T) {
	ctx := context.Background()
	t.Run("RemovesTheDeletingRow", func(t *testing.T) {
		s, db, rec := openStore(t, fallback, sqltest.Response{Affected: 1})
		if err := s.Files.Purge(ctx, db, "F"); err != nil {
			t.Fatalf("Purge: %v", err)
		}
		if got := rec.SQL(sqltest.OpExec); len(got) != 1 || got[0] != "DELETE FROM blobfs_file\nWHERE id = CAST($1 AS uuid) AND status = 'deleting'" {
			t.Errorf("execs = %q, want the removal of a deleting row", got)
		}
		if calls := rec.Calls(); len(calls) != 1 || !slices.Equal(calls[0].Args, []any{"F"}) {
			t.Errorf("calls = %+v, want one exec bound to the id", calls)
		}
	})
	t.Run("GoneIsSuccess", func(t *testing.T) {
		s, db, rec := openStore(t, fallback, sqltest.Response{Affected: 0}, noFile())
		if err := s.Files.Purge(ctx, db, "F"); err != nil {
			t.Fatalf("Purge of a gone row = %v, want success", err)
		}
		if got := ops(rec); got != "exec query" {
			t.Errorf("ops = %q, want the removal and one read", got)
		}
	})
	t.Run("NotDeletingIsRefused", func(t *testing.T) {
		for _, status := range []blobfs.Status{blobfs.StatusAvailable, blobfs.StatusPending} {
			s, db, rec := openStore(t, fallback, sqltest.Response{Affected: 0}, fileResponse("F", "a.txt", status, 1))
			err := s.Files.Purge(ctx, db, "F")
			if !errors.Is(err, blobfs.ErrNotDeleting) || !strings.Contains(err.Error(), "the row is "+string(status)) {
				t.Errorf("Purge of a %s row = %v, want ErrNotDeleting naming the status", status, err)
			}
			if got := ops(rec); got != "exec query" {
				t.Errorf("ops = %q, want the removal and one read and nothing more", got)
			}
		}
	})
	t.Run("BecameDeletingIsRepeatedOnce", func(t *testing.T) {
		s, db, rec := openStore(t, fallback, sqltest.Response{Affected: 0}, fileResponse("F", "a.txt", blobfs.StatusDeleting, 2), sqltest.Response{Affected: 1})
		if err := s.Files.Purge(ctx, db, "F"); err != nil {
			t.Fatalf("Purge = %v, want success from the repeated removal", err)
		}
		if got := ops(rec); got != "exec query exec" {
			t.Errorf("ops = %q, want the removal, the read, and the removal again", got)
		}
	})
	t.Run("ReferencedByAConsumer", func(t *testing.T) {
		s, db, _ := openStore(t, fallback, violation("fk_bookmark_file", sqlate.ErrForeignKeyViolation))
		err := s.Files.Purge(ctx, db, "F")
		var ce *sqlate.ConstraintError
		if !errors.Is(err, blobfs.ErrReferenced) || !errors.As(err, &ce) || ce.Constraint != "fk_bookmark_file" {
			t.Errorf("Purge under a consumer's key = %v, want ErrReferenced with the constraint reachable", err)
		}
		if errors.Is(err, blobfs.ErrNotEmpty) || errors.Is(err, blobfs.ErrNotFound) {
			t.Errorf("a consumer's key classified as blobfs's own: %v", err)
		}
		if want := "data: purge file F: blobfs: the row is referenced by a consumer's row (constraint fk_bookmark_file)"; err.Error() != want {
			t.Errorf("message = %q, want %q", err, want)
		}
	})
}
