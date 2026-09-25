package data_test

import (
	"context"
	"database/sql/driver"
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

// within scripts the cycle check's count.
func within(n int64) sqltest.Response {
	return sqltest.Response{Columns: []string{"matches"}, Rows: [][]driver.Value{{n}}}
}

// TestMoveRefusesBeforeSQL proves the refusals that happen before any
// statement runs: the root is ErrRootDirectory and an invalid name is
// ErrInvalidName, each with nothing reaching the driver but the
// transaction's begin.
func TestMoveRefusesBeforeSQL(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback)
	tx := begin(t, db)
	if _, err := s.Directories.Move(ctx, tx, blobfs.RootID, "P", "root", 1); !errors.Is(err, blobfs.ErrRootDirectory) {
		t.Errorf("Move(root) = %v, want ErrRootDirectory", err)
	}
	if _, err := s.Directories.Move(ctx, tx, "D", "P", "a/b", 1); !errors.Is(err, blobfs.ErrInvalidName) {
		t.Errorf("Move with a slash in the name = %v, want ErrInvalidName", err)
	}
	if got := ops(rec); got != "begin" {
		t.Errorf("the refusals reached the driver with %q", got)
	}
}

// TestMoveIsThreeStepsUnderOneLock proves the order of the move on the
// baseline, in both forms: in the caller's transaction, the lock (a no-op
// that runs no SQL), then the cycle check bound to the new parent and the
// moved directory, then the guarded update bound to the new parent, the
// normalized name, the id, and the expected version, which returns the row
// (with RETURNING in one statement, or followed by the read by id). A
// check that finds the moved directory above the new parent is ErrCycle,
// and the update never runs.
func TestMoveIsThreeStepsUnderOneLock(t *testing.T) {
	ctx := context.Background()
	for _, f := range forms {
		t.Run(f.name, func(t *testing.T) {
			s, db, rec := openStore(t, f, within(0))
			wantOps := "begin query query commit"
			if !f.single {
				rec.Queue(sqltest.Response{Affected: 1})
				wantOps = "begin query exec query commit"
			}
			rec.Queue(directoryResponse("D", "P", nfcName, 2))
			tx := begin(t, db)
			d, err := s.Directories.Move(ctx, tx, "D", "P", nfdName, 1)
			if err != nil {
				t.Fatalf("Move: %v", err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatalf("Commit: %v", err)
			}
			if d.ID != "D" || *d.ParentID != "P" || d.Name != nfcName || d.Version != 2 {
				t.Errorf("Move returned %+v, want the row after the update", d)
			}
			if got := ops(rec); got != wantOps {
				t.Errorf("ops = %q, want %q", got, wantOps)
			}
			calls := rec.Calls()
			if !strings.HasPrefix(calls[1].SQL, "WITH RECURSIVE up") || !slices.Equal(calls[1].Args, []any{"P", "D"}) {
				t.Errorf("the check ran %q with %v, want the upward walk from the new parent looking for the directory", calls[1].SQL, calls[1].Args)
			}
			update := calls[2]
			if !strings.HasPrefix(update.SQL, "UPDATE blobfs_directory") || !strings.Contains(update.SQL, "AND parent_id IS NOT NULL") ||
				strings.Count(update.SQL, "status = 'active'") != 3 ||
				strings.Contains(update.SQL, "RETURNING") != f.single {
				t.Errorf("the update is %q", update.SQL)
			}
			if !slices.Equal(update.Args, []any{"P", nfcName, "D", int64(1)}) {
				t.Errorf("the update bound %v, want the parent, the normalized name, the id, and the expected version", update.Args)
			}
		})
	}

	s, db, rec := openStore(t, fallback, within(1))
	tx := begin(t, db)
	if _, err := s.Directories.Move(ctx, tx, "D", "D", "d", 1); !errors.Is(err, blobfs.ErrCycle) {
		t.Errorf("Move into itself = %v, want ErrCycle", err)
	}
	if execs := rec.SQL(sqltest.OpExec); len(execs) != 0 {
		t.Errorf("the refused move ran %v", execs)
	}
}

// TestMoveClassifies proves the outcomes of the guarded update in both
// forms: no row at all is ErrNotFound; a row at another version is
// ErrVersionMismatch naming both versions; a root the update's own
// predicate refused is ErrRootDirectory; a deleting directory, or one whose
// current or new parent is deleting, is ErrDeleting at any version, the
// parents told by their reads; a missing new parent is ErrNotFound from
// those reads at the expected version, or through the foreign key once the
// predicate passed, and a taken name ErrNameTaken through the unique
// constraint, each with the constraint reachable.
func TestMoveClassifies(t *testing.T) {
	ctx := context.Background()
	for _, f := range forms {
		t.Run(f.name, func(t *testing.T) {
			// unchanged scripts the update that changed no row, in the form's
			// own shape, and then the read of the row as it is.
			unchanged := func(read sqltest.Response) []sqltest.Response {
				if f.single {
					return []sqltest.Response{noDirectory(), read}
				}
				return []sqltest.Response{{Affected: 0}, read}
			}
			move := func(responses ...sqltest.Response) error {
				s, db, _ := openStore(t, f, append([]sqltest.Response{within(0)}, responses...)...)
				_, err := s.Directories.Move(ctx, begin(t, db), "D", "P", "d", 1)
				return err
			}

			if err := move(unchanged(noDirectory())...); !errors.Is(err, blobfs.ErrNotFound) {
				t.Errorf("Move of a missing directory = %v, want ErrNotFound", err)
			}
			// parents scripts the reads of the current and the new parent.
			parents := func(from, to sqltest.Response) []sqltest.Response { return []sqltest.Response{from, to} }
			active := parents(directoryResponse(blobfs.RootID, "", "/", 1), directoryResponse("P", blobfs.RootID, "p", 1))
			err := move(append(unchanged(directoryResponse("D", blobfs.RootID, "d", 3)), active...)...)
			if !errors.Is(err, query.ErrVersionMismatch) || !strings.Contains(err.Error(), "expected 1, current 3") {
				t.Errorf("Move at a stale version = %v, want ErrVersionMismatch naming both versions", err)
			}
			if err := move(unchanged(directoryResponse("D", "", "/", 1))...); !errors.Is(err, blobfs.ErrRootDirectory) {
				t.Errorf("Move refused by the update's own predicate = %v, want ErrRootDirectory", err)
			}
			for _, version := range []int64{1, 3} {
				// A deleting branch outranks a stale version: the mark
				// advanced the version past the one the mover read.
				err := move(unchanged(directoryIn("D", "S", "d", blobfs.DirectoryStatusDeleting, version))...)
				if !errors.Is(err, blobfs.ErrDeleting) || errors.Is(err, query.ErrVersionMismatch) {
					t.Errorf("Move of a deleting directory at version %d = %v, want ErrDeleting", version, err)
				}
				err = move(append(unchanged(directoryResponse("D", "S", "d", version)), directoryIn("S", blobfs.RootID, "s", blobfs.DirectoryStatusDeleting, 2))...)
				if !errors.Is(err, blobfs.ErrDeleting) || !strings.Contains(err.Error(), "its directory S is deleting") {
					t.Errorf("Move out of a deleting parent at version %d = %v, want ErrDeleting", version, err)
				}
				err = move(append(unchanged(directoryResponse("D", "S", "d", version)),
					parents(directoryResponse("S", blobfs.RootID, "s", 1), directoryIn("P", blobfs.RootID, "p", blobfs.DirectoryStatusDeleting, 2))...)...)
				if !errors.Is(err, blobfs.ErrDeleting) || !strings.Contains(err.Error(), "the directory P is deleting") {
					t.Errorf("Move under a deleting parent at version %d = %v, want ErrDeleting", version, err)
				}
			}
			missing := parents(directoryResponse(blobfs.RootID, "", "/", 1), noDirectory())
			err = move(append(unchanged(directoryResponse("D", blobfs.RootID, "d", 1)), missing...)...)
			var ve *blobfs.ViolationError
			if !errors.Is(err, blobfs.ErrNotFound) || errors.As(err, &ve) {
				t.Errorf("Move under a missing parent = %v, want ErrNotFound", err)
			}
			err = move(append(unchanged(directoryResponse("D", blobfs.RootID, "d", 1)), active...)...)
			if err == nil || errors.Is(err, blobfs.ErrDeleting) || errors.Is(err, query.ErrVersionMismatch) || !strings.Contains(err.Error(), "active at version 1") {
				t.Errorf("Move refused with every row active = %v, want an error naming the row's state", err)
			}
			for _, c := range []struct {
				constraint string
				class      error
				want       error
			}{
				{blobfs.ConstraintForeignKeyDirectoryParent, sqlate.ErrForeignKeyViolation, blobfs.ErrNotFound},
				{blobfs.ConstraintUniqueDirectoryParentName, sqlate.ErrUniqueViolation, blobfs.ErrNameTaken},
			} {
				err := move(violation(c.constraint, c.class))
				var ce *sqlate.ConstraintError
				if !errors.Is(err, c.want) || !errors.As(err, &ce) || ce.Constraint != c.constraint {
					t.Errorf("Move under %s = %v, want %v with the constraint reachable", c.constraint, err, c.want)
				}
			}
		})
	}
}

// TestIsWithin proves the tree predicate: a count of zero is false and any
// other count true, bound to the start of the walk and the directory
// looked for, over a walk that combines its steps with UNION so that it
// terminates on a cycle.
func TestIsWithin(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback, within(0), within(2))
	for i, want := range []bool{false, true} {
		got, err := s.Directories.IsWithin(ctx, db, "P", "D")
		if err != nil || got != want {
			t.Errorf("IsWithin %d = %v, %v, want %v", i, got, err, want)
		}
	}
	if args := rec.Calls()[0].Args; !slices.Equal(args, []any{"P", "D"}) {
		t.Errorf("IsWithin bound %v, want the start of the walk and the directory looked for", args)
	}
	// The walk discards a row it has produced, so it terminates on a cycle.
	if text := rec.Calls()[0].SQL; !strings.Contains(text, "UNION\n") || strings.Contains(text, "UNION ALL") {
		t.Errorf("the walk does not combine its steps with UNION:\n%s", text)
	}
}

