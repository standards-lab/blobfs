package data_test

import (
	"context"
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
	"github.com/standards-lab/blobfs/data/datatest"
)

// fileRow is a file row in a directory, with a status and a version; its
// key is the id and the name, as NewKey builds it.
func fileRow(id, directoryID, name string, status blobfs.Status, version int64) blobfs.File {
	now := time.Now()
	return blobfs.File{
		ID: id, DirectoryID: directoryID, Name: name, Status: status, Key: id + "/" + name,
		ContentType: "text/plain", Version: version, CreatedAt: now, UpdatedAt: now,
	}
}

// fileIn scripts one file row in a directory, with a status and a version.
func fileIn(id, directoryID, name string, status blobfs.Status, version int64) sqltest.Response {
	return datatest.FileRows(fileRow(id, directoryID, name, status, version))
}

// fileResponse scripts one file row in the root.
func fileResponse(id, name string, status blobfs.Status, version int64) sqltest.Response {
	return fileIn(id, blobfs.RootID, name, status, version)
}

// noFile scripts a file read, or a returning command's single-statement
// form, that yields no row.
func noFile() sqltest.Response { return datatest.FileRows() }

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

// wantDeleting fails the test unless err is a blobfs.DeletingError of the
// kind directory, naming id, and matches blobfs.ErrDeleting.
func wantDeleting(t *testing.T, what string, err error, directory bool, id string) {
	t.Helper()
	var de *blobfs.DeletingError
	if !errors.Is(err, blobfs.ErrDeleting) || !errors.As(err, &de) || de.Directory != directory || de.ID != id {
		t.Errorf("%s = %v, want the DeletingError with Directory %v naming %s", what, err, directory, id)
	}
}

// errUnexplained stands in a test case for an untyped refusal the reads
// after a statement that selected nothing did not explain.
var errUnexplained = errors.New("the refusal unexplained")

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

// TestFindFileByName checks the normalized lookup and its refusals.
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

// TestCreateFileForms checks Create's pending insert and its bindings in
// the fallback, on the pool and in a transaction, and in one statement.
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
		if len(inserts) != 1 || !strings.Contains(inserts[0].SQL, "'pending'") || !strings.Contains(inserts[0].SQL, "d.status = 'active'") ||
			strings.Contains(inserts[0].SQL, "RETURNING") != c.form.single {
			t.Fatalf("%s: the insert is %v", c.form.name, inserts)
		}
		if want := []any{id, nfcName, id + "/" + nfcName, "application/pdf", blobfs.RootID}; !slices.Equal(inserts[0].Args, want) {
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
	if _, err := blobfs.ParseID(minted); err != nil || calls[1].Args[2] != minted+"/a.txt" || !slices.Equal(calls[2].Args, []any{minted}) {
		t.Errorf("the insert bound %v and the read %v, want a minted id, the key built from it, and the read by it", calls[1].Args, calls[2].Args)
	}
}

