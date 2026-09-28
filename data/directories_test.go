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

// TestFind checks Find is one read by id, and no row is ErrNotFound.
func TestFind(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback, directoryResponse(blobfs.RootID, "", "/", 1), noDirectory())
	root, err := s.Directories.Find(ctx, db, blobfs.RootID)
	if err != nil || !root.IsRoot() || root.Name != "/" {
		t.Fatalf("Find(root) = %+v, %v, want the root", root, err)
	}
	if _, err := s.Directories.Find(ctx, db, blobfs.RootID); !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("Find without the seed = %v, want ErrNotFound", err)
	}
	calls := rec.Calls()
	if len(calls) != 2 || !slices.Equal(calls[0].Args, []any{blobfs.RootID}) || !strings.HasSuffix(calls[0].SQL, "WHERE d.id = CAST($1 AS uuid)") {
		t.Errorf("Find ran %v, want one read by id per call", calls)
	}
}

// TestFindByName checks the normalized lookup and its refusals.
func TestFindByName(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback, childResponse("D", nfcName), noDirectory())
	d, err := s.Directories.FindByName(ctx, db, blobfs.RootID, nfdName)
	if err != nil || d.ID != "D" {
		t.Fatalf("FindByName = %+v, %v, want the child", d, err)
	}
	if _, err := s.Directories.FindByName(ctx, db, blobfs.RootID, "missing"); !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("FindByName of a free name = %v, want ErrNotFound", err)
	}
	for _, name := range []string{"", "a/b", ".."} {
		if _, err := s.Directories.FindByName(ctx, db, blobfs.RootID, name); !errors.Is(err, blobfs.ErrInvalidName) {
			t.Errorf("FindByName(%q) = %v, want ErrInvalidName", name, err)
		}
	}
	calls := rec.Calls()
	if len(calls) != 2 || !slices.Equal(calls[0].Args, []any{blobfs.RootID, nfcName}) ||
		!strings.HasSuffix(calls[0].SQL, "WHERE d.parent_id = CAST($1 AS uuid) AND d.name = $2") {
		t.Errorf("FindByName ran %v, want two lookups bound to the parent and the normalized name", calls)
	}
}

// TestCreateForms checks Create's statements in the fallback, on the pool
// and in a transaction, and in the single-statement form.
func TestCreateForms(t *testing.T) {
	ctx := context.Background()
	id := blobfs.NewID()
	cases := []struct {
		form form
		inTx bool
		ops  string
	}{
		{forms[0], false, "begin exec query commit"},
		{forms[0], true, "begin exec query"},
		{forms[1], false, "query"},
		{forms[1], true, "begin query"},
	}
	for _, c := range cases {
		s, db, rec := openStore(t, c.form)
		if !c.form.single {
			rec.Queue(sqltest.Response{Affected: 1})
		}
		rec.Queue(childResponse(id, nfcName))
		var sess sqlate.Session = db
		if c.inTx {
			sess = begin(t, db)
		}
		d, err := s.Directories.Create(ctx, sess, blobfs.RootID, nfdName, data.WithID(id))
		if err != nil || d.ID != id || d.Name != nfcName || d.Version != 1 {
			t.Fatalf("%s (in tx %v): Create = %+v, %v, want the row", c.form.name, c.inTx, d, err)
		}
		if got := ops(rec); got != c.ops {
			t.Errorf("%s (in tx %v): ops = %q, want %q", c.form.name, c.inTx, got, c.ops)
		}
		var insert sqltest.Call
		for _, call := range rec.Calls() {
			if strings.HasPrefix(call.SQL, "INSERT INTO blobfs_directory (id, parent_id, name)") {
				insert = call
			}
		}
		if !slices.Equal(insert.Args, []any{id, nfcName, blobfs.RootID}) || !strings.Contains(insert.SQL, "p.status = 'active'") {
			t.Errorf("%s: the insert bound %v, want the id, the normalized name, and the parent it selects from:\n%s", c.form.name, insert.Args, insert.SQL)
		}
		if strings.Contains(insert.SQL, "RETURNING") != c.form.single {
			t.Errorf("%s: the insert is not the form's:\n%s", c.form.name, insert.SQL)
		}
	}
}

