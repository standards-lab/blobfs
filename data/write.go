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

// validName returns name normalized and validated, or a blobfs.NameError.
func validName(name string) (string, error) {
	name = blobfs.NormalizeName(name)
	if err := blobfs.ValidateName(name); err != nil {
		return "", err
	}
	return name, nil
}

// rowID returns the id WithID supplied, in canonical form, or a minted one.
func rowID(opts []CreateOption) (string, error) {
	var o createOptions
	for _, opt := range opts {
		opt(&o)
	}
	if !o.hasID {
		return blobfs.NewID(), nil
	}
	return blobfs.ParseID(o.id)
}

// inTransaction reports whether sess is a transaction, where a failed
// statement may have aborted it.
func inTransaction(sess sqlate.Session) bool {
	_, ok := sess.(*sqlate.Tx)
	return ok
}

// insertOrFind runs find, then create only when find returned
// sql.ErrNoRows, and reports whether it created the row, whose id is id.
// A create refused by a concurrent creator is looked up again: on the pool
// for blobfs.ErrNameTaken, and for blobfs.ErrIDTaken when the row found
// carries id, since PostgreSQL checks the primary key before the name's
// constraint, and a lookup that fails is joined to the ErrIDTaken; in a
// transaction too for a holderError, whose insert failed no statement.
// Inside a transaction a violation is returned, since it may have aborted
// the transaction.
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

// holderError is the refusal by the deleting row that holds the name a
// create or a move asked for, marked with the holder's kind and id so it
// reads apart from the moved row's own.
type holderError struct {
	kind string
	id   string
	err  error
}

func (e *holderError) Error() string {
	return fmt.Sprintf("the %s %s holds the name: %v", e.kind, e.id, e.err)
}

func (e *holderError) Unwrap() error { return e.err }

// rerunOnce runs attempt again when it reports its refusal unexplained. At
// read committed the statement and the reads after it take separate
// snapshots, so the holder the statement saw may be gone by the reads; the
// statement failed nothing, so the rerun is safe inside a transaction.
func rerunOnce[T any](attempt func() (T, bool, error)) (T, error) {
	row, unexplained, err := attempt()
	if unexplained {
		row, _, err = attempt()
	}
	return row, err
}

// directoryReads are the directory reads, by id and by parent and name,
// that Directories, Files, and the baseline share.
type directoryReads struct {
	byID   query.Rows[blobfs.Directory]
	byName query.Rows[blobfs.Directory]
}

// newDirectoryReads binds the directory reads of a compiled set.
func newDirectoryReads(stmts *query.Statements) directoryReads {
	directory := query.Scanner[blobfs.Directory]()
	return directoryReads{
		byID:   stmts.Statement("directory_by_id").Scan(directory),
		byName: stmts.Statement("directory_by_name").Scan(directory),
	}
}

// active reads the directory with id: nil when it is active, else
// blobfs.ErrNotFound naming the directory or its blobfs.DeletingError.
func (r directoryReads) active(ctx context.Context, sess sqlate.Session, id string) error {
	dir, err := r.byID.One(ctx, sess, query.Args{"id": id})
	if err != nil {
		return fmt.Errorf("the directory %s: %w", id, notFound(err))
	}
	return closed(dir)
}

// closed is nil while dir is active and, once it is deleting, the
// blobfs.DeletingError naming it.
func closed(dir blobfs.Directory) error {
	if dir.Status.Mutable() {
		return nil
	}
	return &blobfs.DeletingError{Directory: true, ID: dir.ID}
}

// deletingFile builds the refusal of a mutation of the deleting file,
// wrapping cause: the file's own DeletingError while its directory is
// active, and the directory's once it is deleting or gone.
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

// refusedUnder classifies an insert that selected no row from parentID. It
// reads the parent, for blobfs.ErrNotFound or the parent's
// blobfs.DeletingError, and then calls held, for the refusal of a deleting
// row that holds the name. When neither explains the insert, unexplained
// is true and the refusal is untyped, for rerunOnce.
func (r directoryReads) refusedUnder(ctx context.Context, sess sqlate.Session, parentID string, held func() error) (unexplained bool, _ error) {
	if err := r.active(ctx, sess, parentID); err != nil {
		return false, err
	}
	if err := held(); err != nil {
		return false, err
	}
	return true, fmt.Errorf("the insert selected no row, yet the directory %s is %s and no deleting row holds the name", parentID, blobfs.DirectoryStatusActive)
}

// refusedMove classifies a move of a row that is not deleting whose update
// changed nothing, by reading from, its current parent (nil for the
// root), and to, its new one. The refusal is blobfs.ErrDeleting, then
// query.ErrVersionMismatch, then blobfs.ErrNotFound for a missing new
// parent; nil means none explains it.
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
