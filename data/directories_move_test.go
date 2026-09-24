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
// ErrVersionMismatch naming both versions; a row at the expected version
// the update's own predicate refused is the root, ErrRootDirectory; a
// missing new parent is ErrNotFound through the foreign key, and a taken
// name ErrNameTaken through the unique constraint, each with the
// constraint reachable.
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
			err := move(unchanged(directoryResponse("D", blobfs.RootID, "d", 3))...)
			if !errors.Is(err, query.ErrVersionMismatch) || !strings.Contains(err.Error(), "expected 1, current 3") {
				t.Errorf("Move at a stale version = %v, want ErrVersionMismatch naming both versions", err)
			}
			if err := move(unchanged(directoryResponse("D", "", "/", 1))...); !errors.Is(err, blobfs.ErrRootDirectory) {
				t.Errorf("Move refused by the update's own predicate = %v, want ErrRootDirectory", err)
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
