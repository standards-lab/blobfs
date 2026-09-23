package data_test

import (
	"context"
	"database/sql/driver"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"
	"github.com/standards-lab/sqlate/sqltest"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data"
)

// fileColumns is the column list of a file row, as the scripted driver must
// return it.
var fileColumns = []string{
	"id", "directory_id", "name", "status", "key", "size", "content_type", "etag", "version", "created_at", "updated_at",
}

// fileIn scripts one file row in a directory, with a status and a version;
// its key is the id and the name, as NewKey builds it.
func fileIn(id, directoryID, name string, status blobfs.Status, version int64) sqltest.Response {
	now := time.Now()
	row := []driver.Value{id, directoryID, name, string(status), id + "/" + name, nil, "text/plain", nil, version, now, now}
	return sqltest.Response{Columns: fileColumns, Rows: [][]driver.Value{row}}
}

// fileResponse scripts one file row in the root.
func fileResponse(id, name string, status blobfs.Status, version int64) sqltest.Response {
	return fileIn(id, blobfs.RootID, name, status, version)
}

// noFile scripts a file read, or a returning command's single-statement
// form, that yields no row.
func noFile() sqltest.Response { return sqltest.Response{Columns: fileColumns} }

// unchangedFile scripts a returning file command that changed no row, in
// the form's own shape, followed by the read of the row as it is.
func unchangedFile(f form, read sqltest.Response) []sqltest.Response {
	if f.single {
		return []sqltest.Response{noFile(), read}
	}
	return []sqltest.Response{{Affected: 0}, read}
}

// changedFile scripts a returning file command that changed its row, in
// the form's own shape: the row with RETURNING, or the command's one row
// affected and then the read.
func changedFile(f form, row sqltest.Response) []sqltest.Response {
	if f.single {
		return []sqltest.Response{row}
	}
	return []sqltest.Response{{Affected: 1}, row}
}

// accepting is a KeyValidator that accepts every key.
type accepting struct{}

func (accepting) ValidateKey(string) error { return nil }

// refusing is a KeyValidator that refuses every key with a fixed error.
type refusing struct{ err error }

func (r refusing) ValidateKey(string) error { return r.err }

// runeLimit is a KeyValidator that refuses a key longer than its value in
// runes, as the providers blobfs targets count.
type runeLimit int

func (l runeLimit) ValidateKey(key string) error {
	if n := utf8.RuneCountInString(key); n > int(l) {
		return errors.New("over the limit")
	}
	return nil
}

// callsTo returns the calls whose text starts with prefix.
func callsTo(rec *sqltest.Recorder, prefix string) []sqltest.Call {
	var out []sqltest.Call
	for _, c := range rec.Calls() {
		if strings.HasPrefix(c.SQL, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// TestFindFile proves Find is one read by id, and a missing row is
// ErrNotFound.
func TestFindFile(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback, fileResponse("F", "a.txt", blobfs.StatusAvailable, 2), noFile())
	f, err := s.Files.Find(ctx, db, "F")
	if err != nil || f.ID != "F" || f.Status != blobfs.StatusAvailable || f.Key != "F/a.txt" {
		t.Fatalf("Find = %+v, %v, want the row", f, err)
	}
	if _, err := s.Files.Find(ctx, db, "F"); !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("Find of a missing file = %v, want ErrNotFound", err)
	}
	calls := rec.Calls()
	if len(calls) != 2 || !slices.Equal(calls[0].Args, []any{"F"}) || !strings.HasSuffix(calls[0].SQL, "FROM blobfs_file f\nWHERE f.id = CAST($1 AS uuid)") {
		t.Errorf("Find ran %v, want one read by id per call", calls)
	}
}

// TestFindFileByName proves the lookup binds the directory and the
// normalized name, reports no row as ErrNotFound, and refuses an invalid
// name before any SQL.
func TestFindFileByName(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback, fileResponse("F", nfcName, blobfs.StatusPending, 1), noFile())
	f, err := s.Files.FindByName(ctx, db, blobfs.RootID, nfdName)
	if err != nil || f.ID != "F" {
		t.Fatalf("FindByName = %+v, %v, want the file", f, err)
	}
	if _, err := s.Files.FindByName(ctx, db, blobfs.RootID, "missing"); !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("FindByName of a free name = %v, want ErrNotFound", err)
	}
	for _, name := range []string{"", "a/b", ".."} {
		if _, err := s.Files.FindByName(ctx, db, blobfs.RootID, name); !errors.Is(err, blobfs.ErrInvalidName) {
			t.Errorf("FindByName(%q) = %v, want ErrInvalidName", name, err)
		}
	}
	calls := rec.Calls()
	if len(calls) != 2 || !slices.Equal(calls[0].Args, []any{blobfs.RootID, nfcName}) ||
		!strings.HasSuffix(calls[0].SQL, "WHERE f.directory_id = CAST($1 AS uuid) AND f.name = $2") {
		t.Errorf("FindByName ran %v, want two lookups bound to the directory and the normalized name", calls)
	}
}

