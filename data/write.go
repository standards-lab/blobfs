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

// validName normalizes name and validates the normalized form, returning
// the form to store. A refusal is the root package's NameError.
func validName(name string) (string, error) {
	name = blobfs.NormalizeName(name)
	if err := blobfs.ValidateName(name); err != nil {
		return "", err
	}
	return name, nil
}

// rowID resolves the id a create inserts under: the caller's, in canonical
// form, or a minted one when no option supplied any.
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

// inTransaction reports whether sess is a transaction. An insert-or-find
// that hit a unique violation does not look the row up again inside one,
// because on PostgreSQL a failed statement aborts the transaction and
// every later statement in it fails.
func inTransaction(sess sqlate.Session) bool {
	_, ok := sess.(*sqlate.Tx)
	return ok
}

// insertOrFind is the insert-or-find both handles' Ensure runs: find looks
// the name up and returns sql.ErrNoRows when no row holds it, and create
// inserts the row and returns a violation already classified. The lookup
// runs first and the insert only when it found no row, so the common case
// runs no failing statement and composes into a caller's transaction. It
// reports whether this call created the row.
//
// A creator that commits between the lookup and the insert makes the
// insert fail as blobfs.ErrNameTaken. On a session that is not a
// transaction the row is then looked up again and returned as found.
// Inside a transaction the error is returned instead, because the failed
// insert may have aborted the transaction, and the caller retries it. Any
// other refusal of the insert is returned as it came.
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
	// A concurrent creator committed the name between the lookup and the
	// insert, so the row exists.
	row, err = find(ctx, sess)
	if err != nil {
		return zero, false, fmt.Errorf("after a concurrent create: %w", notFound(err))
	}
	return row, false, nil
}

// directoryReads are the reads of one directory the handles share, bound
// once in New: by id, the read every refusal a directory decides is
// classified from, and by parent and name. Directories, Files, and the
// baseline variant hold the same pair.
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

// active reads the directory with id in sess and reports whether it takes
// a change beneath it: nil for an active directory, blobfs.ErrNotFound for
// one that does not exist, and blobfs.ErrDeleting for one that is
// deleting, each naming the directory. Any other error of the read is
// returned naming the directory too.
func (r directoryReads) active(ctx context.Context, sess sqlate.Session, id string) error {
	dir, err := r.byID.One(ctx, sess, query.Args{"id": id})
	if err != nil {
		return fmt.Errorf("the directory %s: %w", id, notFound(err))
	}
	return closed(dir)
}

// closed is the refusal of a change beneath dir or of dir itself: nil
// while dir is active, and blobfs.ErrDeleting naming it once its branch is
// marked for removal.
func closed(dir blobfs.Directory) error {
	if dir.Status.Mutable() {
		return nil
	}
	return fmt.Errorf("the directory %s is %s: %w", dir.ID, dir.Status, blobfs.ErrDeleting)
}

// refusedUnder classifies an insert that selected no row from its parent,
// a directory the insert names by parentID and takes only while it is
// active: a parent that does not exist is blobfs.ErrNotFound, and one that
// is deleting blobfs.ErrDeleting. The parent is read in sess, once the
// insert has run.
func (r directoryReads) refusedUnder(ctx context.Context, sess sqlate.Session, parentID string) error {
	if err := r.active(ctx, sess, parentID); err != nil {
		return err
	}
	return fmt.Errorf("the insert selected no row, yet the directory %s is %s", parentID, blobfs.DirectoryStatusActive)
}

// refusedMove classifies a move whose guarded update changed no row
// although the row exists and is not deleting itself, from the two
// directories the move joins: from, the row's current parent, nil for the
// root, which has no parent, and to, its new one, each read in sess. The
// update's status predicates refuse a move out of or into a deleting
// directory, and a move under a new parent that does not exist, before the
// foreign key could; its guard refuses a row at current when the caller
// expected expected. A deleting directory outranks a stale version, since
// a mark advances the version, and a stale version outranks a missing new
// parent: the refusal is blobfs.ErrDeleting, then query.ErrVersionMismatch,
// then blobfs.ErrNotFound. A current parent that does not exist is passed
// over. nil means none of them explains the refusal.
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
