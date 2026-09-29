package datatest

import (
	"database/sql/driver"

	"github.com/standards-lab/sqlate/sqltest"

	"github.com/standards-lab/blobfs"
)

// The column lists of blobfs's rows, in the order the published patterns
// blobfs.file_columns and blobfs.directory_columns select them and the
// store's statements scan them.
var (
	fileColumns      = []string{"id", "directory_id", "name", "status", "key", "size", "content_type", "etag", "version", "created_at", "updated_at"}
	directoryColumns = []string{"id", "parent_id", "name", "status", "version", "created_at", "updated_at"}
)

// FileRows scripts, for sqltest's driver, a read of blobfs_file rows, one
// row per file, in the columns the store's statements scan; a nil Size or
// ETag is NULL. With no file it is a read that finds no row. It is for a
// consumer's scripted tests of code over the store, beside the suite.
func FileRows(files ...blobfs.File) sqltest.Response {
	r := sqltest.Response{Columns: fileColumns}
	for _, f := range files {
		var size, etag driver.Value
		if f.Size != nil {
			size = *f.Size
		}
		if f.ETag != nil {
			etag = *f.ETag
		}
		r.Rows = append(r.Rows, []driver.Value{f.ID, f.DirectoryID, f.Name, string(f.Status), f.Key, size, f.ContentType, etag, f.Version, f.CreatedAt, f.UpdatedAt})
	}
	return r
}

// DirectoryRows scripts, for sqltest's driver, a read of blobfs_directory
// rows, one row per directory, in the columns the store's statements scan;
// a nil ParentID is the root's NULL. With no directory it is a read that
// finds no row.
func DirectoryRows(dirs ...blobfs.Directory) sqltest.Response {
	r := sqltest.Response{Columns: directoryColumns}
	for _, d := range dirs {
		var parent driver.Value
		if d.ParentID != nil {
			parent = *d.ParentID
		}
		r.Rows = append(r.Rows, []driver.Value{d.ID, parent, d.Name, string(d.Status), d.Version, d.CreatedAt, d.UpdatedAt})
	}
	return r
}