// TestCreateFileForms proves Create inserts the pending row and returns it
// in either form: under the fallback on the pool, the insert and the read
// by id in a transaction of its own; inside the caller's transaction, the
// same two statements in it; and under the returning dialect, one
// statement. The insert binds the id, the directory, the normalized name,
// the key built from the id and the name, and the declared content type,
// with the pending status in its text.
func TestCreateFileForms(t *testing.T) {
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
		s, db, rec := openStore(t, c.form, changedFile(c.form, fileResponse(id, nfcName, blobfs.StatusPending, 1))...)
		var sess sqlate.Session = db
		if c.inTx {
			sess = begin(t, db)
		}
		f, err := s.Files.Create(ctx, sess, accepting{}, blobfs.RootID, nfdName, "application/pdf", data.WithID(id))
		if err != nil || f.ID != id || f.Status != blobfs.StatusPending || f.Key != id+"/"+nfcName {
			t.Fatalf("%s (in tx %v): Create = %+v, %v, want the pending row", c.form.name, c.inTx, f, err)
		}
		if got := ops(rec); got != c.ops {
			t.Errorf("%s (in tx %v): ops = %q, want %q", c.form.name, c.inTx, got, c.ops)
		}
		inserts := callsTo(rec, "INSERT INTO blobfs_file")
		if len(inserts) != 1 || !strings.Contains(inserts[0].SQL, "'pending'") || strings.Contains(inserts[0].SQL, "RETURNING") != c.form.single {
			t.Fatalf("%s: the insert is %v", c.form.name, inserts)
		}
		if want := []any{id, blobfs.RootID, nfcName, id + "/" + nfcName, "application/pdf"}; !slices.Equal(inserts[0].Args, want) {
			t.Errorf("%s: the insert bound %v, want %v", c.form.name, inserts[0].Args, want)
		}
	}

	// A minted id is a UUID, and the key and the read are built from it.
	s, db, rec := openStore(t, fallback, sqltest.Response{Affected: 1}, fileResponse("F", "a.txt", blobfs.StatusPending, 1))
	if _, err := s.Files.Create(ctx, db, accepting{}, blobfs.RootID, "a.txt", "text/plain"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	calls := rec.Calls()
	minted, _ := calls[1].Args[0].(string)
	if _, err := blobfs.ParseID(minted); err != nil || calls[1].Args[3] != minted+"/a.txt" || !slices.Equal(calls[2].Args, []any{minted}) {
		t.Errorf("the insert bound %v and the read %v, want a minted id, the key built from it, and the read by it", calls[1].Args, calls[2].Args)
	}
}

