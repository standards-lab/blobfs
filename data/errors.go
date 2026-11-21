package data

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
)

type writeMapping struct {
	class    error
	sentinel error
}

var writeSentinels = map[string]writeMapping{
	blobfs.ConstraintPrimaryKeyDirectory:       {sqlate.ErrUniqueViolation, blobfs.ErrIDTaken},
	blobfs.ConstraintPrimaryKeyFile:            {sqlate.ErrUniqueViolation, blobfs.ErrIDTaken},
	blobfs.ConstraintUniqueDirectoryRoot:       {sqlate.ErrUniqueViolation, blobfs.ErrRootDirectory},
	blobfs.ConstraintUniqueDirectoryParentName: {sqlate.ErrUniqueViolation, blobfs.ErrNameTaken},
	blobfs.ConstraintUniqueFileDirectoryName:   {sqlate.ErrUniqueViolation, blobfs.ErrNameTaken},
	blobfs.ConstraintForeignKeyDirectoryParent: {sqlate.ErrForeignKeyViolation, blobfs.ErrNotFound},
	blobfs.ConstraintForeignKeyFileDirectory:   {sqlate.ErrForeignKeyViolation, blobfs.ErrNotFound},
}

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

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return blobfs.ErrNotFound
	}
	return err
}

func versionMismatch(expected, current int64) error {
	return fmt.Errorf("%w: expected %d, current %d", query.ErrVersionMismatch, expected, current)
}

func wrap(err *error, format string, args ...any) {
	if *err != nil {
		*err = fmt.Errorf("data: %s: %w", fmt.Sprintf(format, args...), *err)
	}
}
