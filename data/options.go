package data

import (
	"context"
	"time"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
)

// Option configures New beyond its required arguments.
type Option func(*options)

// options collects what the options set.
type options struct {
	engine Engine
}

// WithEngine makes the store forward its variation points to the variant
// e builds instead of the baseline. New calls e once. See Engine.
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

// WithID supplies the id of the row the call inserts, in place of a minted
// one, so a seeded row keeps its id across resets. It is checked with
// blobfs.ParseID before any SQL; a row an Ensure finds keeps its own id.
func WithID(id string) CreateOption {
	return func(o *createOptions) {
		o.id = id
		o.hasID = true
	}
}

// VersionOption configures a call that acts on a row the caller read,
// beyond its required arguments: Files.Hold, Files.Delete,
// Directories.Delete, and Directories.MarkDeleting.
type VersionOption func(*versionOptions)

// versionOptions collects what the version options set.
type versionOptions struct {
	version    int64
	hasVersion bool
}

// AtVersion makes the call act on the row only at version, the value the
// caller read from a listing or an earlier read; a row at another version
// is query.ErrVersionMismatch and is left as it is. A row already deleting
// is Files.Delete's or Directories.MarkDeleting's retry, which converges.
func AtVersion(version int64) VersionOption {
	return func(o *versionOptions) {
		o.version = version
		o.hasVersion = true
	}
}

// atVersion resolves a call's version options: the version AtVersion
// gave, or nil when none did.
func atVersion(opts []VersionOption) *int64 {
	var o versionOptions
	for _, opt := range opts {
		opt(&o)
	}
	if !o.hasVersion {
		return nil
	}
	return &o.version
}

// withVersion binds version into args for a statement whose version
// predicate is nullable: NULL when version is nil, which guards nothing, and
// the version otherwise.
func withVersion(args query.Args, version *int64) query.Args {
	if version == nil {
		return args.With("version", nil)
	}
	return args.With("version", *version)
}

// ListOption configures one call of a listing beyond its required
// arguments: Directories.List, Directories.Continue, Files.List, or
// Files.Continue.
type ListOption func(*listOptions)

// listOptions collects what the list options set.
type listOptions struct {
	includeDeleting bool
}

// IncludeDeleting makes a listing show deleting rows and list a deleting
// directory, which is how the work of a delete is found. A cursor
// continues only a listing called the same way, with the option or
// without it. See Listings in docs/features.md.
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
// finished, each directory removed, and each stale row reclaimed counts
// one. The default is 100; an n below 1 is refused before any SQL.
func Batch(n int) SweepOption {
	return func(o *sweepOptions) { o.batch = n }
}

// OnRemoveDirectory runs fn in the transaction that removes each directory
// of a branch, before the removal: where a consumer removes its own rows
// that reference the directory. An error from fn rolls the removal back
// for the next pass.
func OnRemoveDirectory(fn func(ctx context.Context, tx *sqlate.Tx, dir blobfs.Directory) error) SweepOption {
	return func(o *sweepOptions) { o.onRemove = fn }
}

// StaleOlderThan makes a pass of Store.Sweep also reclaim the pending and
// deleting file rows last written more than age ago, oldest first. age
// must exceed the longest write the consumer lets run; see Stale rows and
// orphaned objects in docs/concepts.md.
func StaleOlderThan(age time.Duration) SweepOption {
	return func(o *sweepOptions) {
		o.staleAge = age
		o.hasStale = true
	}
}
