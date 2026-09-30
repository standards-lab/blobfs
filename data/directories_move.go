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
// check, for a consumer's own. The answer holds only while no other
// transaction moves directories. See Directories in docs/features.md.
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

// Move moves the directory with id under parentID as name, guarded by
// version, and returns the row; its children and files follow it. It runs
// in tx under the tree lock, after the cycle check. Refusals:
// blobfs.ErrRootDirectory, blobfs.NameError, blobfs.ErrCycle,
// blobfs.ErrNotFound, blobfs.ErrNameTaken, a blobfs.DeletingError,
// query.ErrVersionMismatch, and sqlate.ErrSerializationFailure above read
// committed. See Moves in docs/concepts.md.
func (d *Directories) Move(ctx context.Context, tx *sqlate.Tx, id, parentID, name string, version int64) (_ blobfs.Directory, err error) {
	defer wrap(&err, "move directory %s under %s as %q", id, parentID, name)
	if isRoot(id) {
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
	args := query.Args{"id": id, "parent_id": parentID, "name": name, "version": version}
	return rerunOnce(func() (blobfs.Directory, bool, error) {
		dir, changed, err := d.move.One(ctx, tx, args)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return blobfs.Directory{}, false, blobfs.ErrNotFound
		case err != nil:
			return blobfs.Directory{}, false, classifyWrite(err)
		case changed:
			return dir, false, nil
		case dir.IsRoot():
			// The update's parent_id predicate refused the root.
			return blobfs.Directory{}, false, blobfs.ErrRootDirectory
		case !dir.Status.Mutable():
			// Deleting outranks the stale version.
			return blobfs.Directory{}, false, closed(dir)
		}
		// The parents tell a closed or missing parent from a stale version,
		// and then the name's holder tells a deleting one; an update none
		// explains runs once more, as rerunOnce says, under the lock the
		// cycle check ran under.
		if err := d.dirs.refusedMove(ctx, tx, dir.ParentID, parentID, version, dir.Version); err != nil {
			return blobfs.Directory{}, false, err
		}
		if err := d.refusedName(ctx, tx, parentID, name); err != nil {
			return blobfs.Directory{}, false, err
		}
		return blobfs.Directory{}, true, fmt.Errorf("the update matched no row, yet the directory is %s at version %d", dir.Status, dir.Version)
	})
}
