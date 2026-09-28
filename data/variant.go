package data

import (
	"context"
	"database/sql"
	"errors"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
)

// Variant is the set of variation points, the operations an engine may
// implement with native statements; the Store forwards them to the
// variant its Engine built, or to the baseline. A variant embeds the
// variant it is given and overrides the methods it needs: that is the
// contract, and a type that implements the interface without embedding
// one is outside it. The Store validates every input and classifies every
// error, so a variant binds what it is given and returns what the session
// mapped. See Engines and variants in docs/concepts.md.
//
// LockTree takes the tree lock in tx, held until tx ends; Serializes
// reports whether it serializes across transactions at all.
//
// ResolvePath walks segments, normalized and validated names, down from
// startID and returns the deepest directory reached and its depth, the
// number of segments matched: a smaller depth than len(segments) means
// segments[depth] named no directory. No segments is the start at depth
// 0; a missing start is blobfs.ErrNotFound.
//
// HoldFile takes, for the rest of tx, the row lock a Files.Delete waits
// on, and reports whether it held the row: one that exists, is not
// deleting, and sits at *version when version is not nil. A false takes no
// lock and is no error; Files.Hold reads the row to classify it. It
// changes no value.
//
// Statements and Verify are the variant's own inventory, beyond the data
// package's, which Store.Statements lists and Store.Verify runs.
type Variant interface {
	LockTree(ctx context.Context, tx *sqlate.Tx) error
	Serializes() bool
	ResolvePath(ctx context.Context, sess sqlate.Session, startID string, segments []string) (blobfs.Directory, int, error)
	HoldFile(ctx context.Context, tx *sqlate.Tx, id string, version *int64) (bool, error)
	Statements() []query.Statement
	Verify(ctx context.Context, sess sqlate.Session) error
}

// Engine builds a store's variant over base, the baseline New bound,
// against the store's catalog and dialect; it compiles only its own
// statements and embeds base, or another Engine's variant, for what it does
// not override:
//
//	func(c *query.Catalog, d sqlate.Dialect, base data.Variant) (data.Variant, error) {
//		return lockless{Variant: base}, nil
//	}
type Engine func(catalog *query.Catalog, dialect sqlate.Dialect, base Variant) (Variant, error)

// standard is the baseline, the standard-tier variant New binds and hands
// to the Engine as base.
type standard struct {
	dirs     directoryReads
	holdFile query.Statement
}

// newStandard binds the baseline over a compiled set.
func newStandard(stmts *query.Statements, dirs directoryReads) *standard {
	return &standard{
		dirs:     dirs,
		holdFile: stmts.Statement("hold_file"),
	}
}

// LockTree takes no lock: standard SQL has none held until commit.
func (*standard) LockTree(context.Context, *sqlate.Tx) error {
	return nil
}

// Serializes reports false: the baseline's LockTree is a no-op.
func (*standard) Serializes() bool {
	return false
}

// ResolvePath reads the start, then one child per segment: n+1 statements
// for n segments.
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

// HoldFile runs hold_file, a self-assigning update; a row affected is a
// row held.
func (v *standard) HoldFile(ctx context.Context, tx *sqlate.Tx, id string, version *int64) (bool, error) {
	n, err := v.holdFile.Exec(ctx, tx, withVersion(query.Args{"id": id}, version))
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// Statements returns nil: the baseline's are the data package's own.
func (*standard) Statements() []query.Statement {
	return nil
}

// Verify returns nil: the Store verifies the baseline's statements.
func (*standard) Verify(context.Context, sqlate.Session) error {
	return nil
}