// TestCreateFileRefusals checks the refusals before any SQL and the
// classification of blobfs's constraints in both forms.
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

	// An insert that selected no row reads the directory to classify it.
	for _, f := range forms {
		for _, c := range []struct {
			directory sqltest.Response
			want      error
		}{
			{noDirectory(), blobfs.ErrNotFound},
			{directoryIn("D", blobfs.RootID, "d", blobfs.DirectoryStatusDeleting, 2), blobfs.ErrDeleting},
		} {
			s, db, rec := openStore(t, f, append(unchangedFile(f, noFile()), c.directory)...)
			_, err := s.Files.Create(ctx, db, accepting{}, "D", "ok.txt", "text/plain")
			var ve *blobfs.ViolationError
			if !errors.Is(err, c.want) || errors.As(err, &ve) {
				t.Errorf("%s: Create under a directory the insert did not select = %v, want %v", f.name, err, c.want)
			}
			if reads := callsTo(rec, "SELECT d.id"); len(reads) != 1 || !slices.Equal(reads[0].Args, []any{"D"}) {
				t.Errorf("%s: the directory reads are %v, want one by the directory's id", f.name, reads)
			}
		}
	}
	// An insert that selected no row from an active directory reads the
	// file that holds the name: a deleting holder is refused by its delete,
	// or by its directory's once the directory is deleting, marked as the
	// name's holder, and never as the taken name. A read that explains
	// nothing, the holder purged or replaced by a live row since the
	// insert's snapshot, runs the insert once more: it succeeds, meets the
	// constraint, or selects nothing again with a holder the read finds,
	// and only a rerun as unexplained leaves the refusal untyped.
	for _, f := range forms {
		active := directoryIn("D", blobfs.RootID, "d", blobfs.DirectoryStatusActive, 1)
		deleting := fileIn("H", "D", "ok.txt", blobfs.StatusDeleting, 2)
		again := func(reads ...sqltest.Response) []sqltest.Response {
			return append(append(unchangedFile(f, noFile()), active), reads...)
		}
		taken := violation(blobfs.ConstraintUniqueFileDirectoryName, sqlate.ErrUniqueViolation)
		for _, c := range []struct {
			name      string
			reads     []sqltest.Response
			want      error // nil for success
			directory bool
			id        string
			inserts   int
		}{
			{"a deleting holder", []sqltest.Response{deleting, active}, blobfs.ErrDeleting, false, "H", 1},
			{"a holder its branch's mark reached", []sqltest.Response{deleting, directoryIn("D", blobfs.RootID, "d", blobfs.DirectoryStatusDeleting, 2)}, blobfs.ErrDeleting, true, "D", 1},
			{"no holder, the rerun selected", append([]sqltest.Response{noFile()}, changedFile(f, fileIn("N", "D", "ok.txt", blobfs.StatusPending, 1))...), nil, false, "", 2},
			{"an available holder, the rerun taken", []sqltest.Response{fileIn("H", "D", "ok.txt", blobfs.StatusAvailable, 1), taken}, blobfs.ErrNameTaken, false, "", 2},
			{"no holder, then a deleting holder", append([]sqltest.Response{noFile()}, again(deleting, active)...), blobfs.ErrDeleting, false, "H", 2},
			{"no holder twice", append([]sqltest.Response{noFile()}, again(noFile())...), errUnexplained, false, "", 2},
			{"an available holder twice", append([]sqltest.Response{fileIn("H", "D", "ok.txt", blobfs.StatusAvailable, 1)}, again(fileIn("H", "D", "ok.txt", blobfs.StatusAvailable, 1))...), errUnexplained, false, "", 2},
		} {
			s, db, rec := openStore(t, f, append(append(unchangedFile(f, noFile()), active), c.reads...)...)
			file, err := s.Files.Create(ctx, db, accepting{}, "D", "ok.txt", "text/plain")
			switch c.want {
			case nil:
				if err != nil || file.ID != "N" {
					t.Errorf("%s: Create under %s = %+v, %v, want the rerun's row", f.name, c.name, file, err)
				}
			case blobfs.ErrDeleting:
				wantDeleting(t, f.name+": Create under "+c.name, err, c.directory, c.id)
				if errors.Is(err, blobfs.ErrNameTaken) || !strings.Contains(err.Error(), "the file H holds the name: ") {
					t.Errorf("%s: Create under %s = %v, want the holder marked and no ErrNameTaken", f.name, c.name, err)
				}
			case blobfs.ErrNameTaken:
				if !errors.Is(err, blobfs.ErrNameTaken) || errors.Is(err, blobfs.ErrDeleting) {
					t.Errorf("%s: Create under %s = %v, want ErrNameTaken", f.name, c.name, err)
				}
			default:
				if err == nil || errors.Is(err, blobfs.ErrDeleting) || errors.Is(err, blobfs.ErrNameTaken) || !strings.Contains(err.Error(), "no deleting row holds the name") {
					t.Errorf("%s: Create under %s = %v, want the refusal unexplained", f.name, c.name, err)
				}
			}
			if c.want != nil && c.want != blobfs.ErrNameTaken {
				if reads := callsTo(rec, "SELECT f.id"); len(reads) == 0 || !slices.Equal(reads[len(reads)-1].Args, []any{"D", "ok.txt"}) {
					t.Errorf("%s: the file reads are %v, want the last by the directory and the name", f.name, reads)
				}
			}
			inserts := callsTo(rec, "INSERT INTO blobfs_file")
			if len(inserts) != c.inserts || !strings.Contains(inserts[0].SQL, "h.status = 'deleting'") {
				t.Errorf("%s: Create under %s ran the inserts %v, want %d with the deleting holder's predicate", f.name, c.name, inserts, c.inserts)
			}
			wantDone(t, rec)
		}
	}
	// A caller-supplied id another row carries does not make an insert that
	// selected nothing a success: the read found that row unchanged.
	s, db, _ = openStore(t, fallback, sqltest.Response{Affected: 0}, fileResponse("F", "other.txt", blobfs.StatusAvailable, 1),
		directoryIn("D", blobfs.RootID, "d", blobfs.DirectoryStatusDeleting, 2))
	if _, err = s.Files.Create(ctx, db, accepting{}, "D", "ok.txt", "text/plain", data.WithID(blobfs.NewID())); !errors.Is(err, blobfs.ErrDeleting) {
		t.Errorf("Create whose read found another row = %v, want ErrDeleting", err)
	}

	s, db, _ = openStore(t, fallback, violation("blobfs_ck_file_name", sqlate.ErrCheckViolation))
	_, err = s.Files.Create(ctx, db, accepting{}, blobfs.RootID, "ok.txt", "text/plain")
	var ve *blobfs.ViolationError
	if !errors.Is(err, sqlate.ErrCheckViolation) || errors.As(err, &ve) || !strings.Contains(err.Error(), "duplicate key") {
		t.Errorf("Create under a check violation = %v, want the driver's error unclassified", err)
	}
}

