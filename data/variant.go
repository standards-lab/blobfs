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
// forwards these to the variant its Engine built. The default is the
// baseline, the standard-tier variant New binds, which is complete on any
// engine. An engine sub-module ships an Engine of its own, and a consumer
// supplies one by writing an Engine whose variant implements the interface.
//
// A variant embeds the variant it is given, the baseline or an engine's
// variant, and overrides the methods it needs: that is the contract, not a
// convenience. It is what lets the interface grow, since a release that adds
// a variation point adds the method to the baseline as well, and every
// variant that embeds one inherits it, so adding a variation point is a
// minor release. A type that implements the interface without embedding one
// is outside the contract, and a minor release may break its build.
//
// LockTree serializes tree-shape changes: the caller takes it inside the
// transaction that will move a directory, before the cycle check, so that
// two opposing moves cannot each pass the check and together form a cycle.
// The lock is held until the transaction commits or rolls back, and every
// process that moves directories must take it for the serialization to
// hold; it takes a *sqlate.Tx because a lock no transaction holds
// serializes nothing. Serializes reports whether the variant's LockTree
// serializes at all: the baseline has no lock, because standard SQL has
// none, and returns false; a caller that needs the guarantee checks it
// before the first move.
//
// ResolvePath walks segments, each a normalized and validated directory
// name, downward from the directory with startID and returns the deepest
// directory reached and its depth: the number of segments matched, so a
// depth equal to len(segments) is the resolved directory and a smaller
// depth means segments[depth] named no directory under the one returned.
// No segments returns the start at depth 0. A start that does not exist
// is blobfs.ErrNotFound. The baseline reads the start and then one child
// per segment; a variant may walk the whole path in one statement.
//
// HoldFile takes the row lock of the file with id for the rest of tx, the
// lock a Files.Delete of the row waits on, and reports whether it held the
// row: true when the row exists, is not deleting, and, when version is not
// nil, sits at *version; false, with no lock taken and no error, otherwise.
// It changes no value and advances no version. Files.Hold reads the row
// after a false to classify the refusal, so a variant decides only whether
// the row is held and how the lock is taken. The baseline takes it with an
// update that assigns a column to itself, which is portable but writes a
// new row version on an engine that keeps one per update; a variant may
// take the same lock without writing.
//
// Statements and Verify are the variant's own inventory: the statements it
// compiled beyond the data package's, which Store.Statements appends to its
// own, and the query.Verifier that Store.Verify runs in the same pass as its
// own, so a startup Verify covers an engine's statements too. The baseline
// compiled none of its own, since its statements are the data package's,
// and returns nil from both; a variant that compiles statements overrides
// both, and one that wraps another inherits them through the embedding.
//
// The Store validates every input and classifies every error itself, so a
// variant binds what it is given and returns what the session mapped.
type Variant interface {
	LockTree(ctx context.Context, tx *sqlate.Tx) error
	Serializes() bool
	ResolvePath(ctx context.Context, sess sqlate.Session, startID string, segments []string) (blobfs.Directory, int, error)
	HoldFile(ctx context.Context, tx *sqlate.Tx, id string, version *int64) (bool, error)
	Statements() []query.Statement
	Verify(ctx context.Context, sess sqlate.Session) error
}

// Engine builds a store's variant over the baseline the store compiled,
// against the store's catalog and dialect. New compiles the data package's
// statements once, binds the baseline over them, and passes it as base, so
// an engine compiles only its own native statements and embeds base for
// every variation point it does not override. A consumer supplies its own
// variant through an Engine too: the variant embeds base, or the variant
// another Engine returned, and overrides the methods it needs:
//
//	func(c *query.Catalog, d sqlate.Dialect, base data.Variant) (data.Variant, error) {
//		return lockless{Variant: base}, nil
//	}
type Engine func(catalog *query.Catalog, dialect sqlate.Dialect, base Variant) (Variant, error)

// standard is the standard-tier variant, the baseline: no tree lock, path
// resolution one child read per segment, a file hold that is a
// self-assigning update, and no statements of its own. It is the variant
// New uses when no WithEngine option is given. New builds it over the
// statements it compiled and hands it to the Engine, if any, so a variant
// that overrides some of its methods embeds that one and compiles no
// baseline of its own.
type standard struct {
	dirs     directoryReads
	holdFile query.Statement
}

// newStandard binds the baseline over an already compiled set and the
// shared directory reads.
func newStandard(stmts *query.Statements, dirs directoryReads) *standard {
	return &standard{
		dirs:     dirs,
		holdFile: stmts.Statement("hold_file"),
	}
}

// LockTree takes no lock: standard SQL has no statement that holds a lock
// until commit, so the baseline cannot serialize tree-shape changes, and two
// opposing concurrent moves on it can form a cycle. A consumer that needs
// the guarantee on an engine without a native variant serializes moves
// outside the database.
func (*standard) LockTree(context.Context, *sqlate.Tx) error {
	return nil
}

// Serializes reports false: the baseline's LockTree is a no-op.
func (*standard) Serializes() bool {
	return false
}

// ResolvePath runs the baseline's walk: the read of the start by id, then
// one directory_by_name read per segment until one names no directory or
// the segments run out, so a resolved path of n segments is n+1
// statements. See Variant.
func (v *standard) ResolvePath(ctx context.Context, sess sqlate.Session, startID string, segments []string) (blobfs.Directory, int, error) {
	dir, err := v.dirs.byID.One(ctx, sess, query.Args{"id": startID})
	if err != nil {
		return blobfs.Directory{}, 0, notFound(err)
	}
	for depth, name := range segments {
		child, err := v.dirs.byName.One(ctx, sess, query.Args{"parent_id": dir.ID, "name": name})
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

// HoldFile runs the baseline's hold, hold_file: an update that assigns
// updated_at to itself where the row is not deleting and, when version is
// not nil, sits at the version. The update takes the row's lock and
// changes no value, and a row affected is a row held. See Variant.
func (v *standard) HoldFile(ctx context.Context, tx *sqlate.Tx, id string, version *int64) (bool, error) {
	n, err := v.holdFile.Exec(ctx, tx, withVersion(query.Args{"id": id}, version))
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// Statements returns nil: the baseline's statements are the data package's
// own, which the Store already lists.
func (*standard) Statements() []query.Statement {
	return nil
}

// Verify returns nil: the baseline's statements are the data package's
// own, which the Store already verifies.
func (*standard) Verify(context.Context, sqlate.Session) error {
	return nil
}
