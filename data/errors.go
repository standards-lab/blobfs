package data

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
)

// writeMapping is what a constraint means on a write: the violation class
// the constraint reports and the sentinel that class means there. A
// constraint reported under any other class is not classified.
type writeMapping struct {
	class    error
	sentinel error
}

// writeSentinels maps the constraints an insert or an update can violate
// to the sentinel each one means there: a primary key is an id a
// caller supplied that a row already carries, a unique constraint on a
// name is a name already held, the root's partial unique index is a second
// root, and a foreign key to a directory is a parent or directory that
// does not exist. The mapping is the write's view of the constraint; a
// delete violates the same foreign keys with the opposite meaning, which
// classifyDelete gives them.
var writeSentinels = map[string]writeMapping{
	blobfs.ConstraintPrimaryKeyDirectory:       {sqlate.ErrUniqueViolation, blobfs.ErrIDTaken},
	blobfs.ConstraintPrimaryKeyFile:            {sqlate.ErrUniqueViolation, blobfs.ErrIDTaken},
	blobfs.ConstraintUniqueDirectoryRoot:       {sqlate.ErrUniqueViolation, blobfs.ErrRootDirectory},
	blobfs.ConstraintUniqueDirectoryParentName: {sqlate.ErrUniqueViolation, blobfs.ErrNameTaken},
	blobfs.ConstraintUniqueFileDirectoryName:   {sqlate.ErrUniqueViolation, blobfs.ErrNameTaken},
	blobfs.ConstraintForeignKeyDirectoryParent: {sqlate.ErrForeignKeyViolation, blobfs.ErrNotFound},
	blobfs.ConstraintForeignKeyFileDirectory:   {sqlate.ErrForeignKeyViolation, blobfs.ErrNotFound},
}

// classifyWrite maps a constraint violation from an insert or an update to
// blobfs's sentinel when the violated constraint is one blobfs owns and
// writeSentinels lists under the class reported. The result is a
// blobfs.ViolationError, whose message names the sentinel and the
// constraint and which keeps the sqlate.ConstraintError reachable through
// errors.As. Any other error, a violation of a consumer's constraint
// included, is returned as it came.
func classifyWrite(err error) error {
	var ce *sqlate.ConstraintError
	if !errors.As(err, &ce) {
		return err
	}
	m, ok := writeSentinels[ce.Constraint]
	if !ok || !errors.Is(ce.Class, m.class) {
		return err
	}
	return &blobfs.ViolationError{Sentinel: m.sentinel, Constraint: ce.Constraint, Err: err}
}

// classifyDelete maps a constraint violation from a delete to blobfs's
// sentinel as a blobfs.ViolationError, whose message names the sentinel
// and the constraint and which keeps the sqlate.ConstraintError reachable
// through errors.As. A delete violates only foreign keys, with the
// opposite meaning a write gives them: the row still has something that
// references it. A foreign key blobfs owns, one of its two keys into
// blobfs_directory that writeSentinels lists, is blobfs.ErrNotEmpty: the directory still has child
// directories or files. No key of blobfs's own references blobfs_file, so
// the file removal never meets one. Any other foreign-key violation is a
// constraint blobfs does not own: a consumer's key that references the
// row being removed, which blobfs cannot name but can classify by class
// as blobfs.ErrReferenced, so the consumer matches the constraint's name
// against its own. Any other error is returned as it came.
func classifyDelete(err error) error {
	var ce *sqlate.ConstraintError
	if !errors.As(err, &ce) || !errors.Is(ce.Class, sqlate.ErrForeignKeyViolation) {
		return err
	}
	sentinel := blobfs.ErrReferenced
	if m, ok := writeSentinels[ce.Constraint]; ok && errors.Is(ce.Class, m.class) {
		sentinel = blobfs.ErrNotEmpty
	}
	return &blobfs.ViolationError{Sentinel: sentinel, Constraint: ce.Constraint, Err: err}
}

// notFound maps sql.ErrNoRows, which the typed handles return unmapped, to
// blobfs.ErrNotFound and leaves every other error as it came.
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return blobfs.ErrNotFound
	}
	return err
}

// versionMismatch is the optimistic-concurrency conflict of a guarded
// command whose row sits at current when the caller expected expected:
// query.ErrVersionMismatch with both versions in the text, as the query
// library's own guards spell it. The commands classify it themselves, from
// the row their read returned, because a deleting row outranks the
// version: the library's guard reports a mismatch before it looks at the
// row.
func versionMismatch(expected, current int64) error {
	return fmt.Errorf("%w: expected %d, current %d", query.ErrVersionMismatch, expected, current)
}

// wrap names the operation an exported method ran on the error err points
// to, when it is not nil: "data: " and the operation, format and args, as
// the method's identifying arguments give it, ahead of the cause. Each
// exported method defers it once, so its body returns bare errors carrying
// only what that return knows, and every return is named the same way.
// The cause stays reachable through errors.Is and errors.As.
func wrap(err *error, format string, args ...any) {
	if *err != nil {
		*err = fmt.Errorf("data: %s: %w", fmt.Sprintf(format, args...), *err)
	}
}