// TestCreateFileRefusals proves the checks before any SQL, an invalid
// name, a key the store refuses, and a supplied id that is the nil UUID or
// no UUID, and the classification of blobfs's constraints on the insert in
// both forms, each a ViolationError naming the constraint with the
// driver's text hidden. A check violation is no constraint the write
// mapping names, so it passes through as the driver reported it.
func TestCreateFileRefusals(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback)
	for _, name := range []string{"", "a/b", ".."} {
		if _, err := s.Files.Create(ctx, db, accepting{}, blobfs.RootID, name, "text/plain"); !errors.Is(err, blobfs.ErrInvalidName) {
			t.Errorf("Create(%q) = %v, want ErrInvalidName", name, err)
		}
	}
	cause := errors.New("the store says no")
	_, err := s.Files.Create(ctx, db, refusing{cause}, blobfs.RootID, "ok.txt", "text/plain")
	var ke *blobfs.KeyError
	if !errors.Is(err, blobfs.ErrInvalidKey) || !errors.Is(err, cause) || !errors.As(err, &ke) || !strings.HasSuffix(ke.Key, "/ok.txt") {
		t.Errorf("Create with a refusing store = %v, want a KeyError matching ErrInvalidKey and wrapping the cause", err)
	}
	for _, id := range []string{blobfs.RootID, "", "not-a-uuid"} {
		if _, err := s.Files.Create(ctx, db, accepting{}, blobfs.RootID, "ok.txt", "text/plain", data.WithID(id)); !errors.Is(err, blobfs.ErrInvalidID) {
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
			{blobfs.ConstraintUniqueFileDirectoryName, sqlate.ErrUniqueViolation, blobfs.ErrNameTaken},
			{blobfs.ConstraintForeignKeyFileDirectory, sqlate.ErrForeignKeyViolation, blobfs.ErrNotFound},
			{blobfs.ConstraintPrimaryKeyFile, sqlate.ErrUniqueViolation, blobfs.ErrIDTaken},
		} {
			s, db, _ := openStore(t, f, violation(c.constraint, c.class))
			_, err := s.Files.Create(ctx, db, accepting{}, blobfs.RootID, "ok.txt", "text/plain")
			var ve *blobfs.ViolationError
			if !errors.Is(err, c.want) || !errors.As(err, &ve) || ve.Constraint != c.constraint {
				t.Errorf("%s: Create under %s = %v, want %v", f.name, c.constraint, err, c.want)
			}
			if !strings.HasSuffix(err.Error(), c.want.Error()+" (constraint "+c.constraint+")") || strings.Contains(err.Error(), "duplicate key") {
				t.Errorf("%s: Create under %s = %q, want the sentinel and the constraint named and the driver's text hidden", f.name, c.constraint, err)
			}
		}
	}

	s, db, _ = openStore(t, fallback, violation("blobfs_ck_file_name", sqlate.ErrCheckViolation))
	_, err = s.Files.Create(ctx, db, accepting{}, blobfs.RootID, "ok.txt", "text/plain")
	var ve *blobfs.ViolationError
	if !errors.Is(err, sqlate.ErrCheckViolation) || errors.As(err, &ve) || !strings.Contains(err.Error(), "duplicate key") {
		t.Errorf("Create under a check violation = %v, want the driver's error unclassified", err)
	}
}

// TestCreateFileKeyBoundary proves the key is validated by the store's
// rules as given: a name whose key sits exactly at a rune limit is
// accepted, and one rune over is refused before any SQL.
func TestCreateFileKeyBoundary(t *testing.T) {
	ctx := context.Background()
	const limit = 60
	// The id is 36 runes and the slash one, so 23 two-byte runes fill the
	// limit exactly.
	fits := strings.Repeat(nfcName[3:], limit-37)
	s, db, rec := openStore(t, forms[1], fileResponse("F", fits, blobfs.StatusPending, 1))
	if _, err := s.Files.Create(ctx, db, runeLimit(limit), blobfs.RootID, fits, "text/plain"); err != nil {
		t.Fatalf("Create at the boundary: %v", err)
	}
	key, _ := rec.Calls()[0].Args[3].(string)
	if n := utf8.RuneCountInString(key); n != limit || len(key) <= limit {
		t.Errorf("the key bound is %d runes in %d bytes, want %d runes in more bytes", n, len(key), limit)
	}

	s, db, rec = openStore(t, forms[1])
	if _, err := s.Files.Create(ctx, db, runeLimit(limit), blobfs.RootID, fits+nfcName[3:], "text/plain"); !errors.Is(err, blobfs.ErrInvalidKey) {
		t.Fatalf("Create one rune over = %v, want ErrInvalidKey", err)
	}
	if calls := rec.Calls(); len(calls) != 0 {
		t.Errorf("the refusal reached the driver with %v", calls)
	}
}

