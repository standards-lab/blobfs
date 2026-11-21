package datatest

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/standards-lab/sqlate"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data"
)

const (
	precomposedEAcute = '\u00E9'
	combiningAcute    = '\u0301'
)

var (
	composed   = "caf" + string(precomposedEAcute)
	decomposed = "cafe" + string(combiningAcute)
)

type acceptAll struct{}

func (acceptAll) ValidateKey(string) error { return nil }

var errRefusedKey = errors.New("datatest: the store refuses every key")

type refuseAll struct{}

func (refuseAll) ValidateKey(string) error { return errRefusedKey }

var object = blobfs.Object{Size: 42, ContentType: "text/plain", ETag: `"etag"`}

func name(n string) string {
	return strings.NewReplacer("/", "-", "\\", "-").Replace(n)
}

func (s *suite) mkdir(t *testing.T, n string) blobfs.Directory {
	t.Helper()
	return s.mkdirUnder(t, blobfs.RootID, n)
}

func (s *suite) mkdirUnder(t *testing.T, parentID, n string) blobfs.Directory {
	t.Helper()
	d, err := s.store.Directories.Create(s.ctx, s.db, parentID, name(n), data.CreateOptions{})
	if err != nil {
		t.Fatalf("Directories.Create(%q): %v", n, err)
	}
	return d
}

func (s *suite) directory(t *testing.T, id string) blobfs.Directory {
	t.Helper()
	d, err := s.store.Directories.Find(s.ctx, s.db, id)
	if err != nil {
		t.Fatalf("Directories.Find(%s): %v", id, err)
	}
	return d
}

func (s *suite) file(t *testing.T, id string) blobfs.File {
	t.Helper()
	f, err := s.store.Files.Find(s.ctx, s.db, id)
	if err != nil {
		t.Fatalf("Files.Find(%s): %v", id, err)
	}
	return f
}

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

func (s *suite) insertDirectory(t *testing.T, parent, n string) string {
	t.Helper()
	id := blobfs.NewID()
	p := s.db.Dialect().Placeholder
	text := fmt.Sprintf("INSERT INTO blobfs_directory (id, parent_id, name) VALUES (%s, %s, %s)", p(1), p(2), p(3))
	if _, err := s.db.ExecContext(s.ctx, text, id, parent, n); err != nil {
		t.Fatalf("insert directory %s: %v", n, err)
	}
	return id
}

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

func (s *suite) exec(t *testing.T, text string, args ...any) {
	t.Helper()
	if _, err := s.db.ExecContext(s.ctx, text, args...); err != nil {
		t.Fatalf("%s: %v", text, err)
	}
}

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

func (s *suite) beginTx(t *testing.T, opts *sql.TxOptions) *sqlate.Tx {
	t.Helper()
	tx, err := s.db.Begin(s.ctx, opts)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	return tx
}

func (s *suite) wantPath(t *testing.T, id, want string) {
	t.Helper()
	got, err := s.store.Directories.Path(s.ctx, s.db, id)
	if err != nil || got != want {
		t.Errorf("Directories.Path(%s) = %q, %v, want %q", id, got, err, want)
	}
}

const referenceConstraint = "datatest_fk_reference_file"

const ownerConstraint = "datatest_fk_owner_directory"

func (s *suite) createFileReferences(t *testing.T) {
	t.Helper()
	if s.fileReferenceTableCreated {
		return
	}
	s.fileReferenceTableCreated = true
	s.exec(t, "CREATE TABLE datatest_reference AS SELECT f.id AS file_id FROM blobfs_file f WHERE 1 = 0")
	s.exec(t, "ALTER TABLE datatest_reference ADD CONSTRAINT "+referenceConstraint+" FOREIGN KEY (file_id) REFERENCES blobfs_file (id)")
}

func (s *suite) createDirectoryReferences(t *testing.T) {
	t.Helper()
	if s.directoryReferenceTableCreated {
		return
	}
	s.directoryReferenceTableCreated = true
	s.exec(t, "CREATE TABLE datatest_owner AS SELECT d.id AS directory_id FROM blobfs_directory d WHERE 1 = 0")
	s.exec(t, "ALTER TABLE datatest_owner ADD CONSTRAINT "+ownerConstraint+" FOREIGN KEY (directory_id) REFERENCES blobfs_directory (id)")
}

func (s *suite) reference(t *testing.T, sess sqlate.Session, id string) {
	t.Helper()
	if _, err := sess.ExecContext(s.ctx, "INSERT INTO datatest_reference (file_id) VALUES ("+s.db.Dialect().Placeholder(1)+")", id); err != nil {
		t.Fatalf("reference file %s: %v", id, err)
	}
}

func (s *suite) unreference(t *testing.T, id string) {
	t.Helper()
	s.exec(t, "DELETE FROM datatest_reference WHERE file_id = "+s.db.Dialect().Placeholder(1), id)
}

func (s *suite) references(t *testing.T, id string) int {
	t.Helper()
	return s.count(t, s.db, "SELECT COUNT(*) FROM datatest_reference WHERE file_id = "+s.db.Dialect().Placeholder(1), id)
}

func equalDirectory(a, b blobfs.Directory) bool {
	return a.ID == b.ID && equalString(a.ParentID, b.ParentID) && a.Name == b.Name && a.Status == b.Status &&
		a.Version == b.Version && a.CreatedAt.Equal(b.CreatedAt) && a.UpdatedAt.Equal(b.UpdatedAt)
}

func equalFile(a, b blobfs.File) bool {
	return a.ID == b.ID && a.DirectoryID == b.DirectoryID && a.Name == b.Name && a.Status == b.Status &&
		a.Key == b.Key && equalInt(a.Size, b.Size) && a.ContentType == b.ContentType && equalString(a.ETag, b.ETag) &&
		a.Version == b.Version && a.CreatedAt.Equal(b.CreatedAt) && a.UpdatedAt.Equal(b.UpdatedAt)
}

func wantUTC(t *testing.T, what string, createdAt, updatedAt time.Time) {
	t.Helper()
	if createdAt.Location() != time.UTC || updatedAt.Location() != time.UTC {
		t.Errorf("%s returned CreatedAt in %v and UpdatedAt in %v, want both in UTC", what, createdAt.Location(), updatedAt.Location())
	}
}

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

func wantRefusal(t *testing.T, err, want error, constraint string) {
	t.Helper()
	if constraint != "" {
		wantViolation(t, err, want, constraint)
		return
	}
	var ve *blobfs.ViolationError
	if !errors.Is(err, want) || errors.As(err, &ve) {
		t.Errorf("got %v, want %v and no constraint violation", err, want)
	}
}

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
