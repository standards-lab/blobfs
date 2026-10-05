package data_test

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/standards-lab/sqlate/sqltest"

	"github.com/standards-lab/blobfs"
)

// The columns of blobfs's rows as its statements select them.
var (
	directoryScanColumns = []string{"id", "parent_id", "name", "status", "version", "created_at", "updated_at"}
	fileScanColumns      = []string{"id", "directory_id", "name", "status", "key", "size", "content_type", "etag", "version", "created_at", "updated_at"}
)

// TestEntitiesScan checks a row read through the store lands in its
// entity column by column, and that the columns that may be NULL, a
// directory's parent_id and a file's size and etag, scan a NULL as nil and
// a value as a pointer to it.
func TestEntitiesScan(t *testing.T) {
	ctx := context.Background()
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	updated := created.Add(time.Hour)
	parent, size, etag := "P", int64(42), `"abc"`

	for _, c := range []struct {
		name string
		row  []driver.Value
		want blobfs.Directory
	}{
		{"the root", []driver.Value{blobfs.RootID, nil, "/", "active", int64(1), created, updated},
			blobfs.Directory{ID: blobfs.RootID, Name: "/", Status: blobfs.DirectoryStatusActive, Version: 1, CreatedAt: created, UpdatedAt: updated}},
		{"a child", []driver.Value{"D", parent, "docs", "deleting", int64(3), created, updated},
			blobfs.Directory{ID: "D", ParentID: &parent, Name: "docs", Status: blobfs.DirectoryStatusDeleting, Version: 3, CreatedAt: created, UpdatedAt: updated}},
	} {
		s, db, _ := openStore(t, fallback, sqltest.Response{Columns: directoryScanColumns, Rows: [][]driver.Value{c.row}})
		got, err := s.Directories.Find(ctx, db, c.want.ID)
		if err != nil {
			t.Fatalf("Find %s: %v", c.name, err)
		}
		if got.ID != c.want.ID || !samePointer(got.ParentID, c.want.ParentID) || got.Name != c.want.Name || got.Status != c.want.Status ||
			got.Version != c.want.Version || !got.CreatedAt.Equal(c.want.CreatedAt) || !got.UpdatedAt.Equal(c.want.UpdatedAt) {
			t.Errorf("%s scanned as %+v (parent %v), want %+v (parent %v)", c.name, got, deref(got.ParentID), c.want, deref(c.want.ParentID))
		}
	}

	for _, c := range []struct {
		name string
		row  []driver.Value
		want blobfs.File
	}{
		{"a pending file", []driver.Value{"F", "D", "a.txt", "pending", "F/a.txt", nil, "text/plain", nil, int64(1), created, updated},
			blobfs.File{ID: "F", DirectoryID: "D", Name: "a.txt", Status: blobfs.StatusPending, Key: "F/a.txt", ContentType: "text/plain", Version: 1, CreatedAt: created, UpdatedAt: updated}},
		{"an available file", []driver.Value{"F", "D", "a.txt", "available", "F/a.txt", size, "text/plain", etag, int64(2), created, updated},
			blobfs.File{ID: "F", DirectoryID: "D", Name: "a.txt", Status: blobfs.StatusAvailable, Key: "F/a.txt", Size: &size, ContentType: "text/plain", ETag: &etag, Version: 2, CreatedAt: created, UpdatedAt: updated}},
	} {
		s, db, _ := openStore(t, fallback, sqltest.Response{Columns: fileScanColumns, Rows: [][]driver.Value{c.row}})
		got, err := s.Files.Find(ctx, db, c.want.ID)
		if err != nil {
			t.Fatalf("Find %s: %v", c.name, err)
		}
		if got.ID != c.want.ID || got.DirectoryID != c.want.DirectoryID || got.Name != c.want.Name || got.Status != c.want.Status || got.Key != c.want.Key ||
			!samePointer(got.Size, c.want.Size) || got.ContentType != c.want.ContentType || !samePointer(got.ETag, c.want.ETag) ||
			got.Version != c.want.Version || !got.CreatedAt.Equal(c.want.CreatedAt) || !got.UpdatedAt.Equal(c.want.UpdatedAt) {
			t.Errorf("%s scanned as %+v (size %v, etag %v), want %+v (size %v, etag %v)",
				c.name, got, deref(got.Size), deref(got.ETag), c.want, deref(c.want.Size), deref(c.want.ETag))
		}
	}
}

// samePointer reports whether a and b are both nil or point to equal
// values.
func samePointer[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// deref is the value p points to, or nil, for a message.
func deref[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}
