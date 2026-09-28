package data

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
)

// writeMapping is the violation class a constraint reports on a write and
// the sentinel it means there.
type writeMapping struct {
	class    error
	sentinel error
}

// writeSentinels maps the constraints a write can violate to their
// sentinels; classifyDelete reads the foreign keys the opposite way.
var writeSentinels = map[string]writeMapping{
	blobfs.ConstraintPrimaryKeyDirectory:       {sqlate.ErrUniqueViolation, blobfs.ErrIDTaken},
	blobfs.ConstraintPrimaryKeyFile:            {sqlate.ErrUniqueViolation, blobfs.ErrIDTaken},
	blobfs.ConstraintUniqueDirectoryRoot:       {sqlate.ErrUniqueViolation, blobfs.ErrRootDirectory},
	blobfs.ConstraintUniqueDirectoryParentName: {sqlate.ErrUniqueViolation, blobfs.ErrNameTaken},
	blobfs.ConstraintUniqueFileDirectoryName:   {sqlate.ErrUniqueViolation, blobfs.ErrNameTaken},
	blobfs.ConstraintForeignKeyDirectoryParent: {sqlate.ErrForeignKeyViolation, blobfs.ErrNotFound},
	blobfs.ConstraintForeignKeyFileDirectory:   {sqlate.ErrForeignKeyViolation, blobfs.ErrNotFound},
}

// classifyWrite maps a violation of a constraint writeSentinels lists to a
// blobfs.ViolationError and returns any other error as it came.
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

// classifyDelete maps a foreign-key violation from a delete to a
// blobfs.ViolationError: blobfs.ErrNotEmpty for one of blobfs's own keys,
// and blobfs.ErrReferenced, by class, for a consumer's. Any other error is
// returned as it came.
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

// notFound maps sql.ErrNoRows to blobfs.ErrNotFound.
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return blobfs.ErrNotFound
	}
	return err
}

// versionMismatch is query.ErrVersionMismatch with both versions in the
// text, as the query library's guards spell it. The commands build it
// themselves, since the library's guard would report it before ErrDeleting.
func versionMismatch(expected, current int64) error {
	return fmt.Errorf("%w: expected %d, current %d", query.ErrVersionMismatch, expected, current)
}

// wrap prefixes a non-nil *err with "data: " and the operation. Each
// exported method defers it once, so its body returns bare errors.
func wrap(err *error, format string, args ...any) {
	if *err != nil {
		*err = fmt.Errorf("data: %s: %w", fmt.Sprintf(format, args...), *err)
	}
}
