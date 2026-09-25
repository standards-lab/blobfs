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
// provides. The walk discards a directory it has visited, so it terminates
// on a cycle as well, which two opposing moves on a variant whose
// Serializes reports false can leave: id is then within every directory on
// its chain, the loop included, and within no directory off it, the root
// among them, so a move of a directory on the loop back under the root
// passes the check and repairs the tree.
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
// name, which also renames it when the name differs, and returns the row as
// the database holds it afterward. The move runs in tx, in three steps that
// must see one tree lock: LockTree, then IsWithin(parentID, id), then the
// guarded update of parent_id and name. The update is guarded by version,
// the value the caller read from the directory's row, with the query
// library's guard predicate, and by the status of the directory and of its
// current and new parents; the caller reads the directory in the same
// transaction and passes its Version. The update is a returning command:
// the single-statement form where the dialect renders RETURNING, and
// otherwise the fallback, the update and a read of the row. The directory's
// children and files follow it, because they reference it by id and every
// path is computed at read time; no object moves, since no key encodes a
// path.
//
// The root is blobfs.ErrRootDirectory, refused before any SQL. A new
// parent that is the directory itself or one of its descendants is
// blobfs.ErrCycle, and nothing changes. A directory that does not exist,
// or a new parent that does not, is blobfs.ErrNotFound; a name already held
// by a directory under the new parent is blobfs.ErrNameTaken. A file under
// the new parent with the same name is no conflict: directories and files
// have separate name spaces. A directory that is deleting, or whose current
// or new parent is, is blobfs.ErrDeleting, whatever its version, since
// MarkDeleting advances the version and nothing leaves or enters a branch
// marked for removal. Any other row whose version moved on is
// query.ErrVersionMismatch, with the expected and current versions in the
// text. When the update changes nothing, the row its read returns and a
// read of each parent tell these apart. A refused name is a
// blobfs.NameError.
//
// The lock is what closes the race between two opposing moves: each takes
// it before its check, so the second one's check sees the first one's
// committed update and is refused. On a variant whose Serializes reports
// false the lock is a no-op, both checks pass against the same committed
// state, and the two commits leave the two directories each other's
// ancestor, detached from the root; a caller on such a variant serializes
// directory moves outside the database.
//
// The lock's guarantee assumes tx runs at read committed, the default
// isolation, where each statement reads the tree as committed when it
// starts, so the check after the lock sees the first mover's commit. At
// repeatable read or serializable the check reads the snapshot the
// transaction's first statement took, which on PostgreSQL is the lock
// statement itself, taken before it blocks, so the check does not see the
// first move and passes. The engine then refuses the second move at its
// update or commit with sqlate.ErrSerializationFailure: at serializable on
// any engine that implements it, and at repeatable read on PostgreSQL,
// whose foreign-key check locks the new parent the first move changed. The
// caller retries a refused move in a new transaction.
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
	dir, changed, err := d.move.One(ctx, tx, query.Args{"id": id, "parent_id": parentID, "name": name, "version": version})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return blobfs.Directory{}, fmt.Errorf("data: move directory %s: %w", id, blobfs.ErrNotFound)
	case err != nil:
		return blobfs.Directory{}, fmt.Errorf("data: move directory %s under %s as %q: %w", id, parentID, name, classifyWrite(err))
	case changed:
		return dir, nil
	case dir.IsRoot():
		// The update's own predicate, parent_id IS NOT NULL, refused the
		// root, which the check above already refuses by id.
		return blobfs.Directory{}, fmt.Errorf("data: move directory %s: %w", id, blobfs.ErrRootDirectory)
	case !dir.Status.Mutable():
		// A deleting directory outranks a stale version: the mark advanced
		// it, so a mover that read the row before the mark holds a version
		// the row no longer carries.
		return blobfs.Directory{}, fmt.Errorf("data: move directory %s: the directory is %s: %w", id, dir.Status, blobfs.ErrDeleting)
	}
	// The update's status predicates refuse a move out of or into a
	// deleting directory and a move under a parent that does not exist; the
	// two parents tell those apart from a version conflict.
	ends, err := readMoveEnds(ctx, tx, d.byID, dir.ParentID, parentID)
	switch {
	case err != nil:
		return blobfs.Directory{}, fmt.Errorf("data: move directory %s: %w", id, err)
	case ends.deleting != nil:
		return blobfs.Directory{}, fmt.Errorf("data: move directory %s under %s: %w", id, parentID, ends.deleting)
	case dir.Version != version:
		return blobfs.Directory{}, fmt.Errorf("data: move directory %s: %w", id, versionMismatch(version, dir.Version))
	case ends.missing:
		return blobfs.Directory{}, fmt.Errorf("data: move directory %s under %s: the new parent: %w", id, parentID, blobfs.ErrNotFound)
	}
	return blobfs.Directory{}, fmt.Errorf("data: move directory %s: the update matched no row, yet the directory is %s at version %d", id, dir.Status, dir.Version)
}
