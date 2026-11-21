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

func validName(name string) (string, error) {
	name = blobfs.NormalizeName(name)
	if err := blobfs.ValidateName(name); err != nil {
		return "", err
	}
	return name, nil
}

func rowID(o CreateOptions) (string, error) {
	if o.ID == "" {
		return blobfs.NewID(), nil
	}
	return blobfs.ParseID(o.ID)
}

func inTransaction(sess sqlate.Session) bool {
	_, ok := sess.(*sqlate.Tx)
	return ok
}

func insertOrFind[T any](ctx context.Context, sess sqlate.Session, id string, idOf func(T) string, find, create func(context.Context, sqlate.Session) (T, error)) (T, bool, error) {
	var zero T
	row, err := find(ctx, sess)
	switch {
	case err == nil:
		return row, false, nil
	case !errors.Is(err, sql.ErrNoRows):
		return zero, false, err
	}
	row, err = create(ctx, sess)
	var held *holderError
	switch {
	case err == nil:
		return row, true, nil
	case errors.As(err, &held):
	case inTransaction(sess):
		return zero, false, err
	case errors.Is(err, blobfs.ErrIDTaken):
		found, ferr := find(ctx, sess)
		switch {
		case ferr == nil && idOf(found) == id:
			return found, false, nil
		case ferr != nil && !errors.Is(ferr, sql.ErrNoRows):
			return zero, false, errors.Join(err, fmt.Errorf("after a concurrent create: %w", ferr))
		}
		return zero, false, err
	case !errors.Is(err, blobfs.ErrNameTaken):
		return zero, false, err
	}
	found, ferr := find(ctx, sess)
	switch {
	case ferr == nil:
		return found, false, nil
	case held != nil:
		return zero, false, err
	}
	return zero, false, fmt.Errorf("after a concurrent create: %w", notFound(ferr))
}

type holderError struct {
	kind string
	id   string
	err  error
}

func (e *holderError) Error() string {
	return fmt.Sprintf("the %s %s holds the name: %v", e.kind, e.id, e.err)
}

func (e *holderError) Unwrap() error { return e.err }

func rerunOnce[T any](attempt func() (T, bool, error)) (T, error) {
	row, unexplained, err := attempt()
	if unexplained {
		row, _, err = attempt()
	}
	return row, err
}

type directoryReads struct {
	byID   query.Rows[blobfs.Directory]
	byName query.Rows[blobfs.Directory]
}

func newDirectoryReads(stmts *query.Statements) directoryReads {
	directory := query.Scanner[blobfs.Directory]()
	return directoryReads{
		byID:   stmts.Statement("directory_by_id").Scan(directory),
		byName: stmts.Statement("directory_by_name").Scan(directory),
	}
}

func (r directoryReads) active(ctx context.Context, sess sqlate.Session, id string) error {
	dir, err := r.byID.One(ctx, sess, query.Args{"id": id})
	if err != nil {
		return fmt.Errorf("the directory %s: %w", id, notFound(err))
	}
	return closed(dir)
}

func closed(dir blobfs.Directory) error {
	if dir.Status.Mutable() {
		return nil
	}
	return &blobfs.DeletingError{Directory: true, ID: dir.ID}
}

func (r directoryReads) deletingFile(ctx context.Context, sess sqlate.Session, file blobfs.File, cause error) error {
	dir, err := r.byID.One(ctx, sess, query.Args{"id": file.DirectoryID})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return &blobfs.DeletingError{Directory: true, ID: file.DirectoryID, Err: cause}
	case err != nil:
		return fmt.Errorf("the file %s is %s, and its directory %s is unread: %w", file.ID, file.Status, file.DirectoryID, errors.Join(blobfs.ErrDeleting, cause, err))
	case !dir.Status.Mutable():
		return &blobfs.DeletingError{Directory: true, ID: dir.ID, Err: cause}
	}
	return &blobfs.DeletingError{ID: file.ID, Err: cause}
}

func (r directoryReads) refusedUnder(ctx context.Context, sess sqlate.Session, parentID string, held func() error) (unexplained bool, _ error) {
	if err := r.active(ctx, sess, parentID); err != nil {
		return false, err
	}
	if err := held(); err != nil {
		return false, err
	}
	return true, fmt.Errorf("the insert selected no row, yet the directory %s is %s and no deleting row holds the name", parentID, blobfs.DirectoryStatusActive)
}

func (r directoryReads) refusedMove(ctx context.Context, sess sqlate.Session, from *string, to string, expected, current int64) error {
	if from != nil {
		if err := r.active(ctx, sess, *from); err != nil && !errors.Is(err, blobfs.ErrNotFound) {
			return err
		}
	}
	target := r.active(ctx, sess, to)
	switch {
	case target != nil && !errors.Is(target, blobfs.ErrNotFound):
		return target
	case current != expected:
		return versionMismatch(expected, current)
	}
	return target
}
