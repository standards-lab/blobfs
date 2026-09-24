package blobfs

import (
	"errors"
	"fmt"
)

// The sentinels package data maps its outcomes onto. A database
// violation of one of blobfs's own documented constraints becomes one of
// these, carried by a ViolationError; a violation of a consumer's
// constraint is left unclassified.
var (
	// ErrNotFound reports a directory or file that does not exist, including
	// a parent or directory id that an insert or move referenced.
	ErrNotFound = errors.New("blobfs: not found")

	// ErrNameTaken reports a name already held in the target directory by a
	// row of the same table. A deleting row holds its name until it is
	// removed.
	ErrNameTaken = errors.New("blobfs: name taken")

	// ErrInvalidName reports a name ValidateName refused. A NameError
	// carries the reason.
	ErrInvalidName = errors.New("blobfs: invalid name")

	// ErrInvalidPath reports a path package data refused: one that starts
	// with a slash, or one with a segment ValidateName refuses, in which
	// case the error also matches ErrInvalidName. A path is relative, a/b
	// below the directory it starts from; a spelling from the root, /a/b,
	// is a consumer's own input syntax, which it strips before resolving
	// from RootID.
	ErrInvalidPath = errors.New("blobfs: invalid path")

	// ErrRootDirectory reports an operation refused because it targets the
	// root: creating a second row with no parent, or deleting, moving, or
	// renaming the root. There is exactly one root per install, seeded by
	// the schema with the id RootID.
	ErrRootDirectory = errors.New("blobfs: the root directory")

	// ErrInvalidKey reports a key the store refused. A KeyError carries the
	// key and the store's reason.
	ErrInvalidKey = errors.New("blobfs: invalid key")

	// ErrInvalidID reports a caller-supplied row id that ParseID refused:
	// text that is not a UUID, or the nil UUID, which is RootID. An IDError
	// carries the reason.
	ErrInvalidID = errors.New("blobfs: invalid id")

	// ErrIDTaken reports a caller-supplied row id that a row of the same
	// table already carries: the primary key refused the insert.
	ErrIDTaken = errors.New("blobfs: id taken")

	// ErrNotEmpty reports a directory delete refused because the directory
	// still has child directories or files.
	ErrNotEmpty = errors.New("blobfs: directory not empty")

	// ErrInvalidTransition reports a status change the transition table
	// does not allow. A TransitionError carries the two statuses.
	ErrInvalidTransition = errors.New("blobfs: invalid status transition")

	// ErrDeleting reports a mutation refused because the row is deleting:
	// a move or rename, or a status change out of deleting.
	ErrDeleting = errors.New("blobfs: row is deleting")

	// ErrNotDeleting reports a purge, the last step of the two-phase
	// delete, refused because the row exists and is not deleting: its
	// delete has not begun. The caller runs the delete's first step first.
	ErrNotDeleting = errors.New("blobfs: the file is not deleting")

	// ErrReferenced reports a delete refused by a foreign key blobfs does
	// not own: a consumer's row still references the file or directory.
	// The sqlate.ConstraintError stays reachable, so the consumer matches
	// the constraint's name against its own and classifies further.
	ErrReferenced = errors.New("blobfs: the row is referenced by a consumer's row")

	// ErrCycle reports a directory move whose new parent sits inside the
	// directory's own subtree, the directory itself included.
	ErrCycle = errors.New("blobfs: move would create a cycle")
)

// ViolationError reports a database constraint violation that a classifier
// mapped to a sentinel: the sentinel it means, the name of the violated
// constraint, and the error the database reported as the cause. Package
// data builds one for each constraint blobfs owns, and a
// consumer builds one for its own constraints with its own sentinels. The
// message prints the sentinel and the constraint name and never the
// driver's text. Unwrap yields the sentinel and the cause, so errors.Is
// matches the sentinel and errors.As reaches the sqlate.ConstraintError
// beneath, for a caller that imports sqlate and wants the class.
type ViolationError struct {
	Sentinel   error
	Constraint string
	Err        error
}

func (e *ViolationError) Error() string {
	if e.Constraint == "" {
		return e.Sentinel.Error()
	}
	return fmt.Sprintf("%v (constraint %s)", e.Sentinel, e.Constraint)
}

// Unwrap returns the sentinel and the cause, so errors.Is matches the
// sentinel and errors.As finds the cause's types.
func (e *ViolationError) Unwrap() []error {
	if e.Err == nil {
		return []error{e.Sentinel}
	}
	return []error{e.Sentinel, e.Err}
}
