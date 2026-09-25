package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
)

// List reads one page of the directories whose parent is parentID, by
// page number: the rows under req's filters and sort, the total under the
// same filters unless req declines it with query.TotalNone, whether a
// further page exists, and the cursor Continue takes to read it. The
// declared fields are id, parent_id, name, status, version, created_at,
// and updated_at; a filter or sort naming any other is refused by the
// query library before any SQL, with an error that unwraps to
// query.ErrDirectives, as is a page number or size below 1. The default
// order is by name, and name, unique under one parent, is the tie-breaker
// the library appends to every sort, so every order is total. The total
// is counted in the page's own statement, so it never disagrees with the
// page; an empty page after the first carries no count and reports
// query.NoTotal, as does a request that declines the count with
// query.TotalNone. Passing query.TotalNone is how a caller walks a large
// directory by cursor cheaply, since the counted read holds every filtered
// row before it pages.
//
// A deleting directory is hidden: the listing appends a filter on status
// after req's own, so neither the page nor the total holds one, and a
// parent that is itself deleting is blobfs.ErrDeleting, told by a read of
// the parent after the page. IncludeDeleting shows deleting directories
// and lists a deleting parent, without the read.
//
// The root has no parent and never appears in a listing: List of
// blobfs.RootID reads the depth-one directories. A parent that does not
// exist lists no rows and a total of zero. See Continue for when a page
// carries a cursor.
func (d *Directories) List(ctx context.Context, sess sqlate.Session, parentID string, req query.Directives, page query.Page, opts ...ListOption) (query.Collection[blobfs.Directory], error) {
	req, include := listing(req, string(blobfs.DirectoryStatusDeleting), opts)
	c, err := d.list.List(ctx, sess, req, page, query.With("parent_id", parentID))
	if err == nil && !include {
		err = listable(ctx, sess, d.byID, parentID)
	}
	if err != nil {
		return query.Collection[blobfs.Directory]{}, fmt.Errorf("data: list directories under %s: %w", parentID, err)
	}
	return c, nil
}

// Continue reads the size directories under parentID past after, the Next
// of an earlier page of this listing, under the same filters and sort
// that issued it; the total and the next cursor are as List reports them.
// A continued page's total counts the whole listing under the filters,
// not only the rows from the cursor on, and an empty continued page
// carries no count and reports query.NoTotal. A page carries a Next only
// when More is true and its sort can be continued: the terms up to the
// name tie-breaker run in one direction and name no nullable field, which
// here is parent_id. Any other sort pages by number only. A cursor the
// library did not issue, one edited, one issued by the file listing or
// under other filters or another sort, one issued with IncludeDeleting to
// a call without it or the reverse, and any cursor under a sort that
// cannot be continued are refused with a query.CursorError before any
// SQL. A cursor is a position in the name order, not a bookmark on the
// parent: the library does not record parentID in it. Deleting
// directories are hidden and a deleting parent refused as List hides and
// refuses them, so a directory marked after the cursor was issued is not
// on the pages past it.
func (d *Directories) Continue(ctx context.Context, sess sqlate.Session, parentID string, req query.Directives, after query.Cursor, size int, opts ...ListOption) (query.Collection[blobfs.Directory], error) {
	req, include := listing(req, string(blobfs.DirectoryStatusDeleting), opts)
	c, err := d.list.Continue(ctx, sess, req, after, size, query.With("parent_id", parentID))
	if err == nil && !include {
		err = listable(ctx, sess, d.byID, parentID)
	}
	if err != nil {
		return query.Collection[blobfs.Directory]{}, fmt.Errorf("data: continue directories under %s: %w", parentID, err)
	}
	return c, nil
}

// listing resolves a listing's options over req: the directives the
// listing runs, and whether IncludeDeleting was given. Without it the
// directives carry, after the caller's filters, one that hides the rows
// whose status is deleting, the listing's own spelling of it; the
// caller's slice is never appended to in place.
func listing(req query.Directives, deleting string, opts []ListOption) (query.Directives, bool) {
	var o listOptions
	for _, opt := range opts {
		opt(&o)
	}
	if o.includeDeleting {
		return req, true
	}
	req.Filters = append(slices.Clip(req.Filters), query.Filter{Field: "status", Op: query.OpNe, Value: deleting})
	return req, false
}

// listable reads the directory a listing without IncludeDeleting is
// anchored on, through byID in sess, once the page is read: a directory
// that is deleting is blobfs.ErrDeleting, since its branch is being
// removed and a listing hides what is in it. A directory that does not
// exist is no error: its listing is empty.
func listable(ctx context.Context, sess sqlate.Session, byID query.Rows[blobfs.Directory], id string) error {
	dir, err := byID.One(ctx, sess, query.Args{"id": id})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("read the directory %s: %w", id, err)
	case !dir.Status.Mutable():
		return fmt.Errorf("the directory %s is %s: %w", id, dir.Status, blobfs.ErrDeleting)
	}
	return nil
}
