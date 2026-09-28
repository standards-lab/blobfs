package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
)

// IsWithin reports whether the directory with id lies within the subtree
// of the directory with ancestorID, that directory included: Move's cycle
// check, for a consumer's own scope check or to refuse a move early. A
// directory D may move under P only when IsWithin(P, D) is false. A
// directory that does not exist is within nothing; on a loop in the tree,
// id is within every directory on its chain and none off it. The answer
// holds only while no other transaction moves directories.
func (d *Directories) IsWithin(ctx context.Context, sess sqlate.Session, id, ancestorID string) (_ bool, err error) {
	defer wrap(&err, "is %s within %s", id, ancestorID)
	return d.isWithinTree(ctx, sess, id, ancestorID)
}

// isWithinTree is IsWithin's body, with its error bare for Move.
func (d *Directories) isWithinTree(ctx context.Context, sess sqlate.Session, id, ancestorID string) (bool, error) {
	n, err := d.isWithin.One(ctx, sess, query.Args{"id": id, "ancestor_id": ancestorID})
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// LockTree takes the variant's tree lock inside tx, held until tx ends, for
// a tree-shape change of the caller's own; Move and MarkDeleting take it
// themselves. See Variant.
func (d *Directories) LockTree(ctx context.Context, tx *sqlate.Tx) (err error) {
	defer wrap(&err, "lock tree")
	return d.variant.LockTree(ctx, tx)
}

// Serializes reports whether LockTree serializes tree-shape changes across
// transactions on the store's variant. See Variant.
func (d *Directories) Serializes() bool {
	return d.variant.Serializes()
}

// Move moves the directory with id under parentID as name, which renames
// it when the name differs, and returns the row as the database holds it.
// It runs in tx under the tree lock: LockTree, then IsWithin(parentID, id),
// then the update, guarded by version, which the caller read in the same
// transaction. The directory's children and files follow it. See Moves in
// docs/concepts.md for the lock and isolation levels.
//
// Refusals: blobfs.ErrRootDirectory before any SQL; blobfs.NameError;
// blobfs.ErrCycle for a new parent inside the directory's subtree;
// blobfs.ErrNotFound for a missing directory or new parent;
// blobfs.ErrNameTaken for a name a directory under the new parent holds;
// blobfs.ErrDeleting when the directory or either parent is deleting;
// query.ErrVersionMismatch; sqlate.ErrSerializationFailure at repeatable
// read or serializable.
func (d *Directories) Move(ctx context.Context, tx *sqlate.Tx, id, parentID, name string, version int64) (_ blobfs.Directory, err error) {
	defer wrap(&err, "move directory %s under %s as %q", id, parentID, name)
	if id == blobfs.RootID {
		return blobfs.Directory{}, blobfs.ErrRootDirectory
	}
	if name, err = validName(name); err != nil {
		return blobfs.Directory{}, err
	}
	if err := d.variant.LockTree(ctx, tx); err != nil {
		return blobfs.Directory{}, fmt.Errorf("lock tree: %w", err)
	}
	within, err := d.isWithinTree(ctx, tx, parentID, id)
	switch {
	case err != nil:
		return blobfs.Directory{}, fmt.Errorf("the cycle check: %w", err)
	case within:
		return blobfs.Directory{}, fmt.Errorf("the new parent is the directory or one of its descendants: %w", blobfs.ErrCycle)
	}
	dir, changed, err := d.move.One(ctx, tx, query.Args{"id": id, "parent_id": parentID, "name": name, "version": version})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return blobfs.Directory{}, blobfs.ErrNotFound
	case err != nil:
		return blobfs.Directory{}, classifyWrite(err)
	case changed:
		return dir, nil
	case dir.IsRoot():
		// The update's parent_id predicate refused the root.
		return blobfs.Directory{}, blobfs.ErrRootDirectory
	case !dir.Status.Mutable():
		// Deleting outranks the stale version.
		return blobfs.Directory{}, closed(dir)
	}
	// The parents tell a closed or missing parent from a stale version.
	if err := d.dirs.refusedMove(ctx, tx, dir.ParentID, parentID, version, dir.Version); err != nil {
		return blobfs.Directory{}, err
	}
	return blobfs.Directory{}, fmt.Errorf("the update matched no row, yet the directory is %s at version %d", dir.Status, dir.Version)
}
