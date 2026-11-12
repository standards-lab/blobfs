package data

import (
	"context"
	"time"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
)

// Options configures New beyond its required arguments. The zero Options
// binds the baseline.
type Options struct {
	// Engine makes the store forward its variation points to the variant
	// it builds instead of the baseline; New calls it once. See Engine.
	Engine Engine
}

// CreateOptions configures one call of an operation that creates a row,
// Create or Ensure, beyond its required arguments.
type CreateOptions struct {
	// ID is the id of the row the call inserts, so a seeded row keeps its
	// id across resets. A set ID is checked with blobfs.ParseID before any
	// SQL; an empty ID mints one. A row an Ensure finds keeps its own id.
	ID string
}

// VersionOptions configures a call that acts on a row the caller read,
// beyond its required arguments: Files.Hold, Files.Delete,
// Directories.Delete, Directories.MarkDeleting, and Store.RemoveFileID.
type VersionOptions struct {
	// Version makes the call act on the row only at that version, the
	// value the caller read from a listing or an earlier read; a row at
	// another version is query.ErrVersionMismatch and is left as it is. A
	// row already deleting is Files.Delete's or Directories.MarkDeleting's
	// retry, which converges. A zero Version leaves the call unguarded:
	// row versions start at 1.
	Version int64
}

// at is the version Version guards, or nil when the call is unguarded.
func (o VersionOptions) at() *int64 {
	return versionAt(o.Version)
}

// versionAt is version as a guard: nil, which guards nothing, for zero.
func versionAt(version int64) *int64 {
	if version == 0 {
		return nil
	}
	return &version
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

// ListOptions configures one call of a listing beyond its required
// arguments: Directories.List, Directories.Continue, Files.List, or
// Files.Continue.
type ListOptions struct {
	// IncludeDeleting makes the listing show deleting rows and list a
	// deleting directory, which is how the work of a delete is found. A
	// cursor continues only a listing called the same way, with
	// IncludeDeleting set or without it. The zero value hides deleting
	// rows and refuses a deleting directory. See Listings in
	// docs/features.md.
	IncludeDeleting bool
}

// CursorPage names the page Continue reads past a cursor, as query.Page
// names one by number.
type CursorPage struct {
	// After is the Next of an earlier page of the listing called the same
	// way; an empty After is refused before any SQL.
	After query.Cursor

	// Size is the number of rows the page reads; a Size below 1 is refused
	// before any SQL.
	Size int
}

// WriteOption configures one call of a write protocol, Store.WriteFile or
// Store.EnsureFile, beyond its required arguments.
type WriteOption func(*writeOptions)

// writeOptions collects what the write options set.
type writeOptions struct {
	onComplete func(ctx context.Context, tx *sqlate.Tx, f blobfs.File) error
}

// writeOpts resolves a call's write options.
func writeOpts(opts []WriteOption) writeOptions {
	var o writeOptions
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// OnComplete runs fn in the transaction that completes the file, after
// Files.Complete has made the row available, with the call's context and
// the completed File: where a consumer writes its own rows that must
// commit exactly when the file does. An error from fn rolls the completion
// back and abandons the write as a failed completion does, the pending row
// deleted with its object; the call returns an error wrapping fn's.
// Store.EnsureFile runs fn only when the call completes the file itself:
// never for a row it finds available, or one another writer completed.
func OnComplete(fn func(ctx context.Context, tx *sqlate.Tx, f blobfs.File) error) WriteOption {
	return func(o *writeOptions) { o.onComplete = fn }
}

// RemoveOptions configures one call of Store.RemoveFile beyond its
// required arguments.
type RemoveOptions struct {
	// Version guards the delete as VersionOptions.Version guards
	// Files.Delete. A zero Version leaves the call unguarded.
	Version int64

	// OnDelete runs in the transaction that begins the delete, after pick
	// and Files.Delete have marked the file deleting, with the call's
	// context and the File as marked: where a consumer writes its own rows
	// that must commit exactly when the delete begins. A retry, which
	// finds the file already deleting, runs no hook and finishes the
	// delete. An error from it rolls the transaction back, leaving the
	// file as it was and its object in place; the call returns an error
	// wrapping it. A nil OnDelete runs no hook.
	OnDelete func(ctx context.Context, tx *sqlate.Tx, f blobfs.File) error
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
