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
// sql.ErrNoRows, and reports whether it created the row. A
// blobfs.ErrNameTaken from create is a concurrent creator: outside a
// transaction the row is found again; inside one the error is returned.
func insertOrFind[T any](ctx context.Context, sess sqlate.Session, find, create func(context.Context, sqlate.Session) (T, error)) (T, bool, error) {
	var zero T
	row, err := find(ctx, sess)
	switch {
	case err == nil:
		return row, false, nil
	case !errors.Is(err, sql.ErrNoRows):
		return zero, false, err
	}
	row, err = create(ctx, sess)
	switch {
	case err == nil:
		return row, true, nil
	case !errors.Is(err, blobfs.ErrNameTaken) || inTransaction(sess):
		return zero, false, err
	}
	// A concurrent creator committed the name since the lookup.
	row, err = find(ctx, sess)
	if err != nil {
		return zero, false, fmt.Errorf("after a concurrent create: %w", notFound(err))
	}
	return row, false, nil
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

// deletingFile builds the refusal of a mutation of file, a deleting row,
// by reading its directory: the file's own blobfs.DeletingError while the
// directory is active, and the directory's once it is deleting or gone,
// each wrapping cause when it is not nil. A directory gone since the file
// was read, which its foreign key allows only once the file's row is gone
// too, as the sweep of its branch leaves them, is the directory's refusal.
// A read that fails otherwise leaves the refusal untyped,
// blobfs.ErrDeleting beside the read's error.
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

// refusedUnder classifies an insert that selected no row from parentID by
// reading the parent: blobfs.ErrNotFound or the parent's
// blobfs.DeletingError.
func (r directoryReads) refusedUnder(ctx context.Context, sess sqlate.Session, parentID string) error {
	if err := r.active(ctx, sess, parentID); err != nil {
		return err
	}
	return fmt.Errorf("the insert selected no row, yet the directory %s is %s", parentID, blobfs.DirectoryStatusActive)
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
