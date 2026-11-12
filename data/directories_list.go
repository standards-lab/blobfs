package data

import (
	"context"
	"errors"
	"slices"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
)

// List reads one page, by number, of the directories under parentID under
// req, under opts. Refusals: query.ErrDirectives before any SQL;
// blobfs.ErrDeleting for a deleting parent unless opts includes deleting.
// See Listings in docs/features.md.
func (d *Directories) List(ctx context.Context, sess sqlate.Session, parentID string, req query.Directives, page query.Page, opts ListOptions) (_ query.Collection[blobfs.Directory], err error) {
	defer wrap(&err, "list directories under %s", parentID)
	return d.list.list(ctx, sess, parentID, req, page, opts)
}

// Continue reads the page of directories under parentID past page.After,
// under opts. Refusals: List's, and a query.CursorError before any SQL.
// See Listings in docs/features.md.
func (d *Directories) Continue(ctx context.Context, sess sqlate.Session, parentID string, req query.Directives, page CursorPage, opts ListOptions) (_ query.Collection[blobfs.Directory], err error) {
	defer wrap(&err, "continue directories under %s", parentID)
	return d.list.cont(ctx, sess, parentID, req, page, opts)
}

// Listing is the interface of the two listings, Directories and Files,
// each anchored on one directory's id: List reads a page by number, and
// Continue reads the page past a cursor an earlier page returned. A
// consumer that reads either listing the same way takes a Listing of the
// row type.
type Listing[T any] interface {
	List(ctx context.Context, sess sqlate.Session, id string, req query.Directives, page query.Page, opts ListOptions) (query.Collection[T], error)
	Continue(ctx context.Context, sess sqlate.Session, id string, req query.Directives, page CursorPage, opts ListOptions) (query.Collection[T], error)
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
func (l listing[T]) list(ctx context.Context, sess sqlate.Session, id string, req query.Directives, page query.Page, opts ListOptions) (query.Collection[T], error) {
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

// cont reads the page past page.After of the listing anchored on id. See
// Directories.Continue and Files.Continue.
func (l listing[T]) cont(ctx context.Context, sess sqlate.Session, id string, req query.Directives, page CursorPage, opts ListOptions) (query.Collection[T], error) {
	req, include := l.directives(req, opts)
	c, err := l.projection.Continue(ctx, sess, req, page.After, page.Size, query.With(l.anchor, id))
	if err == nil && !include {
		err = l.dirs.listable(ctx, sess, id)
	}
	if err != nil {
		return query.Collection[T]{}, err
	}
	return c, nil
}

// directives returns the directives the listing runs, with the status
// filter appended unless opts includes deleting rows, and whether it does.
// The caller's slice is never appended to in place.
func (l listing[T]) directives(req query.Directives, opts ListOptions) (query.Directives, bool) {
	if opts.IncludeDeleting {
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
