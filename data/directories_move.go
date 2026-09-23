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
// of the directory with ancestorID, that directory itself included. It is
// the cycle check of a directory move, exported as a tree predicate a
// consumer can run on its own, for a scope check of its own or to refuse a
// move early in a user interface: a directory D may move under a new
// parent P only when IsWithin(P, D) is false. One recursive statement
// walks upward from id, so the cost is the depth of id. A directory that
// does not exist is within nothing. The answer is reliable only while no
// other transaction is moving directories, which is what Move's lock
// provides; the walk itself terminates only while the tree has no cycle.
func (d *Directories) IsWithin(ctx context.Context, sess sqlate.Session, id, ancestorID string) (bool, error) {
	n, err := d.isWithin.One(ctx, sess, query.Args{"id": id, "ancestor_id": ancestorID})
	if err != nil {
		return false, fmt.Errorf("data: is %s within %s: %w", id, ancestorID, err)
	}
	return n > 0, nil
}

// LockTree takes the variant's tree lock inside tx, held until tx commits
// or rolls back. Move takes it itself; a caller takes it directly to
// serialize a tree-shape change of its own. See Variant.
func (d *Directories) LockTree(ctx context.Context, tx *sqlate.Tx) error {
	if err := d.variant.LockTree(ctx, tx); err != nil {
		return fmt.Errorf("data: lock tree: %w", err)
	}
	return nil
}

// Serializes reports whether LockTree serializes tree-shape changes across
// transactions on the store's variant. See Variant.
func (d *Directories) Serializes() bool {
	return d.variant.Serializes()
}

// Move moves the directory with id under the directory with parentID as
// name, which also renames it when the name differs, and returns the row
// as the database holds it afterward. The move runs in tx, in three steps
// that must see one tree lock: LockTree, then IsWithin(parentID, id), then
// the guarded update of parent_id and name. The update is guarded by
// version, the value the caller read from the directory's row, through the
// query library's optimistic-concurrency protocol; the caller reads the
// directory in the same transaction and passes its Version. The update
// returns the row: one statement where the dialect renders RETURNING, the
// update and a read of the row otherwise. The directory's children and
// files follow it, because they reference it by id and every path is
// computed at read time; no object moves, since no key encodes a path.
//
// The root is blobfs.ErrRootDirectory, refused before any SQL. A new
// parent that is the directory itself or one of its descendants is
// blobfs.ErrCycle, and nothing changes. A directory that does not exist,
// or a new parent that does not, is blobfs.ErrNotFound (the parent's
// through the foreign key blobfs_fk_directory_parent); a name already held
// by a directory under the new parent is blobfs.ErrNameTaken. A file under
// the new parent with the same name is no conflict: directories and files
// have separate name spaces. A row whose version moved on is
// query.ErrVersionMismatch. A refused name is a blobfs.NameError.
//
// The lock is what closes the race between two opposing moves: each takes
// it before its check, so the second one's check sees the first one's
// committed update and is refused. On a variant whose Serializes reports
// false the lock is a no-op, both checks pass against the same committed
// state, and the two commits leave the two directories each other's
// ancestor, detached from the root; a caller on such a variant serializes
// directory moves outside the database.
func (d *Directories) Move(ctx context.Context, tx *sqlate.Tx, id, parentID, name string, version int64) (blobfs.Directory, error) {
	if id == blobfs.RootID {
		return blobfs.Directory{}, fmt.Errorf("data: move directory %s: %w", id, blobfs.ErrRootDirectory)
	}
	name, err := validName(name)
	if err != nil {
		return blobfs.Directory{}, fmt.Errorf("data: move directory %s: %w", id, err)
	}
	if err := d.LockTree(ctx, tx); err != nil {
		return blobfs.Directory{}, fmt.Errorf("data: move directory %s: %w", id, err)
	}
	within, err := d.IsWithin(ctx, tx, parentID, id)
	if err != nil {
		return blobfs.Directory{}, fmt.Errorf("data: move directory %s: %w", id, err)
	}
	if within {
		return blobfs.Directory{}, fmt.Errorf("data: move directory %s under %s: the new parent is the directory or one of its descendants: %w", id, parentID, blobfs.ErrCycle)
	}
	dir, err := d.move.Run(ctx, tx, version, query.Args{"id": id, "parent_id": parentID, "name": name})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return blobfs.Directory{}, fmt.Errorf("data: move directory %s: %w", id, blobfs.ErrNotFound)
	case errors.Is(err, query.ErrRefused):
		// The row is at the expected version and the update's own
		// predicate, parent_id IS NOT NULL, refused it: the root, which
		// the check above already refuses by id.
		return blobfs.Directory{}, fmt.Errorf("data: move directory %s: %w", id, blobfs.ErrRootDirectory)
	case err != nil:
		return blobfs.Directory{}, fmt.Errorf("data: move directory %s under %s as %q: %w", id, parentID, name, classifyWrite(err))
	}
	return dir, nil
}
