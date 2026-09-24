package datatest

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/standards-lab/sqlate"

	"github.com/standards-lab/blobfs"
)

// The two spellings of one name, built from code points so that no editor
// can normalize the fixtures: é as one code point, and e followed by a
// combining acute accent.
var (
	composed   = "caf" + string(rune(0x00E9))
	decomposed = "cafe" + string(rune(0x0301))
)

// acceptAll is the key validator the write groups pass: every key is
// accepted, since no object store is involved.
type acceptAll struct{}

func (acceptAll) ValidateKey(string) error { return nil }

// errRefusedKey is what refuseAll reports.
var errRefusedKey = errors.New("datatest: the store refuses every key")

// refuseAll is a key validator that refuses every key, for the check that
// a refused key stops a write before any SQL.
type refuseAll struct{}

func (refuseAll) ValidateKey(string) error { return errRefusedKey }

// object is what the write groups report as the stored object.
var object = blobfs.Object{Size: 42, ContentType: "text/plain", ETag: `"etag"`}

// name makes a subtest name usable as a directory name: a slash or a
// backslash in it would be refused or rewritten.
func name(n string) string {
	return strings.NewReplacer("/", "-", "\\", "-").Replace(n)
}

// mkdir creates a directory under the root through the store under test,
// named from n.
func (s *suite) mkdir(t *testing.T, n string) blobfs.Directory {
	t.Helper()
	return s.mkdirUnder(t, blobfs.RootID, n)
}

// mkdirUnder creates a directory under a parent through the store under
// test, named from n.
func (s *suite) mkdirUnder(t *testing.T, parentID, n string) blobfs.Directory {
	t.Helper()
	d, err := s.store.Directories.Create(s.ctx, s.db, parentID, name(n))
	if err != nil {
		t.Fatalf("Directories.Create(%q): %v", n, err)
	}
	return d
}

// directory reads the directory row by id through the store, on the pool.
func (s *suite) directory(t *testing.T, id string) blobfs.Directory {
	t.Helper()
	d, err := s.store.Directories.Find(s.ctx, s.db, id)
	if err != nil {
		t.Fatalf("Directories.Find(%s): %v", id, err)
	}
	return d
}

// file reads the file row by id through the store, on the pool.
func (s *suite) file(t *testing.T, id string) blobfs.File {
	t.Helper()
	f, err := s.store.Files.Find(s.ctx, s.db, id)
	if err != nil {
		t.Fatalf("Files.Find(%s): %v", id, err)
	}
	return f
}

// insertFile inserts a file row in status through plain SQL, with the
// dialect's placeholders, and returns its id. An available row carries a
// size; a pending or deleting one carries none, as the protocols leave
// them.
func (s *suite) insertFile(t *testing.T, dir, n string, status blobfs.Status) string {
	t.Helper()
	id := blobfs.NewID()
	var size *int64
	if status == blobfs.StatusAvailable {
		v := int64(len(n))
		size = &v
	}
	p := s.db.Dialect().Placeholder
	text := fmt.Sprintf(
		"INSERT INTO blobfs_file (id, directory_id, name, status, key, content_type, size) VALUES (%s, %s, %s, %s, %s, 'text/plain', %s)",
		p(1), p(2), p(3), p(4), p(5), p(6))
	if _, err := s.db.ExecContext(s.ctx, text, id, dir, n, status.String(), id+"/"+blobfs.SanitizeFilename(n), size); err != nil {
		t.Fatalf("insert file %s: %v", n, err)
	}
	return id
}

// insertFileAt inserts an available file row at a fixed created_at and
// version through plain SQL and returns its id, so a sort by either has
// ties the key must break.
func (s *suite) insertFileAt(t *testing.T, dir, n string, at time.Time, version int64) string {
	t.Helper()
	id := blobfs.NewID()
	p := s.db.Dialect().Placeholder
	text := fmt.Sprintf(
		"INSERT INTO blobfs_file (id, directory_id, name, status, key, content_type, size, version, created_at, updated_at) VALUES (%s, %s, %s, 'available', %s, 'text/plain', 1, %s, %s, %s)",
		p(1), p(2), p(3), p(4), p(5), p(6), p(7))
	if _, err := s.db.ExecContext(s.ctx, text, id, dir, n, id+"/"+n, version, at, at); err != nil {
		t.Fatalf("insert file %s: %v", n, err)
	}
	return id
}

// exec runs a statement of the suite's own through the pool, with its
// placeholders written as the dialect's.
func (s *suite) exec(t *testing.T, text string, args ...any) {
	t.Helper()
	if _, err := s.db.ExecContext(s.ctx, text, args...); err != nil {
		t.Fatalf("%s: %v", text, err)
	}
}

// count runs a one-value count query through sess.
func (s *suite) count(t *testing.T, sess sqlate.Session, text string, args ...any) int {
	t.Helper()
	rows, err := sess.QueryContext(s.ctx, text, args...)
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	defer func() { _ = rows.Close() }()
	var n int
	if !rows.Next() {
		t.Fatalf("%s: no row: %v", text, rows.Err())
	}
	if err := rows.Scan(&n); err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return n
}

