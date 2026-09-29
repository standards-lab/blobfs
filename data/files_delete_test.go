package data_test

import (
	"context"
	"database/sql"
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

// TestHoldFile checks the hold's statement and bindings, with and without
// AtVersion, and each refusal classified from the read that follows.
func TestHoldFile(t *testing.T) {
	ctx := context.Background()
	// hold runs one hold of F inside a transaction over the scripted
	// responses and returns its error and the recorder.
	hold := func(t *testing.T, responses []sqltest.Response, opts ...data.VersionOption) (*sqltest.Recorder, error) {
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
	if update.SQL != "UPDATE blobfs_file\nSET updated_at = updated_at\nWHERE id = CAST($1 AS uuid) AND status <> 'deleting'\n  AND (CAST($2 AS bigint) IS NULL OR version = CAST($2 AS bigint))" {
		t.Errorf("the hold is not the self-assigning update:\n%s", update.SQL)
	}
	if !slices.Equal(update.Args, []any{"F", nil}) {
		t.Errorf("the hold bound %v, want the id and no version", update.Args)
	}

	rec, err = hold(t, []sqltest.Response{{Affected: 1}}, data.AtVersion(4))
	if err != nil {
		t.Fatalf("Hold at a version: %v", err)
	}
	update = rec.Calls()[1]
	if !strings.HasSuffix(update.SQL, "WHERE id = CAST($1 AS uuid) AND status <> 'deleting'\n  AND (CAST($2 AS bigint) IS NULL OR version = CAST($2 AS bigint))") || strings.Contains(update.SQL, "version + 1") {
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

	// A deleting row is its own delete's refusal while its directory is
	// active, and the directory's once the directory is deleting.
	root := directoryResponse(blobfs.RootID, "", "/", 1)
	rec, err = hold(t, []sqltest.Response{{Affected: 0}, fileResponse("F", "a.txt", blobfs.StatusDeleting, 3), root})
	wantDeleting(t, "Hold of a deleting row", err, false, "F")
	if got := ops(rec); got != "begin exec query query rollback" {
		t.Errorf("ops = %q, want the exec, the row's read, its directory's, and the rollback", got)
	}
	_, err = hold(t, []sqltest.Response{{Affected: 0}, fileResponse("F", "a.txt", blobfs.StatusDeleting, 3), root}, data.AtVersion(2))
	wantDeleting(t, "Hold of a deleting row at another version", err, false, "F")
	if errors.Is(err, query.ErrVersionMismatch) {
		t.Errorf("Hold of a deleting row at another version = %v, want no version mismatch", err)
	}
	marked := directoryIn("S", blobfs.RootID, "s", blobfs.DirectoryStatusDeleting, 2)
	_, err = hold(t, []sqltest.Response{{Affected: 0}, fileIn("F", "S", "a.txt", blobfs.StatusDeleting, 3), marked})
	wantDeleting(t, "Hold of a file its branch's mark reached", err, true, "S")
	// A directory gone since the row was read, as a sweep of its branch
	// leaves it, is the directory's refusal, with no sql.ErrNoRows.
	_, err = hold(t, []sqltest.Response{{Affected: 0}, fileIn("F", "S", "a.txt", blobfs.StatusDeleting, 3), noDirectory()})
	wantDeleting(t, "Hold of a file whose directory is gone", err, true, "S")
	if errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Hold of a file whose directory is gone = %v, want no sql.ErrNoRows", err)
	}
	// A directory that cannot be read leaves the refusal untyped, the
	// read's error beside it.
	errRead := errors.New("the connection dropped")
	_, err = hold(t, []sqltest.Response{{Affected: 0}, fileResponse("F", "a.txt", blobfs.StatusDeleting, 3), {Err: errRead}})
	var de *blobfs.DeletingError
	if !errors.Is(err, blobfs.ErrDeleting) || !errors.Is(err, errRead) || errors.As(err, &de) {
		t.Errorf("Hold of a deleting row whose directory is unread = %v, want ErrDeleting and the read's error, untyped", err)
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

// TestDeleteFile checks the delete's first step in both forms, and a
// retry of a deleting row.
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
			if command != "UPDATE blobfs_file\nSET status = 'deleting', version = version + 1, updated_at = CURRENT_TIMESTAMP\nWHERE id = CAST($1 AS uuid) AND status <> 'deleting'\n  AND (CAST($2 AS bigint) IS NULL OR version = CAST($2 AS bigint))" ||
				strings.Contains(update.SQL, "RETURNING") != f.single {
				t.Errorf("the update is %q", update.SQL)
			}
			if !slices.Equal(update.Args, []any{"F", nil}) {
				t.Errorf("the update bound %v, want the id and no version", update.Args)
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

// TestPurgeFile checks the purge, the read after a removal of nothing,
// and each refusal.
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