// TestCreateRefusals checks the refusals before any SQL and the
// classification of blobfs's constraints in both forms.
func TestCreateRefusals(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback)
	for _, name := range []string{"", "a/b", "tab\there", ".."} {
		if _, err := s.Directories.Create(ctx, db, blobfs.RootID, name); !errors.Is(err, blobfs.ErrInvalidName) {
			t.Errorf("Create(%q) = %v, want ErrInvalidName", name, err)
		}
	}
	for _, id := range []string{blobfs.RootID, "", "not-a-uuid"} {
		if _, err := s.Directories.Create(ctx, db, blobfs.RootID, "docs", data.WithID(id)); !errors.Is(err, blobfs.ErrInvalidID) {
			t.Errorf("Create with the id %q = %v, want ErrInvalidID", id, err)
		}
	}
	if calls := rec.Calls(); len(calls) != 0 {
		t.Errorf("the refusals reached the driver with %v", calls)
	}

	for _, f := range forms {
		for _, c := range []struct {
			constraint string
			class      error
			want       error
		}{
			{blobfs.ConstraintUniqueDirectoryParentName, sqlate.ErrUniqueViolation, blobfs.ErrNameTaken},
			{blobfs.ConstraintForeignKeyDirectoryParent, sqlate.ErrForeignKeyViolation, blobfs.ErrNotFound},
			{blobfs.ConstraintPrimaryKeyDirectory, sqlate.ErrUniqueViolation, blobfs.ErrIDTaken},
		} {
			s, db, rec := openStore(t, f, violation(c.constraint, c.class))
			_, err := s.Directories.Create(ctx, db, blobfs.RootID, "docs")
			var ve *blobfs.ViolationError
			if !errors.Is(err, c.want) || !errors.As(err, &ve) || ve.Constraint != c.constraint {
				t.Errorf("%s: Create under %s = %v, want %v", f.name, c.constraint, err, c.want)
			}
			if !strings.HasSuffix(err.Error(), c.want.Error()+" (constraint "+c.constraint+")") || strings.Contains(err.Error(), "duplicate key") {
				t.Errorf("%s: Create under %s = %q, want the sentinel and the constraint named and the driver's text hidden", f.name, c.constraint, err)
			}
			want := "query"
			if !f.single {
				want = "begin exec rollback"
			}
			if got := ops(rec); got != want {
				t.Errorf("%s: ops = %q, want %q", f.name, got, want)
			}
		}
	}
}

// TestCreateUnderAClosedParent checks an insert that selected no row reads
// the parent to classify the refusal, in both forms.
func TestCreateUnderAClosedParent(t *testing.T) {
	ctx := context.Background()
	for _, f := range forms {
		unchanged := []sqltest.Response{noDirectory(), noDirectory()}
		if !f.single {
			unchanged = []sqltest.Response{{Affected: 0}, noDirectory()}
		}
		for _, c := range []struct {
			parent sqltest.Response
			want   error
		}{
			{noDirectory(), blobfs.ErrNotFound},
			{directoryIn("P", blobfs.RootID, "p", blobfs.DirectoryStatusDeleting, 2), blobfs.ErrDeleting},
		} {
			s, db, rec := openStore(t, f, append(unchanged, c.parent)...)
			_, err := s.Directories.Create(ctx, db, "P", "docs")
			var ve *blobfs.ViolationError
			if !errors.Is(err, c.want) || errors.As(err, &ve) {
				t.Errorf("%s: Create under a parent the insert did not select = %v, want %v", f.name, err, c.want)
			}
			calls := rec.Calls()
			if last := calls[len(calls)-1]; !strings.HasPrefix(last.SQL, "SELECT d.id") || !slices.Equal(last.Args, []any{"P"}) {
				t.Errorf("%s: the last call is %q with %v, want the read of the parent", f.name, last.SQL, last.Args)
			}
		}
	}
	s, db, _ := openStore(t, fallback, sqltest.Response{Affected: 0}, childResponse("X", "other"),
		directoryIn("P", blobfs.RootID, "p", blobfs.DirectoryStatusDeleting, 2))
	if _, err := s.Directories.Create(ctx, db, "P", "docs", data.WithID(blobfs.NewID())); !errors.Is(err, blobfs.ErrDeleting) {
		t.Errorf("Create whose read found another row = %v, want ErrDeleting", err)
	}
}

// TestWithID proves the supplied id reaches the insert in canonical form,
// whatever form the caller wrote.
func TestWithID(t *testing.T) {
	ctx := context.Background()
	id := blobfs.NewID()
	s, db, rec := openStore(t, forms[1], childResponse(id, "docs"))
	if _, err := s.Directories.Create(ctx, db, blobfs.RootID, "docs", data.WithID("{"+strings.ToUpper(id)+"}")); err != nil {
		t.Fatalf("Create with a braced upper-case id: %v", err)
	}
	if got := rec.Calls()[0].Args[0]; got != id {
		t.Errorf("the insert bound %v, want the canonical form %q", got, id)
	}
}

