package data

import (
	"context"
	"database/sql"
	"errors"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
)

// Variant is the set of operations an engine may implement with its own
// native statements: the variation points of the data package, which
// standard SQL cannot express as well. The Store runs every other operation
// from its standard-tier statements, whose returning commands already take
// the single-statement form on an engine whose dialect renders it, and
// forwards these to the variant its Engine built. The default is Standard,
// which is complete on any engine. An engine sub-module ships an Engine of
// its own, and a consumer supplies one by writing an Engine whose variant
// implements the interface, typically by embedding the baseline it is given,
// or an engine's variant, and overriding the methods it needs.
//
// LockTree serializes tree-shape changes: the caller takes it inside the
// transaction that will move a directory, before the cycle check, so that
// two opposing moves cannot each pass the check and together form a cycle.
// The lock is held until the transaction commits or rolls back, and every
// process that moves directories must take it for the serialization to
// hold; it takes a *sqlate.Tx because a lock no transaction holds
// serializes nothing. Serializes reports whether the variant's LockTree
// serializes at all: Standard has no lock, because standard SQL has none,
// and returns false; a caller that needs the guarantee checks it before
// the first move.
//
// ResolvePath walks segments, each a normalized and validated directory
// name, downward from the directory with startID and returns the deepest
// directory reached and its depth: the number of segments matched, so a
// depth equal to len(segments) is the resolved directory and a smaller
// depth means segments[depth] named no directory under the one returned.
// No segments returns the start at depth 0. A start that does not exist
// is blobfs.ErrNotFound. Standard reads the start and then one child per
// segment; a variant may walk the whole path in one statement.
//
// The Store validates every input and classifies every error itself, so a
// variant binds what it is given and returns what the session mapped.
type Variant interface {
	LockTree(ctx context.Context, tx *sqlate.Tx) error
	Serializes() bool
	ResolvePath(ctx context.Context, sess sqlate.Session, startID string, segments []string) (blobfs.Directory, int, error)
}

// Engine builds a store's variant over the baseline the store compiled,
// against the store's catalog and dialect. New compiles the data package's
// statements once, binds Standard over them, and passes it as base, so an
// engine compiles only its own native statements and embeds base for every
// variation point it does not override. A consumer supplies its own variant
// through an Engine too: the variant embeds base, or the variant another
// Engine returned, and overrides the methods it needs:
//
//	func(c *query.Catalog, d sqlate.Dialect, base *data.Standard) (data.Variant, error) {
//		return lockless{Variant: base}, nil
//	}
//
// A variant that compiled statements of its own exposes them through two
// optional methods the store asserts: Statements() []query.Statement, which
// Store.Statements appends to its inventory, and Verify(ctx, sess) error,
// the query.Verifier that Store.Verify runs in the same pass as its own, so
// a startup Verify covers the engine's statements too. A wrapper that embeds
// such a variant through the Variant interface hides both methods, and
// forwards them itself when it wants them listed and verified.
type Engine func(catalog *query.Catalog, dialect sqlate.Dialect, base *Standard) (Variant, error)

// Standard is the standard-tier variant, the baseline: no tree lock, and
// path resolution one child read per segment. It is the variant New uses
// when no WithEngine option is given. New builds it over the statements it
// compiled and hands it to the Engine, if any, so a variant that overrides
// some of its methods embeds that one and compiles no baseline of its own.
type Standard struct {
	directoryByID   query.Rows[blobfs.Directory]
	directoryByName query.Rows[blobfs.Directory]
}

// newStandard binds the baseline over an already compiled set.
func newStandard(stmts *query.Statements) *Standard {
	directory := query.Scanner[blobfs.Directory]()
	return &Standard{
		directoryByID:   stmts.Statement("directory_by_id").Scan(directory),
		directoryByName: stmts.Statement("directory_by_name").Scan(directory),
	}
}

// LockTree takes no lock: standard SQL has no statement that holds a lock
// until commit, so the baseline cannot serialize tree-shape changes, and two
// opposing concurrent moves on it can form a cycle. A consumer that needs
// the guarantee on an engine without a native variant serializes moves
// outside the database.
func (*Standard) LockTree(context.Context, *sqlate.Tx) error {
	return nil
}

// Serializes reports false: the baseline's LockTree is a no-op.
func (*Standard) Serializes() bool {
	return false
}

// ResolvePath runs the baseline's walk: the read of the start by id, then
// one directory_by_name read per segment until one names no directory or
// the segments run out, so a resolved path of n segments is n+1
// statements. See Variant.
func (v *Standard) ResolvePath(ctx context.Context, sess sqlate.Session, startID string, segments []string) (blobfs.Directory, int, error) {
	dir, err := v.directoryByID.One(ctx, sess, query.Args{"id": startID})
	if err != nil {
		return blobfs.Directory{}, 0, notFound(err)
	}
	for depth, name := range segments {
		child, err := v.directoryByName.One(ctx, sess, query.Args{"parent_id": dir.ID, "name": name})
		if errors.Is(err, sql.ErrNoRows) {
			return dir, depth, nil
		}
		if err != nil {
			return blobfs.Directory{}, 0, err
		}
		dir = child
	}
	return dir, len(segments), nil
}
