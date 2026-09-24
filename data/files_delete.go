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

// Hold locks the row of the file with id for the rest of tx, so that no
// Delete of the file begins before tx ends: the library's half of the
// reference-then-delete rule. A consumer calls it before it inserts a row
// that references the file, in the same transaction as the insert, and a
// Delete, whose update takes the same row lock, waits for that transaction
// and then sees the reference. The hold changes no value and advances no
// version, so other holders of the row's version stay valid. Only a row that
// is not deleting is held, because a file whose delete has begun must take
// no new reference; a pending row is held like an available one. It takes a
// *sqlate.Tx because on the pool the lock would be released as the
// statement ends and hold nothing.
//
// The hold is a variation point, Variant.HoldFile. The baseline takes the
// lock with an update that assigns a column to itself, which is portable;
// an engine's variant may take the same lock without writing a row version,
// as the PostgreSQL engine does with SELECT ... FOR NO KEY UPDATE. The
// refusals are the same on every variant.
//
// A file that does not exist is blobfs.ErrNotFound. A row that is deleting
// is blobfs.ErrDeleting, whatever its version, since no version will make
// it holdable. With AtVersion, a row that is not deleting and sits at
// another version is query.ErrVersionMismatch, with the expected and
// current versions in the text. When the hold holds no row, the row is
// read once more, in tx, to classify.
func (f *Files) Hold(ctx context.Context, tx *sqlate.Tx, id string, opts ...HoldOption) error {
	var o holdOptions
	for _, opt := range opts {
		opt(&o)
	}
	var version *int64
	if o.hasVersion {
		version = &o.version
	}
	held, err := f.variant.HoldFile(ctx, tx, id, version)
	if err != nil {
		return fmt.Errorf("data: hold file %s: %w", id, err)
	}
	if held {
		return nil
	}
	// No row was held: the row is gone, deleting, or at another version.
	file, err := f.byID.One(ctx, tx, query.Args{"id": id})
	switch {
	case err != nil:
		return fmt.Errorf("data: hold file %s: %w", id, notFound(err))
	case file.Status == blobfs.StatusDeleting:
		return fmt.Errorf("data: hold file %s: the row is %s: %w", id, file.Status, blobfs.ErrDeleting)
	case o.hasVersion && file.Version != o.version:
		return fmt.Errorf("data: hold file %s: %w", id, versionMismatch(o.version, file.Version))
	}
	return fmt.Errorf("data: hold file %s: the hold matched no row, yet the row is %s at version %d", id, file.Status, file.Version)
}

// Delete is the first step of a file delete: it moves the row of the file
// with id to blobfs.StatusDeleting, advances its version, and returns the
// row as the database holds it afterward, so the caller deletes the object
// under its Key next and then calls Purge. A row that is already deleting
// is returned as it is, with no version change, so a retry converges; the
// version advances once per delete. The transition is allowed from pending
// and available alike, so an abandoned write is removed the same way. A
// file that does not exist is blobfs.ErrNotFound, whether the id never
// existed or a concurrent delete purged it first.
//
// It takes a *sqlate.Tx because it is the delete's half of the
// reference-then-delete rule: its update takes the row's lock and waits on a
// Hold another transaction took, so the delete and a consumer's new
// reference to the file serialize on the row. Once Delete returns, every
// reference a Hold admitted has committed, so a consumer checks for its own
// references in tx after the call, and a foreign key of its own refuses
// Purge while one remains. The update is a returning command: the
// single-statement form where the dialect renders RETURNING, and otherwise
// the fallback, the update and a read of the row.
func (f *Files) Delete(ctx context.Context, tx *sqlate.Tx, id string) (blobfs.File, error) {
	file, _, err := f.remove.One(ctx, tx, query.Args{"id": id})
	if err != nil {
		return blobfs.File{}, fmt.Errorf("data: delete file %s: %w", id, notFound(err))
	}
	return file, nil
}

// Purge is the last step of a file delete: it removes the row of the file
// with id, after the caller has deleted the object under the row's Key.
// Only a deleting row is removed. A row that is already gone is success,
// because the step's postcondition is the row's absence and a retry after
// a crash cannot tell its own earlier completion from a row that never
// existed; a caller that wants to report a missing file resolves it before
// Delete. A row that exists and is not deleting is blobfs.ErrNotDeleting
// and is left as it is: its delete has not begun, so the object may still
// be wanted.
//
// A foreign key from a consumer's table that references the row refuses the
// removal as blobfs.ErrReferenced, with the sqlate.ConstraintError
// reachable, so the consumer matches the constraint's name against its own;
// the row stays deleting, and a retry after the consumer's row is gone
// converges. Purge runs one statement to remove the row, so the session may
// be the pool or a transaction.
func (f *Files) Purge(ctx context.Context, sess sqlate.Session, id string) error {
	args := query.Args{"id": id}
	n, err := f.purge.Exec(ctx, sess, args)
	if err != nil {
		return fmt.Errorf("data: purge file %s: %w", id, classifyDelete(err))
	}
	if n > 0 {
		return nil
	}
	// No row was removed: the row is gone, which is success, or it exists
	// in a status the removal refuses.
	file, err := f.byID.One(ctx, sess, args)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("data: purge file %s: %w", id, err)
	case file.Status == blobfs.StatusDeleting:
		// A concurrent Delete moved the row to deleting between the removal
		// and this read. The removal is repeated once; a deleting row never
		// leaves that status except by removal, so the second attempt
		// removes it or finds it gone.
		if _, err := f.purge.Exec(ctx, sess, args); err != nil {
			return fmt.Errorf("data: purge file %s: %w", id, classifyDelete(err))
		}
		return nil
	}
	return fmt.Errorf("data: purge file %s: the row is %s: %w", id, file.Status, blobfs.ErrNotDeleting)
}