// TestEnsure checks the insert-or-find in both forms: found, created, the
// race recovered on the pool, and returned inside a transaction.
func TestEnsure(t *testing.T) {
	ctx := context.Background()
	for _, f := range forms {
		t.Run(f.name, func(t *testing.T) {
			// insertOps is what the insert runs on the pool in this form.
			insertOps, refusedOps := "query", "query"
			if !f.single {
				insertOps, refusedOps = "begin exec query commit", "begin exec rollback"
			}

			s, db, rec := openStore(t, f, childResponse("D", "docs"))
			d, created, err := s.Directories.Ensure(ctx, db, blobfs.RootID, "docs")
			if err != nil || created || d.ID != "D" {
				t.Fatalf("Ensure of a held name = %+v, %v, %v; want the row found", d, created, err)
			}
			if calls := rec.Calls(); len(calls) != 1 || !slices.Equal(calls[0].Args, []any{blobfs.RootID, "docs"}) {
				t.Errorf("calls = %v, want one lookup bound to the parent and the name", calls)
			}

			s, db, rec = openStore(t, f, noDirectory())
			if !f.single {
				rec.Queue(sqltest.Response{Affected: 1})
			}
			rec.Queue(childResponse("N", "docs"))
			d, created, err = s.Directories.Ensure(ctx, db, blobfs.RootID, "docs")
			if err != nil || !created || d.ID != "N" {
				t.Fatalf("Ensure of a free name = %+v, %v, %v; want the row created", d, created, err)
			}
			if got, want := ops(rec), "query "+insertOps; got != want {
				t.Errorf("ops = %q, want %q", got, want)
			}

			s, db, rec = openStore(t, f, noDirectory(), violation(blobfs.ConstraintUniqueDirectoryParentName, sqlate.ErrUniqueViolation), childResponse("C", "docs"))
			d, created, err = s.Directories.Ensure(ctx, db, blobfs.RootID, "docs")
			if err != nil || created || d.ID != "C" {
				t.Fatalf("Ensure under a concurrent creator = %+v, %v, %v; want the creator's row found", d, created, err)
			}
			if got, want := ops(rec), "query "+refusedOps+" query"; got != want {
				t.Errorf("ops = %q, want %q", got, want)
			}

			s, db, rec = openStore(t, f, noDirectory(), violation(blobfs.ConstraintUniqueDirectoryParentName, sqlate.ErrUniqueViolation))
			_, err = db.Transact(ctx, func(tx *sqlate.Tx) (blobfs.Directory, error) {
				d, _, err := s.Directories.Ensure(ctx, tx, blobfs.RootID, "docs")
				return d, err
			})
			if !errors.Is(err, blobfs.ErrNameTaken) {
				t.Errorf("Ensure under a concurrent creator inside a transaction = %v, want ErrNameTaken", err)
			}
			want := "begin query query rollback"
			if !f.single {
				want = "begin query exec rollback"
			}
			if got := ops(rec); got != want {
				t.Errorf("ops = %q, want %q: no lookup after the refused insert inside the transaction", got, want)
			}

			s, db, rec = openStore(t, f, noDirectory(), violation(blobfs.ConstraintPrimaryKeyDirectory, sqlate.ErrUniqueViolation))
			_, _, err = s.Directories.Ensure(ctx, db, blobfs.RootID, "docs", data.WithID(blobfs.NewID()))
			if !errors.Is(err, blobfs.ErrIDTaken) || errors.Is(err, blobfs.ErrNameTaken) {
				t.Errorf("Ensure under a taken id = %v, want ErrIDTaken", err)
			}
			if got, want := ops(rec), "query "+refusedOps; got != want {
				t.Errorf("ops = %q, want %q: no recovery lookup after a violation that is not the name's", got, want)
			}
		})
	}
}

// TestEnsureFindsADeletingDirectory checks a found deleting directory is
// ErrDeleting, with no insert.
func TestEnsureFindsADeletingDirectory(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback, directoryIn("D", blobfs.RootID, "docs", blobfs.DirectoryStatusDeleting, 2))
	if _, _, err := s.Directories.Ensure(ctx, db, blobfs.RootID, "docs"); !errors.Is(err, blobfs.ErrDeleting) {
		t.Errorf("Ensure of a deleting directory = %v, want ErrDeleting", err)
	}
	if got := ops(rec); got != "query" {
		t.Errorf("ops = %q, want the one lookup", got)
	}
}