// TestEnsureFile proves the insert-or-find in both forms: a free name is
// the lookup and Create's statements, created; a pending row is one
// lookup, resumed; an available or a deleting row is one lookup, present,
// with the row's status telling which; and a writer that wins between the
// lookup and the insert on the pool is recovered by one more lookup, whose
// row is reported by its status. Inside a transaction the same race
// returns ErrNameTaken and no further statement runs.
func TestEnsureFile(t *testing.T) {
	ctx := context.Background()
	for _, f := range forms {
		t.Run(f.name, func(t *testing.T) {
			insertOps, refusedOps := "query", "query"
			if !f.single {
				insertOps, refusedOps = "begin exec query commit", "begin exec rollback"
			}

			s, db, rec := openStore(t, f, append([]sqltest.Response{noFile()}, changedFile(f, fileResponse("N", "a.txt", blobfs.StatusPending, 1))...)...)
			file, outcome, err := s.Files.Ensure(ctx, db, accepting{}, blobfs.RootID, "a.txt", "text/plain")
			if err != nil || outcome != data.WriteCreated || file.ID != "N" {
				t.Fatalf("Ensure of a free name = %+v, %v, %v; want the pending row created", file, outcome, err)
			}
			if got, want := ops(rec), "query "+insertOps; got != want {
				t.Errorf("ops = %q, want %q", got, want)
			}
			if lookup := rec.Calls()[0]; !slices.Equal(lookup.Args, []any{blobfs.RootID, "a.txt"}) {
				t.Errorf("the lookup bound %v, want the directory and the name", lookup.Args)
			}

			for status, want := range map[blobfs.Status]data.WriteOutcome{
				blobfs.StatusPending:   data.WriteResumed,
				blobfs.StatusAvailable: data.WritePresent,
				blobfs.StatusDeleting:  data.WritePresent,
			} {
				s, db, rec := openStore(t, f, fileResponse("P", "a.txt", status, 2))
				file, outcome, err := s.Files.Ensure(ctx, db, accepting{}, blobfs.RootID, "a.txt", "text/plain", data.WithID(blobfs.NewID()))
				if err != nil || outcome != want || file.ID != "P" || file.Status != status {
					t.Errorf("Ensure over a %s row = %+v, %v, %v; want the row as it is and %s", status, file, outcome, err, want)
				}
				if got := ops(rec); got != "query" {
					t.Errorf("ops = %q, want the lookup alone", got)
				}
			}

			for status, want := range map[blobfs.Status]data.WriteOutcome{blobfs.StatusPending: data.WriteResumed, blobfs.StatusAvailable: data.WritePresent} {
				s, db, rec := openStore(t, f, noFile(), violation(blobfs.ConstraintUniqueFileDirectoryName, sqlate.ErrUniqueViolation), fileResponse("C", "a.txt", status, 1))
				file, outcome, err := s.Files.Ensure(ctx, db, accepting{}, blobfs.RootID, "a.txt", "text/plain")
				if err != nil || outcome != want || file.ID != "C" {
					t.Errorf("Ensure under a concurrent %s writer = %+v, %v, %v; want the writer's row and %s", status, file, outcome, err, want)
				}
				if got, want := ops(rec), "query "+refusedOps+" query"; got != want {
					t.Errorf("ops = %q, want %q", got, want)
				}
			}

			s, db, rec = openStore(t, f, noFile(), violation(blobfs.ConstraintUniqueFileDirectoryName, sqlate.ErrUniqueViolation))
			_, err = db.Transact(ctx, func(tx *sqlate.Tx) (blobfs.File, error) {
				file, _, err := s.Files.Ensure(ctx, tx, accepting{}, blobfs.RootID, "a.txt", "text/plain")
				return file, err
			})
			if !errors.Is(err, blobfs.ErrNameTaken) {
				t.Errorf("Ensure under a concurrent writer inside a transaction = %v, want ErrNameTaken", err)
			}
			want := "begin query query rollback"
			if !f.single {
				want = "begin query exec rollback"
			}
			if got := ops(rec); got != want {
				t.Errorf("ops = %q, want %q: no lookup after the refused insert inside the transaction", got, want)
			}
		})
	}
}

// TestEnsureFileRefusals proves the checks that run before any SQL, the
// same as Create's: an invalid name, a key the store refuses, and a
// supplied id that is the nil UUID or no UUID.
func TestEnsureFileRefusals(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback)
	if _, _, err := s.Files.Ensure(ctx, db, accepting{}, blobfs.RootID, "a/b", "text/plain"); !errors.Is(err, blobfs.ErrInvalidName) {
		t.Errorf("Ensure(a/b) = %v, want ErrInvalidName", err)
	}
	cause := errors.New("the store says no")
	if _, _, err := s.Files.Ensure(ctx, db, refusing{cause}, blobfs.RootID, "ok.txt", "text/plain"); !errors.Is(err, blobfs.ErrInvalidKey) || !errors.Is(err, cause) {
		t.Errorf("Ensure with a refusing store = %v, want ErrInvalidKey wrapping the cause", err)
	}
	for _, id := range []string{blobfs.RootID, "", "not-a-uuid"} {
		if _, _, err := s.Files.Ensure(ctx, db, accepting{}, blobfs.RootID, "ok.txt", "text/plain", data.WithID(id)); !errors.Is(err, blobfs.ErrInvalidID) {
			t.Errorf("Ensure with the id %q = %v, want ErrInvalidID", id, err)
		}
	}
	if calls := rec.Calls(); len(calls) != 0 {
		t.Errorf("the refusals reached the driver with %v", calls)
	}
}

