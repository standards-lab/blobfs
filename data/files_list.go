package data

import (
	"context"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
)

// List reads one page, by number, of the files in directoryID, under req's
// filters, sort, and total mode, hiding deleting files unless
// IncludeDeleting is given. The declared fields are id, directory_id,
// name, status, size, content_type, etag, version, created_at, and
// updated_at; the object key is not one. A directory that does not exist
// lists empty. See Listings in docs/features.md.
//
// Refusals: an error matching query.ErrDirectives before any SQL;
// blobfs.ErrDeleting for a deleting directory without IncludeDeleting.
func (f *Files) List(ctx context.Context, sess sqlate.Session, directoryID string, req query.Directives, page query.Page, opts ...ListOption) (_ query.Collection[blobfs.File], err error) {
	defer wrap(&err, "list files in %s", directoryID)
	return f.list.list(ctx, sess, directoryID, req, page, opts)
}

// Continue reads the size files in directoryID past after, the Next of an
// earlier page of this listing, called the same way; the total and the
// next cursor are as List reports them. A sort by size or etag, the
// nullable fields, issues no cursor.
//
// Refusals: List's; a query.CursorError before any SQL for a cursor
// edited, issued elsewhere, or under other filters or another sort.
func (f *Files) Continue(ctx context.Context, sess sqlate.Session, directoryID string, req query.Directives, after query.Cursor, size int, opts ...ListOption) (_ query.Collection[blobfs.File], err error) {
	defer wrap(&err, "continue files in %s", directoryID)
	return f.list.cont(ctx, sess, directoryID, req, after, size, opts)
}
