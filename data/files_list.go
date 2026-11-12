package data

import (
	"context"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
)

// List reads one page, by number, of the files in directoryID under req,
// under opts. Refusals: query.ErrDirectives before any SQL;
// blobfs.ErrDeleting for a deleting directory unless opts includes
// deleting. See Listings in docs/features.md.
func (f *Files) List(ctx context.Context, sess sqlate.Session, directoryID string, req query.Directives, page query.Page, opts ListOptions) (_ query.Collection[blobfs.File], err error) {
	defer wrap(&err, "list files in %s", directoryID)
	return f.list.list(ctx, sess, directoryID, req, page, opts)
}

// Continue reads the page of files in directoryID past page.After, under
// opts. Refusals: List's, and a query.CursorError before any SQL. See
// Listings in docs/features.md.
func (f *Files) Continue(ctx context.Context, sess sqlate.Session, directoryID string, req query.Directives, page CursorPage, opts ListOptions) (_ query.Collection[blobfs.File], err error) {
	defer wrap(&err, "continue files in %s", directoryID)
	return f.list.cont(ctx, sess, directoryID, req, page, opts)
}