// TestCompleteFile proves the last step of a write in both forms: the
// guarded update binds the object's facts, the id, and the expected
// version, carries the status predicate, and returns the available row.
// When it changes no row, the row its read returned classifies the
// refusal with no further statement: no row is ErrNotFound, a row at
// another version query.ErrVersionMismatch naming both versions, and a
// row at the expected version that is not pending a TransitionError from
// its status, which matches ErrDeleting for a deleting row only.
func TestCompleteFile(t *testing.T) {
	ctx := context.Background()
	obj := blobfs.Object{Size: 42, ContentType: "text/plain", ETag: `"abc"`}
	for _, f := range forms {
		t.Run(f.name, func(t *testing.T) {
			s, db, rec := openStore(t, f, changedFile(f, fileResponse("F", "a.txt", blobfs.StatusAvailable, 2))...)
			file, err := s.Files.Complete(ctx, db, "F", 1, obj)
			if err != nil || file.Status != blobfs.StatusAvailable || file.Version != 2 {
				t.Fatalf("Complete = %+v, %v, want the available row", file, err)
			}
			want := "query"
			if !f.single {
				want = "begin exec query commit"
			}
			if got := ops(rec); got != want {
				t.Errorf("ops = %q, want %q", got, want)
			}
			update := callsTo(rec, "UPDATE blobfs_file")[0]
			if !strings.Contains(update.SQL, "status = 'available'") || !strings.Contains(update.SQL, "version = version + 1") ||
				!strings.Contains(update.SQL, "AND status = 'pending'") || strings.Contains(update.SQL, "RETURNING") != f.single {
				t.Errorf("the update is %q", update.SQL)
			}
			if !slices.Equal(update.Args, []any{int64(42), "text/plain", `"abc"`, "F", int64(1)}) {
				t.Errorf("the update bound %v, want the size, content type, etag, id, and expected version", update.Args)
			}

			// complete runs Complete of F at version 1 over a command that
			// changed no row, with the read returning read, and returns its
			// ops it ran and its error.
			complete := func(read sqltest.Response) (string, error) {
				s, db, rec := openStore(t, f, unchangedFile(f, read)...)
				_, err := s.Files.Complete(ctx, db, "F", 1, obj)
				return ops(rec), err
			}
			refusedOps := "query query"
			if !f.single {
				refusedOps = "begin exec query commit"
			}

			_, err = complete(noFile())
			if !errors.Is(err, blobfs.ErrNotFound) {
				t.Errorf("Complete of a missing row = %v, want ErrNotFound", err)
			}
			got, err := complete(fileResponse("F", "a.txt", blobfs.StatusPending, 3))
			if !errors.Is(err, query.ErrVersionMismatch) || !strings.Contains(err.Error(), "version mismatch: expected 1, current 3") {
				t.Errorf("Complete at a stale version = %v, want ErrVersionMismatch naming both versions", err)
			}
			if got != refusedOps {
				t.Errorf("ops = %q, want %q: the refusal classifies from the returning read alone", got, refusedOps)
			}
			for _, status := range []blobfs.Status{blobfs.StatusDeleting, blobfs.StatusAvailable} {
				_, err := complete(fileResponse("F", "a.txt", status, 1))
				var te *blobfs.TransitionError
				if !errors.Is(err, blobfs.ErrInvalidTransition) || errors.Is(err, query.ErrVersionMismatch) || !errors.As(err, &te) || te.From != status || te.To != blobfs.StatusAvailable {
					t.Errorf("Complete of a %s row = %v, want the TransitionError to available and no version mismatch", status, err)
				}
				if errors.Is(err, blobfs.ErrDeleting) != (status == blobfs.StatusDeleting) {
					t.Errorf("Complete of a %s row = %v; ErrDeleting should match for deleting only", status, err)
				}
			}
			// A pending row at the expected version the update did not
			// change is no state the statement can leave; it is reported,
			// not taken for success.
			_, err = complete(fileResponse("F", "a.txt", blobfs.StatusPending, 1))
			if err == nil || errors.Is(err, blobfs.ErrInvalidTransition) || !strings.Contains(err.Error(), "pending at version 1") {
				t.Errorf("Complete refused over a pending row = %v, want an error naming the row's state", err)
			}
		})
	}
}