// TestCreateFileKeyBoundary checks a key at the store's rune limit is
// accepted and one rune over refused before any SQL.
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
	key, _ := rec.Calls()[0].Args[2].(string)
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

// TestEnsureFile checks the insert-or-find and its WriteOutcome in both
// forms, the race on the pool and inside a transaction included.
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

			// A concurrent writer under the same id fails the primary key
			// first: on the pool the name is looked up again, and the row is
			// found when it carries the id; otherwise the id stays taken.
			id := blobfs.NewID()
			s, db, rec = openStore(t, f, noFile(), violation(blobfs.ConstraintPrimaryKeyFile, sqlate.ErrUniqueViolation), fileResponse(id, "a.txt", blobfs.StatusPending, 1))
			file, outcome, err = s.Files.Ensure(ctx, db, accepting{}, blobfs.RootID, "a.txt", "text/plain", data.WithID(strings.ToUpper(id)))
			if err != nil || outcome != data.WriteResumed || file.ID != id {
				t.Errorf("Ensure under a concurrent writer of the same id = %+v, %v, %v; want the writer's row resumed", file, outcome, err)
			}
			if got, want := ops(rec), "query "+refusedOps+" query"; got != want {
				t.Errorf("ops = %q, want %q", got, want)
			}
			for _, again := range []sqltest.Response{fileResponse(blobfs.NewID(), "a.txt", blobfs.StatusPending, 1), noFile()} {
				s, db, rec = openStore(t, f, noFile(), violation(blobfs.ConstraintPrimaryKeyFile, sqlate.ErrUniqueViolation), again)
				_, _, err = s.Files.Ensure(ctx, db, accepting{}, blobfs.RootID, "a.txt", "text/plain", data.WithID(id))
				if !errors.Is(err, blobfs.ErrIDTaken) || errors.Is(err, blobfs.ErrNameTaken) {
					t.Errorf("Ensure under an id taken by a row elsewhere = %v, want ErrIDTaken", err)
				}
				if got, want := ops(rec), "query "+refusedOps+" query"; got != want {
					t.Errorf("ops = %q, want %q", got, want)
				}
			}
			// A second lookup that fails leaves the ErrIDTaken, its failure
			// joined.
			errLookup := errors.New("the lookup failed")
			s, db, _ = openStore(t, f, noFile(), violation(blobfs.ConstraintPrimaryKeyFile, sqlate.ErrUniqueViolation), sqltest.Response{Err: errLookup})
			_, _, err = s.Files.Ensure(ctx, db, accepting{}, blobfs.RootID, "a.txt", "text/plain", data.WithID(id))
			if !errors.Is(err, blobfs.ErrIDTaken) || !errors.Is(err, errLookup) {
				t.Errorf("Ensure under a taken id whose lookup failed = %v, want ErrIDTaken joined to the lookup's failure", err)
			}
			// Inside a transaction the id's violation is returned with no
			// second lookup, since it may have aborted the transaction.
			s, db, rec = openStore(t, f, noFile(), violation(blobfs.ConstraintPrimaryKeyFile, sqlate.ErrUniqueViolation))
			_, err = db.Transact(ctx, func(tx *sqlate.Tx) (blobfs.File, error) {
				file, _, err := s.Files.Ensure(ctx, tx, accepting{}, blobfs.RootID, "a.txt", "text/plain", data.WithID(id))
				return file, err
			})
			if !errors.Is(err, blobfs.ErrIDTaken) {
				t.Errorf("Ensure under a taken id inside a transaction = %v, want ErrIDTaken", err)
			}
			txOps := "begin query query rollback"
			if !f.single {
				txOps = "begin query exec rollback"
			}
			if got := ops(rec); got != txOps {
				t.Errorf("ops = %q, want %q: no lookup after the refused insert inside the transaction", got, txOps)
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

			// A deleting row a writer committed between the lookup and the
			// insert is found again, on the pool and inside a transaction
			// alike, since the insert selected nothing and failed no
			// statement: WritePresent, as the lookup would have found it. A
			// holder gone again by the second lookup leaves its refusal.
			root := directoryResponse(blobfs.RootID, "", "/", 1)
			holder := fileResponse("H", "a.txt", blobfs.StatusDeleting, 2)
			refused := append(append([]sqltest.Response{noFile()}, unchangedFile(f, noFile())...), root, holder, root)
			for _, inTx := range []bool{false, true} {
				for _, again := range []sqltest.Response{holder, noFile()} {
					s, db, rec := openStore(t, f, append(slices.Clone(refused), again)...)
					type ensured struct {
						file    blobfs.File
						outcome data.WriteOutcome
					}
					run := func(sess sqlate.Session) (ensured, error) {
						file, outcome, err := s.Files.Ensure(ctx, sess, accepting{}, blobfs.RootID, "a.txt", "text/plain")
						return ensured{file, outcome}, err
					}
					var e ensured
					var err error
					if inTx {
						e, err = db.Transact(ctx, func(tx *sqlate.Tx) (ensured, error) { return run(tx) })
					} else {
						e, err = run(db)
					}
					if len(again.Rows) == 0 {
						wantDeleting(t, "Ensure whose deleting holder is gone again", err, false, "H")
					} else if err != nil || e.outcome != data.WritePresent || e.file.ID != "H" || e.file.Status != blobfs.StatusDeleting {
						t.Errorf("Ensure under a concurrent deleting row, in a transaction %v = %+v, %v; want the row and WritePresent", inTx, e, err)
					}
					var byName int
					for _, c := range callsTo(rec, "SELECT f.id") {
						if slices.Equal(c.Args, []any{blobfs.RootID, "a.txt"}) {
							byName++
						}
					}
					if byName != 3 {
						t.Errorf("Ensure under a concurrent deleting row read by the name %d times, want the lookup, the holder's read, and the lookup again", byName)
					}
					wantDone(t, rec)
				}
			}
		})
	}
}

