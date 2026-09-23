package data

import (
	"context"
	"fmt"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
)

// List reads one page of the directories whose parent is parentID, by
// page number: the rows under req's filters and sort, the total under the
// same filters unless req declines it with query.TotalNone, whether a
// further page exists, and the cursor Continue takes to read it. The
// declared fields are id, parent_id, name, version, created_at, and
// updated_at; a filter or sort naming any other is refused by the query
// library before any SQL, with an error that unwraps to
// query.ErrDirectives, as is a page number or size below 1. The default
// order is by name, and name, unique under one parent, is the tie-breaker
// the library appends to every sort, so every order is total.
//
// The root has no parent and never appears in a listing: List of
// blobfs.RootID reads the depth-one directories. A parent that does not
// exist lists no rows and a total of zero. See Continue for when a page
// carries a cursor.
func (d *Directories) List(ctx context.Context, sess sqlate.Session, parentID string, req query.Directives, page query.Page) (query.Collection[blobfs.Directory], error) {
	c, err := d.list.List(ctx, sess, req, page, query.With("parent_id", parentID))
	if err != nil {
		return query.Collection[blobfs.Directory]{}, fmt.Errorf("data: list directories under %s: %w", parentID, err)
	}
	return c, nil
}

// Continue reads the size directories under parentID past after, the Next
// of an earlier page of this listing, under the same filters and sort
// that issued it; the total and the next cursor are as List reports them.
// A page carries a Next only when More is true and its sort can be
// continued: the terms up to the name tie-breaker run in one direction
// and name no nullable field, which here is parent_id. Any other sort
// pages by number only. A cursor the library did not issue, one edited,
// one issued by the file listing or under other filters or another sort,
// and any cursor under a sort that cannot be continued are refused with a
// query.CursorError before any SQL. A cursor is a position in the name
// order, not a bookmark on the parent: the library does not record
// parentID in it.
func (d *Directories) Continue(ctx context.Context, sess sqlate.Session, parentID string, req query.Directives, after query.Cursor, size int) (query.Collection[blobfs.Directory], error) {
	c, err := d.list.Continue(ctx, sess, req, after, size, query.With("parent_id", parentID))
	if err != nil {
		return query.Collection[blobfs.Directory]{}, fmt.Errorf("data: continue directories under %s: %w", parentID, err)
	}
	return c, nil
}
