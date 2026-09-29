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
	"github.com/standards-lab/blobfs/data/datatest"
)

// within scripts the cycle check's count.
func within(n int64) sqltest.Response {
	return sqltest.Response{Columns: []string{"matches"}, Rows: [][]driver.Value{{n}}}
}

// TestMoveRefusesBeforeSQL checks the root and an invalid name are refused
// before any statement.
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

// TestMoveIsThreeStepsUnderOneLock checks the move's order in both forms:
// the lock, the cycle check, then the guarded update; a cycle stops it.
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
				strings.Count(update.SQL, "status = 'active'") != 3 || !strings.Contains(update.SQL, "h.status = 'deleting'") ||
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

// TestMoveClassifies checks each refusal of the guarded update in both
// forms, from the row its read returns and the reads of the two parents.
func TestMoveClassifies(t *testing.T) {
	ctx := context.Background()
	for _, f := range forms {
		t.Run(f.name, func(t *testing.T) {
			// unchanged scripts the update that changed no row, in the
			// form's own shape, and then the read of the row as it is.
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
				// Deleting outranks the stale version.
				err := move(unchanged(directoryIn("D", "S", "d", blobfs.DirectoryStatusDeleting, version))...)
				if !errors.Is(err, blobfs.ErrDeleting) || errors.Is(err, query.ErrVersionMismatch) {
					t.Errorf("Move of a deleting directory at version %d = %v, want ErrDeleting", version, err)
				}
				wantDeleting(t, "Move of a deleting directory", err, true, "D")
				err = move(append(unchanged(directoryResponse("D", "S", "d", version)), directoryIn("S", blobfs.RootID, "s", blobfs.DirectoryStatusDeleting, 2))...)
				if !errors.Is(err, blobfs.ErrDeleting) || !strings.Contains(err.Error(), "the directory S is deleting") {
					t.Errorf("Move out of a deleting parent at version %d = %v, want ErrDeleting", version, err)
				}
				wantDeleting(t, "Move out of a deleting parent", err, true, "S")
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
			// unexplained scripts an update the reads of the row, the
			// parents, and the name's holder do not explain.
			unexplained := append(append(unchanged(directoryResponse("D", blobfs.RootID, "d", 1)), active...), noDirectory())
			err = move(append(slices.Clone(unexplained), unexplained...)...)
			if err == nil || errors.Is(err, blobfs.ErrDeleting) || errors.Is(err, query.ErrVersionMismatch) || !strings.Contains(err.Error(), "active at version 1") {
				t.Errorf("Move refused with every row active, twice = %v, want an error naming the row's state", err)
			}
			// A deleting directory that holds the name under the new parent
			// refuses the move by its own delete, not as the taken name, and
			// is marked as the name's holder, apart from the moved row's own.
			holder := append(append(unchanged(directoryResponse("D", blobfs.RootID, "e", 1)), active...), directoryIn("H", "P", "d", blobfs.DirectoryStatusDeleting, 2))
			err = move(holder...)
			if errors.Is(err, blobfs.ErrNameTaken) || !strings.Contains(err.Error(), "the directory H holds the name: ") {
				t.Errorf("Move onto a deleting holder's name = %v, want the holder marked and no ErrNameTaken", err)
			}
			wantDeleting(t, "Move onto a deleting holder's name", err, true, "H")
			// An update the reads do not explain, its holder purged or its
			// name taken by a live row since the update's snapshot, runs once
			// more: the rerun moves the row, meets the constraint, or selects
			// nothing again with a holder the read finds.
			changed := []sqltest.Response{directoryResponse("D", "P", "d", 2)}
			if !f.single {
				changed = []sqltest.Response{{Affected: 1}, directoryResponse("D", "P", "d", 2)}
			}
			s, db, rec := openStore(t, f, append(append([]sqltest.Response{within(0)}, unexplained...), changed...)...)
			if dir, err := s.Directories.Move(ctx, begin(t, db), "D", "P", "d", 1); err != nil || dir.Version != 2 {
				t.Errorf("Move whose rerun changed the row = %+v, %v, want the row", dir, err)
			}
			if updates := callsTo(rec, "UPDATE blobfs_directory"); len(updates) != 2 {
				t.Errorf("Move ran %d updates, want the update and its rerun", len(updates))
			}
			wantDone(t, rec)
			err = move(append(slices.Clone(unexplained), violation(blobfs.ConstraintUniqueDirectoryParentName, sqlate.ErrUniqueViolation))...)
			if !errors.Is(err, blobfs.ErrNameTaken) || errors.Is(err, blobfs.ErrDeleting) {
				t.Errorf("Move whose rerun met a live holder = %v, want ErrNameTaken", err)
			}
			err = move(append(slices.Clone(unexplained), holder...)...)
			wantDeleting(t, "Move whose rerun met a deleting holder", err, true, "H")
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

// TestIsWithin checks the count's reading and the walk's bindings.
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

// TestMarkDeleting checks the mark's statements, their bindings and
// counts, and its refusals.
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
	for i, c := range execs {
		if want := [][]any{{"D", nil}, {"D"}}[i]; !slices.Equal(c.Args, want) || !strings.Contains(c.SQL, "WITH RECURSIVE branch") || !strings.Contains(c.SQL, "status <> 'deleting'") {
			t.Errorf("the mark ran %q with %v, want the walk of the branch bound to %v", c.SQL, c.Args, want)
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

	// The version guards the directories' update in its anchor; a
	// directory at another version is told apart by the read, and the
	// files' update does not run.
	s, db, rec = openStore(t, fallback, sqltest.Response{Affected: 0}, directoryIn("D", blobfs.RootID, "d", blobfs.DirectoryStatusActive, 3))
	if _, err := s.Directories.MarkDeleting(ctx, begin(t, db), "D", data.AtVersion(2)); !errors.Is(err, query.ErrVersionMismatch) {
		t.Errorf("MarkDeleting at a stale version = %v, want ErrVersionMismatch", err)
	}
	if got := ops(rec); got != "begin exec query" {
		t.Errorf("ops = %q, want the guarded update and the read, and no files' update", got)
	}
	if c := rec.Calls()[1]; !slices.Equal(c.Args, []any{"D", int64(2)}) || !strings.Contains(c.SQL, "d.version = CAST($2 AS bigint) OR d.status = 'deleting'") {
		t.Errorf("the guarded mark ran %q with %v, want the version in the walk's anchor", c.SQL, c.Args)
	}

	// A branch deleting already is the mark's retry, whatever the version.
	s, db, _ = openStore(t, fallback, sqltest.Response{Affected: 0}, directoryIn("D", blobfs.RootID, "d", blobfs.DirectoryStatusDeleting, 3), sqltest.Response{Affected: 0})
	if marked, err := s.Directories.MarkDeleting(ctx, begin(t, db), "D", data.AtVersion(1)); err != nil || marked != (data.Marked{}) {
		t.Errorf("MarkDeleting's retry at an old version = %+v, %v, want nothing marked", marked, err)
	}
}

// TestDeleting checks the read of the branch roots and its limit.
func TestDeleting(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback,
		datatest.DirectoryRows(
			directoryRow("A", blobfs.RootID, "a", blobfs.DirectoryStatusDeleting, 2),
			directoryRow("B", "P", "b", blobfs.DirectoryStatusDeleting, 3)),
		noDirectory(),
	)
	roots, err := s.Directories.Deleting(ctx, db, 10)
	if err != nil || len(roots) != 2 || roots[0].ID != "A" || roots[1].ID != "B" || roots[1].Status != blobfs.DirectoryStatusDeleting {
		t.Fatalf("Deleting = %+v, %v, want A and B, deleting", roots, err)
	}
	none, err := s.Directories.Deleting(ctx, db, 1)
	if err != nil || len(none) != 0 {
		t.Errorf("Deleting with no branch being deleted = %+v, %v, want none", none, err)
	}
	calls := queries(rec)
	wantWhere := "JOIN blobfs_directory p ON p.id = d.parent_id\nWHERE d.status = 'deleting' AND p.status = 'active'\nORDER BY d.id\n OFFSET $1 ROWS FETCH NEXT $2 ROWS ONLY"
	if len(calls) != 2 || !strings.HasSuffix(calls[0].SQL, wantWhere) || !slices.Equal(calls[0].Args, []any{0, 10}) || !slices.Equal(calls[1].Args, []any{0, 1}) {
		t.Errorf("Deleting ran %v, want the roots' read %q from offset 0 to each limit", calls, wantWhere)
	}
	for _, limit := range []int{0, -1} {
		if _, err := s.Directories.Deleting(ctx, db, limit); err == nil {
			t.Errorf("Deleting(%d) = nil, want a refusal", limit)
		}
	}
	if n := len(rec.Calls()); n != 2 {
		t.Errorf("the refused limits ran SQL: %d calls", n)
	}
}