// TestEnsureFileRefusals checks Ensure refuses what Create refuses before
// any SQL.
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

// TestCompleteFile checks the guarded update in both forms and each
// refusal classified from the row its read returns.
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
			// changed no row, its read returning read.
			complete := func(read ...sqltest.Response) (string, error) {
				s, db, rec := openStore(t, f, append(unchangedFile(f, read[0]), read[1:]...)...)
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
			root := directoryResponse(blobfs.RootID, "", "/", 1)
			for _, status := range []blobfs.Status{blobfs.StatusDeleting, blobfs.StatusAvailable} {
				read := []sqltest.Response{fileResponse("F", "a.txt", status, 1)}
				if status == blobfs.StatusDeleting {
					read = append(read, root)
				}
				_, err := complete(read...)
				var te *blobfs.TransitionError
				if !errors.Is(err, blobfs.ErrInvalidTransition) || errors.Is(err, query.ErrVersionMismatch) || !errors.As(err, &te) || te.From != status || te.To != blobfs.StatusAvailable {
					t.Errorf("Complete of a %s row = %v, want the TransitionError to available and no version mismatch", status, err)
				}
				if errors.Is(err, blobfs.ErrDeleting) != (status == blobfs.StatusDeleting) {
					t.Errorf("Complete of a %s row = %v; ErrDeleting should match for deleting only", status, err)
				}
			}
			// Deleting outranks the stale version, and the directory's
			// read tells whose delete refused the completion.
			got, err = complete(fileResponse("F", "a.txt", blobfs.StatusDeleting, 2), root)
			var te *blobfs.TransitionError
			if !errors.Is(err, blobfs.ErrDeleting) || errors.Is(err, query.ErrVersionMismatch) || !errors.As(err, &te) || te.From != blobfs.StatusDeleting {
				t.Errorf("Complete of a deleting row at a later version = %v, want the TransitionError from deleting and no version mismatch", err)
			}
			wantDeleting(t, "Complete of a deleting row", err, false, "F")
			if got != refusedOps+" query" {
				t.Errorf("ops = %q, want %q: the refusal classifies from the returning read and the directory's", got, refusedOps+" query")
			}
			_, err = complete(fileIn("F", "S", "a.txt", blobfs.StatusDeleting, 2), directoryIn("S", blobfs.RootID, "s", blobfs.DirectoryStatusDeleting, 2))
			wantDeleting(t, "Complete of a file its branch's mark reached", err, true, "S")
			if !errors.As(err, &te) {
				t.Errorf("Complete of a file its branch's mark reached = %v, want the TransitionError reachable", err)
			}
			_, err = complete(fileResponse("F", "a.txt", blobfs.StatusAvailable, 2))
			if !errors.Is(err, query.ErrVersionMismatch) || errors.Is(err, blobfs.ErrInvalidTransition) || !strings.Contains(err.Error(), "expected 1, current 2") {
				t.Errorf("Complete of an available row at another version = %v, want ErrVersionMismatch", err)
			}
			// A pending row the update left unchanged is reported, not
			// taken for success.
			_, err = complete(fileResponse("F", "a.txt", blobfs.StatusPending, 1))
			if err == nil || errors.Is(err, blobfs.ErrInvalidTransition) || !strings.Contains(err.Error(), "pending at version 1") {
				t.Errorf("Complete refused over a pending row = %v, want an error naming the row's state", err)
			}
		})
	}
}

