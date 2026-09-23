package data_test

import (
	"context"
	"database/sql/driver"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"
	"github.com/standards-lab/sqlate/sqltest"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data"
)

// catalog builds the catalog a consumer builds: the library's patterns and
// blobfs's, and nothing else.
func catalog(t *testing.T) *query.Catalog {
	t.Helper()
	c, err := query.NewCatalog(query.Patterns(), data.Patterns())
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	return c
}

// form is one way a returning command runs: the fallback under
// sqltest.Dialect, the command and then its read, and the single-statement
// form under sqltest.ReturningDialect, the command with RETURNING.
type form struct {
	name    string
	dialect sqlate.Dialect
	single  bool
}

// forms are the two forms, for a test that runs over both.
var forms = []form{
	{"Fallback", sqltest.Dialect{}, false},
	{"Single", sqltest.ReturningDialect{}, true},
}

// fallback is the form most tests run under.
var fallback = forms[0]

// newStore compiles the store against the consumer's catalog under the
// form's dialect.
func newStore(t *testing.T, f form, opts ...data.Option) *data.Store {
	t.Helper()
	s, err := data.New(catalog(t), f.dialect, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// openStore compiles the store under the form's dialect and opens a
// scripted pool over the responses.
func openStore(t *testing.T, f form, responses ...sqltest.Response) (*data.Store, *sqlate.DB, *sqltest.Recorder) {
	t.Helper()
	pool, rec := sqltest.Open(t, responses...)
	return newStore(t, f), sqlate.Wrap(pool, f.dialect), rec
}

// begin opens a transaction on db, rolled back when the test ends unless
// the test commits it first.
func begin(t *testing.T, db *sqlate.DB) *sqlate.Tx {
	t.Helper()
	tx, err := db.Begin(context.Background())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	return tx
}

// directoryColumns is the column list of a directory row, as the scripted
// driver must return it.
var directoryColumns = []string{"id", "parent_id", "name", "version", "created_at", "updated_at"}

// directoryResponse scripts one directory row; an empty parent is the
// root's NULL.
func directoryResponse(id, parent, name string, version int64) sqltest.Response {
	now := time.Now()
	var p driver.Value
	if parent != "" {
		p = parent
	}
	return sqltest.Response{Columns: directoryColumns, Rows: [][]driver.Value{{id, p, name, version, now, now}}}
}

// childResponse scripts one directory row under the root at version 1.
func childResponse(id, name string) sqltest.Response {
	return directoryResponse(id, blobfs.RootID, name, 1)
}

// noDirectory scripts a directory read that finds no row.
func noDirectory() sqltest.Response { return sqltest.Response{Columns: directoryColumns} }

// violation scripts a statement refused by constraint under class.
func violation(constraint string, class error) sqltest.Response {
	return sqltest.Response{Err: &sqlate.ConstraintError{Constraint: constraint, Class: class, Err: errors.New("duplicate key")}}
}

// The two spellings of one name, built from code points so that no editor
// can normalize the fixtures.
var (
	nfcName = "caf" + string(rune(0x00E9))  // é as one code point
	nfdName = "cafe" + string(rune(0x0301)) // e followed by a combining acute
)

// ops joins the recorder's calls into one line of op names.
func ops(rec *sqltest.Recorder) string {
	var out []string
	for _, op := range rec.Ops() {
		out = append(out, string(op))
	}
	return strings.Join(out, " ")
}

// TestNew proves the catalog builds with the two sources, every statement
// compiles, and the inventory: sixteen statements, all standard tier; the
// directory move, the file delete, and the two file holds the ones
// requiring a transaction; and the six returning commands, the directory
// ones reading their row back through directory_by_id and the file ones
// through file_by_id. The fallback's dialect renders no single-statement
// form and the returning dialect renders one per command.
func TestNew(t *testing.T) {
	want := []string{
		"complete_file", "create_directory", "create_file", "delete_directory", "delete_file",
		"directory_ancestors", "directory_by_id", "directory_by_name", "directory_is_within",
		"file_by_id", "file_by_name", "hold_file", "hold_file_at_version", "move_directory",
		"move_file", "purge_file",
	}
	returning := map[string]string{
		"create_directory": "directory_by_id", "move_directory": "directory_by_id",
		"create_file": "file_by_id", "complete_file": "file_by_id",
		"move_file": "file_by_id", "delete_file": "file_by_id",
	}
	txRequired := map[string]bool{
		"move_directory": true, "delete_file": true, "hold_file": true, "hold_file_at_version": true,
	}
	for _, f := range forms {
		t.Run(f.name, func(t *testing.T) {
			var names []string
			for _, st := range newStore(t, f).Statements() {
				names = append(names, st.Name())
				if st.Tier() != query.TierStandard {
					t.Errorf("%s is %s tier, want standard", st.Name(), st.Tier())
				}
				if st.TransactionRequired() != txRequired[st.Name()] {
					t.Errorf("%s: TransactionRequired = %v, want %v", st.Name(), st.TransactionRequired(), txRequired[st.Name()])
				}
				if reads := returning[st.Name()]; st.Reads() != reads {
					t.Errorf("%s reads %q, want %q", st.Name(), st.Reads(), reads)
				}
				if rendered := st.ReturningText() != ""; rendered != (f.single && returning[st.Name()] != "") {
					t.Errorf("%s: ReturningText = %q under %s", st.Name(), st.ReturningText(), f.name)
				}
			}
			if !slices.Equal(names, want) {
				t.Errorf("Statements = %v, want %v", names, want)
			}
		})
	}
}

// TestPatterns proves the published namespace and its inventory: the two
// column lists, standard tier and parameter-free, and nothing else.
func TestPatterns(t *testing.T) {
	var names []string
	for _, p := range catalog(t).Patterns() {
		if p.Namespace != data.Namespace {
			continue
		}
		names = append(names, p.Name)
		if p.Tier != query.TierStandard {
			t.Errorf("pattern %s.%s is %s tier, want standard", p.Namespace, p.Name, p.Tier)
		}
		if len(p.Slots) != 0 {
			t.Errorf("pattern %s.%s declares slots %v; published patterns are parameter-free", p.Namespace, p.Name, p.Slots)
		}
	}
	if got := strings.Join(names, " "); got != "directory_columns file_columns" {
		t.Errorf("blobfs patterns = %q, want %q", got, "directory_columns file_columns")
	}
}

// TestNewWithoutPatterns proves a catalog that lacks either namespace is
// refused with an error naming what is missing: the blobfs namespace
// before any statement compiles, and the query library's namespace, whose
// guard patterns the move includes, by the compile.
func TestNewWithoutPatterns(t *testing.T) {
	c, err := query.NewCatalog(query.Patterns())
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	_, err = data.New(c, sqltest.Dialect{})
	if err == nil || !strings.Contains(err.Error(), `"blobfs"`) {
		t.Fatalf("New without the blobfs namespace = %v, want an error naming the namespace", err)
	}
	c, err = query.NewCatalog(data.Patterns())
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	_, err = data.New(c, sqltest.Dialect{})
	if err == nil || !strings.Contains(err.Error(), `namespace "sql"`) {
		t.Fatalf("New without the query library's namespace = %v, want an error naming the sql namespace", err)
	}
}

// TestVerify proves Verify prepares every statement as authored, and each
// returning command's single-statement form beside it where the dialect
// renders one, without consuming a response.
func TestVerify(t *testing.T) {
	for _, c := range []struct {
		form form
		want int
	}{{forms[0], 16}, {forms[1], 22}} {
		t.Run(c.form.name, func(t *testing.T) {
			s, db, rec := openStore(t, c.form)
			if err := s.Verify(context.Background(), db); err != nil {
				t.Fatalf("Verify: %v", err)
			}
			prepared := rec.SQL(sqltest.OpPrepare)
			if len(prepared) != c.want {
				t.Errorf("Verify prepared %d statements, want %d", len(prepared), c.want)
			}
			returning := 0
			for _, text := range prepared {
				if strings.Contains(text, "RETURNING") {
					returning++
				}
			}
			if returning != c.want-16 {
				t.Errorf("Verify prepared %d single-statement forms, want %d", returning, c.want-16)
			}
		})
	}
}

// TestStandardTreeLock proves the baseline's tree lock: it reports that it
// does not serialize, and inside a transaction it runs no statement at
// all.
func TestStandardTreeLock(t *testing.T) {
	s, db, rec := openStore(t, fallback)
	ctx := context.Background()
	if s.Directories.Serializes() {
		t.Error("Serializes = true, want false for the baseline")
	}
	tx := begin(t, db)
	if err := s.Directories.LockTree(ctx, tx); err != nil {
		t.Errorf("LockTree = %v, want nil", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := ops(rec); got != "begin commit" {
		t.Errorf("the baseline's LockTree ran %q, want only the begin and the commit", got)
	}
}

// lockOverride is a consumer's variant that embeds the baseline and
// overrides the tree lock alone.
type lockOverride struct {
	data.Variant
	locked int
}

func (v *lockOverride) LockTree(context.Context, *sqlate.Tx) error {
	v.locked++
	return nil
}

func (*lockOverride) Serializes() bool { return true }

// failingLock is a variant whose lock fails, to prove Move stops there.
type failingLock struct{ data.Variant }

var errLock = errors.New("lock refused")

func (failingLock) LockTree(context.Context, *sqlate.Tx) error { return errLock }

// TestConsumerVariantSwapsOneMethod proves a consumer-supplied variant needs
// no fork: a wrapper that embeds the baseline built with NewStandard and
// overrides the lock is handed to New through WithVariant, the store runs
// the override, and path resolution still runs as the baseline does. The
// wrapper compiled nothing, so the inventory is the package's own. A lock
// that fails stops a move before any statement.
func TestConsumerVariantSwapsOneMethod(t *testing.T) {
	ctx := context.Background()
	base, err := data.NewStandard(catalog(t), sqltest.Dialect{})
	if err != nil {
		t.Fatalf("NewStandard: %v", err)
	}
	v := &lockOverride{Variant: base}
	s := newStore(t, fallback, data.WithVariant(v))
	pool, rec := sqltest.Open(t, childResponse("A", "a"), childResponse("B", "b"))
	db := sqlate.Wrap(pool, sqltest.Dialect{})
	if !s.Directories.Serializes() {
		t.Error("Serializes = false, want the wrapper's true")
	}
	tx := begin(t, db)
	if err := s.Directories.LockTree(ctx, tx); err != nil || v.locked != 1 {
		t.Errorf("LockTree = %v with %d wrapper calls, want nil and one call", err, v.locked)
	}
	if d, err := s.Directories.FindByPath(ctx, db, "A", "b"); err != nil || d.ID != "B" {
		t.Errorf("FindByPath = %+v, %v, want the baseline's walk through the wrapper", d, err)
	}
	if n := len(rec.SQL(sqltest.OpQuery)); n != 2 {
		t.Errorf("the walk ran %d queries, want the baseline's 2", n)
	}
	if n := len(s.Statements()); n != 16 {
		t.Errorf("Statements() lists %d, want the persistence package's 16", n)
	}

	s = newStore(t, fallback, data.WithVariant(failingLock{Variant: base}))
	pool, rec = sqltest.Open(t)
	tx = begin(t, sqlate.Wrap(pool, sqltest.Dialect{}))
	if _, err := s.Directories.Move(ctx, tx, "D", "P", "d", 1); !errors.Is(err, errLock) {
		t.Errorf("Move under a failing lock = %v, want the lock's error", err)
	}
	if got := ops(rec); got != "begin" {
		t.Errorf("the move ran %q after its lock failed", got)
	}
}
