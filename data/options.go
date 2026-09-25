package data

import (
	"context"
	"time"

	"github.com/standards-lab/sqlate"

	"github.com/standards-lab/blobfs"
)

// Option configures New beyond its required arguments.
type Option func(*options)

// options collects what the options set.
type options struct {
	engine Engine
}

// WithEngine makes the store forward its variation points to the variant
// e builds instead of Standard. New calls e once, with its own catalog and
// dialect and the baseline it bound over the statements it compiled, so a
// consumer selects an engine with New(catalog, dialect,
// WithEngine(postgres.Engine)), or passes an Engine of its own. See
// Engine.
func WithEngine(e Engine) Option {
	return func(o *options) { o.engine = e }
}

// CreateOption configures one call of an operation that creates a row,
// Create or Ensure, beyond its required arguments.
type CreateOption func(*createOptions)

// createOptions collects what the create options set.
type createOptions struct {
	id    string
	hasID bool
}

// WithID supplies the id of the row the call inserts, in place of one the
// store mints, so a seeded row keeps the same id across resets. The id is
// checked with blobfs.ParseID before any SQL: text that is not a UUID, or
// the nil UUID, is blobfs.ErrInvalidID, and the canonical form is what the
// row carries. An id a row of the same table already carries fails the
// primary key as blobfs.ErrIDTaken. When an Ensure finds a row instead of
// inserting one, the found row keeps its own id and the option has no
// effect.
func WithID(id string) CreateOption {
	return func(o *createOptions) {
		o.id = id
		o.hasID = true
	}
}

// VersionOption configures a call that acts on a row the caller read, beyond
// its required arguments: Files.Hold, Files.Delete, Directories.Delete, and
// Directories.MarkDeleting.
type VersionOption func(*holdOptions)

// HoldOption configures one call of Files.Hold beyond its required
// arguments.
type HoldOption = VersionOption

// DeleteOption configures one call of Files.Delete or Directories.Delete
// beyond its required arguments.
type DeleteOption = VersionOption

// holdOptions collects what the version options set.
type holdOptions struct {
	version    int64
	hasVersion bool
}

// AtVersion makes the call act on the row only at version, the value the
// caller read from a listing or an earlier read, so a caller that acts on a
// row it has not read inside its transaction learns that the row moved on:
// a Files.Hold or a Directories.Delete matches nothing, and a Files.Delete
// or a Directories.MarkDeleting leaves the rows as they are, unless the row
// is still at version. A row at another version is
// query.ErrVersionMismatch. A file or directory that is already deleting is
// the delete's or the mark's retry, which converges whatever the version.
func AtVersion(version int64) VersionOption {
	return func(o *holdOptions) {
		o.version = version
		o.hasVersion = true
	}
}

// ListOption configures one call of a listing, Directories.List or
// Directories.Continue and Files.List or Files.Continue, beyond its
// required arguments.
type ListOption func(*listOptions)

// listOptions collects what the list options set.
type listOptions struct {
	includeDeleting bool
}

// IncludeDeleting makes a listing show deleting rows as well: the
// directories of a branch marked for deletion, and the files whose delete
// began or whose directory's branch was marked. Without it a listing
// hides them, by a filter on status it appends after the caller's own,
// and a listing of a directory that is itself deleting is
// blobfs.ErrDeleting. With it the listing composes the caller's filters
// alone and lists a deleting directory's contents, which is how the work
// of a branch's delete is found. The appended filter is part of what a
// cursor is bound to, so a cursor continues only a listing called the
// same way, with the option or without it; either way a row marked
// deleting after the cursor was issued is hidden from the pages past it
// unless the option is given.
func IncludeDeleting() ListOption {
	return func(o *listOptions) { o.includeDeleting = true }
}

// SweepOption configures one call of Store.Sweep beyond its required
// arguments.
type SweepOption func(*sweepOptions)

// sweepOptions collects what the sweep options set.
type sweepOptions struct {
	batch    int
	onRemove func(ctx context.Context, tx *sqlate.Tx, dir blobfs.Directory) error
	staleAge time.Duration
	hasStale bool
}

// defaultBatch is the number of records a pass handles when Batch is not
// given.
const defaultBatch = 100

// Batch bounds the records one pass of Store.Sweep handles to n: each file
// whose object it deletes and whose row it purges, each directory it
// removes, and each stale row it reclaims counts one. The bound is by
// record, not by bytes, since the pass never reads an object's size to
// delete it. A pass that stops at the bound with work remaining reports
// SweepResult.More. The default is 100; an n below 1 is refused before
// any SQL.
func Batch(n int) SweepOption {
	return func(o *sweepOptions) { o.batch = n }
}

// OnRemoveDirectory runs fn inside the transaction that removes each
// directory of a branch being deleted, before the removal, with the
// directory as the pass read it, deleting. It is where a consumer removes
// its own rows about the directory, such as the owner row that binds a
// top-level directory to a unit, whose foreign key would otherwise refuse
// the removal as blobfs.ErrReferenced. An error from fn rolls the
// transaction back, so the directory stays, deleting, for the next pass,
// and the pass leaves its branch and returns the error with any others.
// fn runs once per attempt at a
// removal, and what it wrote commits or rolls back with the removal, so a
// removal aborted by fn or refused by the database runs it again on the
// next pass with nothing of the first attempt left.
func OnRemoveDirectory(fn func(ctx context.Context, tx *sqlate.Tx, dir blobfs.Directory) error) SweepOption {
	return func(o *sweepOptions) { o.onRemove = fn }
}

// StaleOlderThan makes a pass of Store.Sweep also reclaim the file rows
// a caller left partway through a protocol, whose updated_at is older
// than age: the pending rows of writes that stopped after their first
// step and were never completed or deleted, and the deleting rows of
// deletes that stopped after Files.Delete and before Files.Purge. Such a
// deleting row is hidden from the listings yet still holds its name, so a
// write of the name is refused until the row is purged, and nothing but a
// sweep finds it. A pending row is moved to deleting at the version the
// pass read, so a write that completed in the meantime is left as it is;
// a deleting row is past that step already. Then the row's object, which
// may never have been stored or may be gone already, is deleted, and the
// row purged. The reclaim is off without the option. A write resumed
// through Files.Ensure keeps its row's updated_at, so age must exceed the
// longest write a consumer lets run, and a Complete of a reclaimed row is
// blobfs.ErrDeleting, or blobfs.ErrNotFound once the row is purged; a
// delete its caller is still finishing is harmless to finish twice, since
// each of its steps is idempotent. An age that is
// not positive is refused before any SQL.
func StaleOlderThan(age time.Duration) SweepOption {
	return func(o *sweepOptions) {
		o.staleAge = age
		o.hasStale = true
	}
}