// TestMoveFile checks the guarded move in both forms and each refusal,
// classified from the row's read and the two directories' reads.
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
			if !strings.Contains(command, "AND status <> 'deleting'") || strings.Count(command, "status = 'active'") != 2 || strings.Contains(command, "key") {
				t.Errorf("the update is %q, want the status predicates and no key column", update.SQL)
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
			// active scripts the reads of the file's directory and the new
			// one, both active, after an update that changed nothing.
			active := []sqltest.Response{directoryResponse(blobfs.RootID, "", "/", 1), directoryResponse("P", blobfs.RootID, "p", 1)}
			err = move(append(unchangedFile(f, fileResponse("F", "a", blobfs.StatusAvailable, 3)), active...)...)
			if !errors.Is(err, query.ErrVersionMismatch) || !strings.Contains(err.Error(), "expected 1, current 3") {
				t.Errorf("Move at a stale version = %v, want ErrVersionMismatch naming both versions", err)
			}
			for _, version := range []int64{1, 3} {
				// Deleting at either end outranks the stale version.
				from := fileIn("F", "S", "a", blobfs.StatusAvailable, version)
				err = move(append(unchangedFile(f, from), directoryIn("S", blobfs.RootID, "s", blobfs.DirectoryStatusDeleting, 2))...)
				if !errors.Is(err, blobfs.ErrDeleting) || errors.Is(err, query.ErrVersionMismatch) || !strings.Contains(err.Error(), "the directory S is deleting") {
					t.Errorf("Move out of a deleting directory at version %d = %v, want ErrDeleting", version, err)
				}
				wantDeleting(t, "Move out of a deleting directory", err, true, "S")
				err = move(append(unchangedFile(f, from), directoryResponse("S", blobfs.RootID, "s", 1), directoryIn("P", blobfs.RootID, "p", blobfs.DirectoryStatusDeleting, 2))...)
				if !errors.Is(err, blobfs.ErrDeleting) || errors.Is(err, query.ErrVersionMismatch) || !strings.Contains(err.Error(), "the directory P is deleting") {
					t.Errorf("Move into a deleting directory at version %d = %v, want ErrDeleting", version, err)
				}
				wantDeleting(t, "Move into a deleting directory", err, true, "P")
			}
			// A missing new directory is refused by the predicate first.
			missing := []sqltest.Response{directoryResponse(blobfs.RootID, "", "/", 1), noDirectory()}
			err = move(append(unchangedFile(f, fileResponse("F", "a", blobfs.StatusAvailable, 1)), missing...)...)
			var ve *blobfs.ViolationError
			if !errors.Is(err, blobfs.ErrNotFound) || errors.As(err, &ve) {
				t.Errorf("Move into a missing directory = %v, want ErrNotFound", err)
			}
			err = move(append(unchangedFile(f, fileResponse("F", "a", blobfs.StatusAvailable, 3)), missing...)...)
			if !errors.Is(err, query.ErrVersionMismatch) {
				t.Errorf("Move into a missing directory at a stale version = %v, want ErrVersionMismatch", err)
			}
			for _, version := range []int64{1, 2} {
				// Deleting outranks the stale version.
				err = move(append(unchangedFile(f, fileResponse("F", "a", blobfs.StatusDeleting, version)), active[0])...)
				if errors.Is(err, query.ErrVersionMismatch) || !strings.Contains(err.Error(), "the file F is deleting") || strings.Contains(err.Error(), "holds the name") {
					t.Errorf("Move of a deleting row at version %d = %v, want its own ErrDeleting, not a holder's, and not a version mismatch", version, err)
				}
				wantDeleting(t, "Move of a deleting row", err, false, "F")
			}
			err = move(append(unchangedFile(f, fileIn("F", "S", "a", blobfs.StatusDeleting, 2)), directoryIn("S", blobfs.RootID, "s", blobfs.DirectoryStatusDeleting, 2))...)
			wantDeleting(t, "Move of a file its branch's mark reached", err, true, "S")
			// A deleting file that holds the name in the new directory
			// refuses the move by its own delete, not as the taken name, and
			// is marked as the name's holder, apart from the moved row's own.
			holder := append(append(unchangedFile(f, fileResponse("F", "b", blobfs.StatusAvailable, 1)), active...),
				fileIn("H", "P", "a", blobfs.StatusDeleting, 2), directoryResponse("P", blobfs.RootID, "p", 1))
			s, db, rec = openStore(t, f, holder...)
			_, err = s.Files.Move(ctx, db, "F", "P", "a", 1)
			if errors.Is(err, blobfs.ErrNameTaken) || !strings.Contains(err.Error(), "the file H holds the name: ") {
				t.Errorf("Move onto a deleting holder's name = %v, want the holder marked and no ErrNameTaken", err)
			}
			wantDeleting(t, "Move onto a deleting holder's name", err, false, "H")
			if reads := callsTo(rec, "SELECT f.id"); !slices.Equal(reads[len(reads)-1].Args, []any{"P", "a"}) {
				t.Errorf("the last file read bound %v, want the new directory and the name", reads[len(reads)-1].Args)
			}
			if update := callsTo(rec, "UPDATE blobfs_file")[0]; !strings.Contains(update.SQL, "h.status = 'deleting'") {
				t.Errorf("the update is %q, want the deleting holder's predicate", update.SQL)
			}
			wantDone(t, rec)
			// An available row the update left unchanged, with no deleting
			// holder, runs the update once more, and is reported, not taken
			// for success, when the rerun is as unexplained.
			unexplained := append(append(unchangedFile(f, fileResponse("F", "a", blobfs.StatusAvailable, 1)), active...), noFile())
			err = move(append(slices.Clone(unexplained), unexplained...)...)
			if err == nil || errors.Is(err, blobfs.ErrDeleting) || errors.Is(err, query.ErrVersionMismatch) || !strings.Contains(err.Error(), "available at version 1") {
				t.Errorf("Move refused over an available row, twice = %v, want an error naming the row's state", err)
			}
			// The rerun, its holder purged or its name taken by a live row
			// since the update's snapshot, moves the row, meets the
			// constraint, or selects nothing again with a holder the read
			// finds.
			s, db, rec = openStore(t, f, append(slices.Clone(unexplained), changedFile(f, fileIn("F", "P", "a", blobfs.StatusAvailable, 2))...)...)
			if file, err := s.Files.Move(ctx, db, "F", "P", "a", 1); err != nil || file.Version != 2 {
				t.Errorf("Move whose rerun changed the row = %+v, %v, want the row", file, err)
			}
			if updates := callsTo(rec, "UPDATE blobfs_file"); len(updates) != 2 {
				t.Errorf("Move ran %d updates, want the update and its rerun", len(updates))
			}
			wantDone(t, rec)
			err = move(append(slices.Clone(unexplained), violation(blobfs.ConstraintUniqueFileDirectoryName, sqlate.ErrUniqueViolation))...)
			if !errors.Is(err, blobfs.ErrNameTaken) || errors.Is(err, blobfs.ErrDeleting) {
				t.Errorf("Move whose rerun met a live holder = %v, want ErrNameTaken", err)
			}
			err = move(append(slices.Clone(unexplained), holder...)...)
			wantDeleting(t, "Move whose rerun met a deleting holder", err, false, "H")
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