// TestMoveFile proves the file move in both forms: one guarded command on
// the pool bound to the directory, the normalized name, the id, and the
// expected version, with the status predicate in its text and the key
// untouched, returning the moved row; a refused name before any SQL; and
// the outcomes of the guard: a missing row is ErrNotFound, a moved version
// ErrVersionMismatch, a deleting row at the expected version ErrDeleting,
// a missing directory ErrNotFound through the foreign key, and a taken name
// ErrNameTaken through the unique constraint.
func TestMoveFile(t *testing.T) {
	ctx := context.Background()
	for _, f := range forms {
		t.Run(f.name, func(t *testing.T) {
			s, db, rec := openStore(t, f, changedFile(f, fileIn("F", "P", nfcName, blobfs.StatusAvailable, 2))...)
			file, err := s.Files.Move(ctx, db, "F", "P", nfdName, 1)
			if err != nil || file.ID != "F" || file.DirectoryID != "P" || file.Version != 2 || file.Key != "F/"+nfcName {
				t.Fatalf("Move = %+v, %v, want the moved row", file, err)
			}
			want := "query"
			if !f.single {
				want = "begin exec query commit"
			}
			if got := ops(rec); got != want {
				t.Errorf("ops = %q, want %q", got, want)
			}
			update := callsTo(rec, "UPDATE blobfs_file")[0]
			command, _, _ := strings.Cut(update.SQL, "RETURNING")
			if !strings.Contains(command, "AND status <> 'deleting'") || strings.Contains(command, "key") {
				t.Errorf("the update is %q, want the status predicate and no key column", update.SQL)
			}
			if !slices.Equal(update.Args, []any{"P", nfcName, "F", int64(1)}) {
				t.Errorf("the update bound %v, want the directory, the normalized name, the id, and the expected version", update.Args)
			}

			move := func(responses ...sqltest.Response) error {
				s, db, _ := openStore(t, f, responses...)
				_, err := s.Files.Move(ctx, db, "F", "P", "a", 1)
				return err
			}
			if err := move(unchangedFile(f, noFile())...); !errors.Is(err, blobfs.ErrNotFound) {
				t.Errorf("Move of a missing row = %v, want ErrNotFound", err)
			}
			err = move(unchangedFile(f, fileResponse("F", "a", blobfs.StatusAvailable, 3))...)
			if !errors.Is(err, query.ErrVersionMismatch) || !strings.Contains(err.Error(), "expected 1, current 3") {
				t.Errorf("Move at a stale version = %v, want ErrVersionMismatch naming both versions", err)
			}
			err = move(unchangedFile(f, fileResponse("F", "a", blobfs.StatusDeleting, 1))...)
			if !errors.Is(err, blobfs.ErrDeleting) || errors.Is(err, query.ErrVersionMismatch) || !strings.Contains(err.Error(), "the row is deleting") {
				t.Errorf("Move of a deleting row = %v, want ErrDeleting and not a version mismatch", err)
			}
			for _, c := range []struct {
				constraint string
				class      error
				want       error
			}{
				{blobfs.ConstraintForeignKeyFileDirectory, sqlate.ErrForeignKeyViolation, blobfs.ErrNotFound},
				{blobfs.ConstraintUniqueFileDirectoryName, sqlate.ErrUniqueViolation, blobfs.ErrNameTaken},
			} {
				err := move(violation(c.constraint, c.class))
				var ce *sqlate.ConstraintError
				if !errors.Is(err, c.want) || !errors.As(err, &ce) || ce.Constraint != c.constraint {
					t.Errorf("Move under %s = %v, want %v with the constraint reachable", c.constraint, err, c.want)
				}
			}
		})
	}

	s, db, rec := openStore(t, fallback)
	if _, err := s.Files.Move(ctx, db, "F", "P", "", 1); !errors.Is(err, blobfs.ErrInvalidName) {
		t.Errorf("Move to an empty name = %v, want ErrInvalidName", err)
	}
	if calls := rec.Calls(); len(calls) != 0 {
		t.Errorf("the refused name reached the driver with %v", calls)
	}
}
