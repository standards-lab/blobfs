package data

import (
	"context"
	"fmt"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
)

// List reads one page of the files in the directory with directoryID,
// whatever their status, by page number: the rows under req's filters and
// sort, the total under the same filters unless req declines it with
// query.TotalNone, whether a further page exists, and the cursor Continue
// takes to read it. The declared fields are id, directory_id, name,
// status, size, content_type, etag, version, created_at, and updated_at;
// a filter or sort naming any other, the object key included, is refused
// by the query library before any SQL, with an error that unwraps to
// query.ErrDirectives, as is a page number or size below 1. A filter on
// status narrows the listing to one stage of the write and delete
// protocols. The default order is by name, and name, unique in one
// directory, is the tie-breaker the library appends to every sort, so
// every order is total. The total is counted in the page's own statement,
// so it never disagrees with the page; an empty page after the first
// carries no count and reports query.NoTotal, as does a request that
// declines the count with query.TotalNone. Passing query.TotalNone is how
// a caller walks a large directory by cursor cheaply, since the counted
// read holds every filtered row before it pages. A directory that does
// not exist lists no rows and a total of zero.
func (f *Files) List(ctx context.Context, sess sqlate.Session, directoryID string, req query.Directives, page query.Page) (query.Collection[blobfs.File], error) {
	c, err := f.list.List(ctx, sess, req, page, query.With("directory_id", directoryID))
	if err != nil {
		return query.Collection[blobfs.File]{}, fmt.Errorf("data: list files in %s: %w", directoryID, err)
	}
	return c, nil
}

// Continue reads the size files in directoryID past after, the Next of an
// earlier page of this listing, under the same filters and sort that
// issued it; the total and the next cursor are as List reports them. A
// continued page's total counts the whole listing under the filters, not
// only the rows from the cursor on, and an empty continued page carries
// no count and reports query.NoTotal. A page carries a Next only when
// More is true and its sort can be continued: the terms up to the name
// tie-breaker run in one direction and name no nullable field, which here
// is size or etag, null until a write completes. Any other sort pages by
// number only. A cursor the library did not issue, one edited, one issued
// by the directory listing or under other filters or another sort, and
// any cursor under a sort that cannot be continued are refused with a
// query.CursorError before any SQL. A cursor is a position in the order,
// not a bookmark on the directory: the library does not record
// directoryID in it.
func (f *Files) Continue(ctx context.Context, sess sqlate.Session, directoryID string, req query.Directives, after query.Cursor, size int) (query.Collection[blobfs.File], error) {
	c, err := f.list.Continue(ctx, sess, req, after, size, query.With("directory_id", directoryID))
	if err != nil {
		return query.Collection[blobfs.File]{}, fmt.Errorf("data: continue files in %s: %w", directoryID, err)
	}
	return c, nil
}