// TestEnsureRefusals proves the checks that run before any SQL: an invalid
// name, and a supplied id that is the nil UUID or no UUID.
func TestEnsureRefusals(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback)
	if _, _, err := s.Directories.Ensure(ctx, db, blobfs.RootID, "a/b"); !errors.Is(err, blobfs.ErrInvalidName) {
		t.Errorf("Ensure(a/b) = %v, want ErrInvalidName", err)
	}
	for _, id := range []string{blobfs.RootID, "", "not-a-uuid"} {
		if _, _, err := s.Directories.Ensure(ctx, db, blobfs.RootID, "docs", data.WithID(id)); !errors.Is(err, blobfs.ErrInvalidID) {
			t.Errorf("Ensure with the id %q = %v, want ErrInvalidID", id, err)
		}
	}
	if calls := rec.Calls(); len(calls) != 0 {
		t.Errorf("the refusals reached the driver with %v", calls)
	}
}

// TestDelete checks the directory removal and its refusals.
func TestDelete(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback)
	if err := s.Directories.Delete(ctx, db, blobfs.RootID); !errors.Is(err, blobfs.ErrRootDirectory) {
		t.Errorf("Delete(root) = %v, want ErrRootDirectory", err)
	}
	if calls := rec.Calls(); len(calls) != 0 {
		t.Errorf("the root's refusal reached the driver: %+v", calls)
	}

	s, db, rec = openStore(t, fallback, sqltest.Response{Affected: 1}, sqltest.Response{Affected: 0})
	if err := s.Directories.Delete(ctx, db, "D"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := rec.SQL(sqltest.OpExec); len(got) != 1 || !strings.HasPrefix(got[0], "DELETE FROM blobfs_directory") || !strings.Contains(got[0], "parent_id IS NOT NULL") {
		t.Errorf("execs = %q, want the removal that keeps the root", got)
	}
	if calls := rec.Calls(); !slices.Equal(calls[0].Args, []any{"D", nil}) {
		t.Errorf("the removal bound %v, want the id and no version", calls[0].Args)
	}
	if err := s.Directories.Delete(ctx, db, "D"); !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("Delete of a missing directory = %v, want ErrNotFound", err)
	}

	for _, c := range []struct {
		constraint string
		want       error
	}{
		{blobfs.ConstraintForeignKeyDirectoryParent, blobfs.ErrNotEmpty},
		{blobfs.ConstraintForeignKeyFileDirectory, blobfs.ErrNotEmpty},
		{"fk_directory_owner_directory", blobfs.ErrReferenced},
	} {
		s, db, _ := openStore(t, fallback, violation(c.constraint, sqlate.ErrForeignKeyViolation))
		err := s.Directories.Delete(ctx, db, "D")
		var ce *sqlate.ConstraintError
		if !errors.Is(err, c.want) || !errors.As(err, &ce) || ce.Constraint != c.constraint {
			t.Errorf("Delete under %s = %v, want %v with the constraint reachable", c.constraint, err, c.want)
		}
		if want := "data: delete directory D: " + c.want.Error() + " (constraint " + c.constraint + ")"; err.Error() != want {
			t.Errorf("Delete under %s = %q, want %q", c.constraint, err, want)
		}
	}
}

// TestCreateOnASessionThatCannotBegin checks the fallback needs a session
// that can begin a transaction, where the single-statement form does not.
func TestCreateOnASessionThatCannotBegin(t *testing.T) {
	ctx := context.Background()
	for _, f := range forms {
		s, db, rec := openStore(t, f, childResponse("N", "docs"))
		_, err := s.Directories.Create(ctx, plainSession{db}, blobfs.RootID, "docs")
		if f.single {
			if err != nil {
				t.Errorf("%s: Create on a plain session = %v, want the row", f.name, err)
			}
			continue
		}
		if !errors.Is(err, query.ErrTransactionRequired) {
			t.Errorf("%s: Create on a plain session = %v, want ErrTransactionRequired", f.name, err)
		}
		if calls := rec.Calls(); len(calls) != 0 {
			t.Errorf("%s: the refusal reached the driver with %v", f.name, calls)
		}
	}
}

// plainSession is a session that hides the pool's Begin, as a consumer's
// own wrapper might.
type plainSession struct{ sqlate.Session }