// TestMarkDeleting proves the mark of a branch in the caller's transaction:
// the root refused before any SQL; the tree lock (a no-op on the baseline),
// the directories' update, and the files' update, each bound to the id,
// with their counts reported; a mark that changed no directory reading the
// row to tell a missing directory, ErrNotFound with no files' update, from
// a branch marked already, which is no error.
func TestMarkDeleting(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback)
	if _, err := s.Directories.MarkDeleting(ctx, begin(t, db), blobfs.RootID); !errors.Is(err, blobfs.ErrRootDirectory) {
		t.Errorf("MarkDeleting(root) = %v, want ErrRootDirectory", err)
	}
	if got := ops(rec); got != "begin" {
		t.Errorf("the root's refusal reached the driver with %q", got)
	}

	s, db, rec = openStore(t, fallback, sqltest.Response{Affected: 3}, sqltest.Response{Affected: 5})
	marked, err := s.Directories.MarkDeleting(ctx, begin(t, db), "D")
	if err != nil || marked != (data.Marked{Directories: 3, Files: 5}) {
		t.Fatalf("MarkDeleting = %+v, %v, want 3 directories and 5 files", marked, err)
	}
	execs := rec.Calls()[1:]
	if len(execs) != 2 || !strings.HasPrefix(execs[0].SQL, "UPDATE blobfs_directory") || !strings.HasPrefix(execs[1].SQL, "UPDATE blobfs_file") {
		t.Fatalf("MarkDeleting ran %v, want the directories' update and then the files'", execs)
	}
	for _, c := range execs {
		if !slices.Equal(c.Args, []any{"D"}) || !strings.Contains(c.SQL, "WITH RECURSIVE branch") || !strings.Contains(c.SQL, "status <> 'deleting'") {
			t.Errorf("the mark ran %q with %v, want the walk of the branch bound to the id", c.SQL, c.Args)
		}
	}

	s, db, rec = openStore(t, fallback, sqltest.Response{Affected: 0}, noDirectory())
	if _, err := s.Directories.MarkDeleting(ctx, begin(t, db), "D"); !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("MarkDeleting of a missing directory = %v, want ErrNotFound", err)
	}
	if got := ops(rec); got != "begin exec query" {
		t.Errorf("ops = %q, want the directories' update and the read, and no files' update", got)
	}

	s, db, _ = openStore(t, fallback, sqltest.Response{Affected: 0}, directoryIn("D", blobfs.RootID, "d", blobfs.DirectoryStatusDeleting, 2), sqltest.Response{Affected: 1})
	marked, err = s.Directories.MarkDeleting(ctx, begin(t, db), "D")
	if err != nil || marked != (data.Marked{Files: 1}) {
		t.Errorf("MarkDeleting of a marked branch = %+v, %v, want no directory and the one straggling file", marked, err)
	}
}
