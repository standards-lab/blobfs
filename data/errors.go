package data

import (
	"database/sql"
	"errors"

	"github.com/standards-lab/sqlate"

	"github.com/standards-lab/blobfs"
)

// writeMapping is what a constraint means on a write: the violation class
// the constraint reports and the sentinel that class means there.
type writeMapping struct {
	class    error
	sentinel error
}

// writeSentinels maps the constraints an insert or an update can violate
// to the sentinel each one means there: a primary key is an id a
// caller supplied that a row already carries, a unique constraint on a
// name is a name already held, the root's partial unique index is a second
// root, and a foreign key to a directory is a parent or directory that
// does not exist. The mapping is the write's view of the constraint. A
// delete violates the same foreign keys with the opposite meaning (the row
// still has children), so the delete carries its own table.
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

// deleteSentinels maps the constraints a delete can violate to the
// sentinel each one means there: the two foreign keys into
// blobfs_directory mean the directory still has child directories or
// files. The mapping is the delete's view of the constraint; the same
// keys mean a missing parent on a write. No key of blobfs's own
// references blobfs_file, so the file removal never appears here.
var deleteSentinels = map[string]writeMapping{
	blobfs.ConstraintForeignKeyDirectoryParent: {sqlate.ErrForeignKeyViolation, blobfs.ErrNotEmpty},
	blobfs.ConstraintForeignKeyFileDirectory:   {sqlate.ErrForeignKeyViolation, blobfs.ErrNotEmpty},
}

// classifyDelete maps a constraint violation from a delete to blobfs's
// sentinel as a blobfs.ViolationError, whose message names the sentinel
// and the constraint and which keeps the sqlate.ConstraintError reachable
// through errors.As. A foreign key blobfs owns, under the class it
// reports, is blobfs.ErrNotEmpty. Any other foreign-key violation is a
// constraint blobfs does not own: a consumer's key that references the
// row being removed, which blobfs cannot name but can classify by class
// as blobfs.ErrReferenced, so the consumer matches the constraint's name
// against its own. Any other error is returned as it came.
func classifyDelete(err error) error {
	var ce *sqlate.ConstraintError
	if !errors.As(err, &ce) {
		return err
	}
	if m, ok := deleteSentinels[ce.Constraint]; ok && errors.Is(ce.Class, m.class) {
		return &blobfs.ViolationError{Sentinel: m.sentinel, Constraint: ce.Constraint, Err: err}
	}
	if errors.Is(ce.Class, sqlate.ErrForeignKeyViolation) {
		return &blobfs.ViolationError{Sentinel: blobfs.ErrReferenced, Constraint: ce.Constraint, Err: err}
	}
	return err
}

// notFound maps sql.ErrNoRows, which the typed handles return unmapped, to
// blobfs.ErrNotFound and leaves every other error as it came.
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return blobfs.ErrNotFound
	}
	return err
}
