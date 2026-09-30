package data

import (
	"context"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
)

// List reads one page, by number, of the files in directoryID under req,
// hiding deleting ones unless IncludeDeleting is given. Refusals:
// query.ErrDirectives before any SQL; blobfs.ErrDeleting for a deleting
// directory. See Listings in docs/features.md.
func (f *Files) List(ctx context.Context, sess sqlate.Session, directoryID string, req query.Directives, page query.Page, opts ...ListOption) (_ query.Collection[blobfs.File], err error) {
	defer wrap(&err, "list files in %s", directoryID)
	return f.list.list(ctx, sess, directoryID, req, page, opts)
}

// Continue reads the size files in directoryID past after, the Next of an
// earlier page of the listing called the same way. Refusals: List's, and a
// query.CursorError before any SQL. See Listings in docs/features.md.
func (f *Files) Continue(ctx context.Context, sess sqlate.Session, directoryID string, req query.Directives, after query.Cursor, size int, opts ...ListOption) (_ query.Collection[blobfs.File], err error) {
	defer wrap(&err, "continue files in %s", directoryID)
	return f.list.cont(ctx, sess, directoryID, req, after, size, opts)
}
