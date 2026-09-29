package data

import (
	"context"
	"errors"
	"slices"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
)

// List reads one page, by number, of the directories under parentID,
// under req's filters, sort, and total mode, hiding deleting directories
// unless IncludeDeleting is given. The declared fields are id, parent_id,
// name, status, version, created_at, and updated_at. List of blobfs.RootID
// reads the depth-one directories; a parent that does not exist lists
// empty. See Listings in docs/features.md.
//
// Refusals: an error matching query.ErrDirectives before any SQL;
// blobfs.ErrDeleting for a deleting parent without IncludeDeleting.
func (d *Directories) List(ctx context.Context, sess sqlate.Session, parentID string, req query.Directives, page query.Page, opts ...ListOption) (_ query.Collection[blobfs.Directory], err error) {
	defer wrap(&err, "list directories under %s", parentID)
	return d.list.list(ctx, sess, parentID, req, page, opts)
}

// Continue reads the size directories under parentID past after, the Next
// of an earlier page of this listing, called the same way; the total and
// the next cursor are as List reports them. A sort by parent_id, the
// nullable field, issues no cursor.
//
// Refusals: List's; a query.CursorError before any SQL for a cursor
// edited, issued elsewhere, or under other filters or another sort.
func (d *Directories) Continue(ctx context.Context, sess sqlate.Session, parentID string, req query.Directives, after query.Cursor, size int, opts ...ListOption) (_ query.Collection[blobfs.Directory], err error) {
	defer wrap(&err, "continue directories under %s", parentID)
	return d.list.cont(ctx, sess, parentID, req, after, size, opts)
}

// Listing is the interface of the two listings, Directories and Files,
// each anchored on one directory's id: List reads a page by number, and
// Continue reads the page past a cursor an earlier page returned. A
// consumer that reads either listing the same way takes a Listing of the
// row type.
type Listing[T any] interface {
	List(ctx context.Context, sess sqlate.Session, id string, req query.Directives, page query.Page, opts ...ListOption) (query.Collection[T], error)
	Continue(ctx context.Context, sess sqlate.Session, id string, req query.Directives, after query.Cursor, size int, opts ...ListOption) (query.Collection[T], error)
}

var (
	_ Listing[blobfs.Directory] = (*Directories)(nil)
	_ Listing[blobfs.File]      = (*Files)(nil)
)

// listing is one of the two listings: a projection anchored on one
// directory by the parameter anchor, whose rows spell the deleting status
// as deleting. Its errors are bare, for the exported method to name.
type listing[T any] struct {
	projection query.Projection[T]
	anchor     string
	deleting   string
	dirs       directoryReads
}

// list reads one page by number of the listing anchored on id. See
// Directories.List and Files.List.
func (l listing[T]) list(ctx context.Context, sess sqlate.Session, id string, req query.Directives, page query.Page, opts []ListOption) (query.Collection[T], error) {
	req, include := l.directives(req, opts)
	c, err := l.projection.List(ctx, sess, req, page, query.With(l.anchor, id))
	if err == nil && !include {
		err = l.dirs.listable(ctx, sess, id)
	}
	if err != nil {
		return query.Collection[T]{}, err
	}
	return c, nil
}

// cont reads the size rows past after of the listing anchored on id. See
// Directories.Continue and Files.Continue.
func (l listing[T]) cont(ctx context.Context, sess sqlate.Session, id string, req query.Directives, after query.Cursor, size int, opts []ListOption) (query.Collection[T], error) {
	req, include := l.directives(req, opts)
	c, err := l.projection.Continue(ctx, sess, req, after, size, query.With(l.anchor, id))
	if err == nil && !include {
		err = l.dirs.listable(ctx, sess, id)
	}
	if err != nil {
		return query.Collection[T]{}, err
	}
	return c, nil
}

// directives returns the directives the listing runs, with the status
// filter appended unless IncludeDeleting was given, and whether it was.
// The caller's slice is never appended to in place.
func (l listing[T]) directives(req query.Directives, opts []ListOption) (query.Directives, bool) {
	var o listOptions
	for _, opt := range opts {
		opt(&o)
	}
	if o.includeDeleting {
		return req, true
	}
	req.Filters = append(slices.Clip(req.Filters), query.Filter{Field: "status", Op: query.OpNe, Value: l.deleting})
	return req, false
}

// listable refuses a listing anchored on a deleting directory with
// blobfs.ErrDeleting; a directory that does not exist is no error.
func (r directoryReads) listable(ctx context.Context, sess sqlate.Session, id string) error {
	if err := r.active(ctx, sess, id); err != nil && !errors.Is(err, blobfs.ErrNotFound) {
		return err
	}
	return nil
}