// column runs a one-column query and returns its values as text, in the
// query's order.
func (s *suite) column(t *testing.T, text string, args ...any) []string {
	t.Helper()
	rows, err := s.db.QueryContext(s.ctx, text, args...)
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("%s: %v", text, err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return out
}

// beginTx opens a transaction on the pool, failing the test when it
// cannot.
func (s *suite) beginTx(t *testing.T, opts ...sqlate.TxOption) *sqlate.Tx {
	t.Helper()
	tx, err := s.db.Begin(s.ctx, opts...)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	return tx
}

// wantPath checks Directories.Path of id through the store under test.
func (s *suite) wantPath(t *testing.T, id, want string) {
	t.Helper()
	got, err := s.store.Directories.Path(s.ctx, s.db, id)
	if err != nil || got != want {
		t.Errorf("Directories.Path(%s) = %q, %v, want %q", id, got, err, want)
	}
}

// referenceConstraint is the name of the suite's own foreign key into
// blobfs_file, which stands in for a consumer's.
const referenceConstraint = "datatest_fk_reference_file"

// ownerConstraint is the name of the suite's own foreign key into
// blobfs_directory, which stands in for a consumer's.
const ownerConstraint = "datatest_fk_owner_directory"

// createFileReferences creates the suite's file reference table, once per
// run: one column of the same type as blobfs_file.id, taken from the
// migrated table so the DDL names no engine type, under a foreign key to
// blobfs_file.
func (s *suite) createFileReferences(t *testing.T) {
	t.Helper()
	if s.fileReferences {
		return
	}
	s.fileReferences = true
	s.exec(t, "CREATE TABLE datatest_reference AS SELECT f.id AS file_id FROM blobfs_file f WHERE 1 = 0")
	s.exec(t, "ALTER TABLE datatest_reference ADD CONSTRAINT "+referenceConstraint+" FOREIGN KEY (file_id) REFERENCES blobfs_file (id)")
}

// createDirectoryReferences creates the suite's directory reference table,
// once per run, the same way.
func (s *suite) createDirectoryReferences(t *testing.T) {
	t.Helper()
	if s.directoryReferences {
		return
	}
	s.directoryReferences = true
	s.exec(t, "CREATE TABLE datatest_owner AS SELECT d.id AS directory_id FROM blobfs_directory d WHERE 1 = 0")
	s.exec(t, "ALTER TABLE datatest_owner ADD CONSTRAINT "+ownerConstraint+" FOREIGN KEY (directory_id) REFERENCES blobfs_directory (id)")
}

// reference inserts a row that references the file with id, through sess.
func (s *suite) reference(t *testing.T, sess sqlate.Session, id string) {
	t.Helper()
	if _, err := sess.ExecContext(s.ctx, "INSERT INTO datatest_reference (file_id) VALUES ("+s.db.Dialect().Placeholder(1)+")", id); err != nil {
		t.Fatalf("reference file %s: %v", id, err)
	}
}

// unreference removes the rows that reference the file with id.
func (s *suite) unreference(t *testing.T, id string) {
	t.Helper()
	s.exec(t, "DELETE FROM datatest_reference WHERE file_id = "+s.db.Dialect().Placeholder(1), id)
}

// references counts the rows that reference the file with id.
func (s *suite) references(t *testing.T, id string) int {
	t.Helper()
	return s.count(t, s.db, "SELECT COUNT(*) FROM datatest_reference WHERE file_id = "+s.db.Dialect().Placeholder(1), id)
}

// equalDirectory compares two rows field by field, timestamps by instant.
func equalDirectory(a, b blobfs.Directory) bool {
	return a.ID == b.ID && equalString(a.ParentID, b.ParentID) && a.Name == b.Name &&
		a.Version == b.Version && a.CreatedAt.Equal(b.CreatedAt) && a.UpdatedAt.Equal(b.UpdatedAt)
}

// equalFile compares two rows field by field, timestamps by instant.
func equalFile(a, b blobfs.File) bool {
	return a.ID == b.ID && a.DirectoryID == b.DirectoryID && a.Name == b.Name && a.Status == b.Status &&
		a.Key == b.Key && equalInt(a.Size, b.Size) && a.ContentType == b.ContentType && equalString(a.ETag, b.ETag) &&
		a.Version == b.Version && a.CreatedAt.Equal(b.CreatedAt) && a.UpdatedAt.Equal(b.UpdatedAt)
}

// sameFileShape compares two file rows on every column that does not
// identify the row or carry a clock.
func sameFileShape(a, b blobfs.File) bool {
	return a.DirectoryID == b.DirectoryID && a.Status == b.Status && equalInt(a.Size, b.Size) &&
		a.ContentType == b.ContentType && equalString(a.ETag, b.ETag) && a.Version == b.Version
}

func equalInt(a, b *int64) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func equalString(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// wantViolation checks err is want as a blobfs.ViolationError over the
// named constraint, with the sqlate.ConstraintError reachable, and that
// its message names the sentinel and the constraint and nothing the
// driver said.
func wantViolation(t *testing.T, err, want error, constraint string) {
	t.Helper()
	var ve *blobfs.ViolationError
	var ce *sqlate.ConstraintError
	if !errors.Is(err, want) || !errors.As(err, &ve) || ve.Constraint != constraint || !errors.As(err, &ce) || ce.Constraint != constraint {
		t.Errorf("got %v, want %v over the constraint %s", err, want, constraint)
		return
	}
	if suffix := want.Error() + " (constraint " + constraint + ")"; !strings.HasSuffix(err.Error(), suffix) || strings.Contains(err.Error(), ce.Err.Error()) {
		t.Errorf("the refusal reads %q, want a message ending with %q and none of the driver's text", err, suffix)
	}
}

// wantSameError checks the baseline refused the same way, in text, as the
// store under test did.
func wantSameError(t *testing.T, got, base error) {
	t.Helper()
	if got == nil || base == nil {
		t.Errorf("the store under test returned %v and the baseline %v; want both refused", got, base)
		return
	}
	if got.Error() != base.Error() {
		t.Errorf("the store under test refused with\n%v\nand the baseline with\n%v", got, base)
	}
}
