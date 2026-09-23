package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/standards-lab/sqlate"

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
// because on Postgres a failed statement aborts the transaction and every
// later statement in it fails.
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
	// insert; the row exists now.
	row, err = find(ctx, sess)
	if err != nil {
		return zero, false, fmt.Errorf("after a concurrent create: %w", notFound(err))
	}
	return row, false, nil
}
