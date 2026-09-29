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

// Hold locks the row of the file with id for the rest of tx without
// changing it, so a Delete of the file waits for tx: the library's half of
// the reference-then-delete rule. It takes a *sqlate.Tx because on the
// pool the lock would end with the statement. The lock is the variant's
// (see Variant.HoldFile). See Reference-then-delete in docs/concepts.md.
//
// Refusals: blobfs.ErrNotFound; a blobfs.DeletingError for a deleting
// row, the file's or its directory's as a read of the directory tells;
// query.ErrVersionMismatch under AtVersion.
func (f *Files) Hold(ctx context.Context, tx *sqlate.Tx, id string, opts ...VersionOption) (err error) {
	defer wrap(&err, "hold file %s", id)
	version := atVersion(opts)
	held, err := f.variant.HoldFile(ctx, tx, id, version)
	switch {
	case err != nil:
		return err
	case held:
		return nil
	}
	// No row was held: the row is gone, deleting, or at another version.
	file, err := f.byID.One(ctx, tx, query.Args{"id": id})
	switch {
	case err != nil:
		return notFound(err)
	case !file.Status.Mutable():
		return f.dirs.deletingFile(ctx, tx, file, nil)
	case version != nil && file.Version != *version:
		return versionMismatch(*version, file.Version)
	}
	return fmt.Errorf("the hold matched no row, yet the row is %s at version %d", file.Status, file.Version)
}

// Delete is the first step of a file delete: it moves the row of the file
// with id, pending or available, to blobfs.StatusDeleting, advancing its
// version once, and returns it; the caller deletes the object under its
// Key and then calls Purge. A row already deleting is returned as it is,
// so a retry converges. It takes a *sqlate.Tx because its update waits on
// a Hold another transaction took; the consumer checks for its own
// references in tx after the call.
//
// Refusals: blobfs.ErrNotFound; query.ErrVersionMismatch under AtVersion.
func (f *Files) Delete(ctx context.Context, tx *sqlate.Tx, id string, opts ...VersionOption) (_ blobfs.File, err error) {
	defer wrap(&err, "delete file %s", id)
	return f.deleteFile(ctx, tx, id, atVersion(opts))
}

// deleteFile is Delete's body, at version when it is not nil, with its
// errors bare for the sweep.
func (f *Files) deleteFile(ctx context.Context, tx *sqlate.Tx, id string, version *int64) (blobfs.File, error) {
	file, _, err := f.remove.One(ctx, tx, withVersion(query.Args{"id": id}, version))
	switch {
	case err != nil:
		return blobfs.File{}, notFound(err)
	case version != nil && file.Status.Mutable():
		// The update matched nothing and the row is not deleting.
		return blobfs.File{}, versionMismatch(*version, file.Version)
	}
	return file, nil
}

// Purge is the last step of a file delete: it removes the deleting row of
// the file with id, after the caller deleted its object. A row already
// gone is success.
//
// Refusals: blobfs.ErrNotDeleting for a row whose delete has not begun;
// blobfs.ErrReferenced for a consumer's foreign key, which leaves the row
// deleting.
func (f *Files) Purge(ctx context.Context, sess sqlate.Session, id string) (err error) {
	defer wrap(&err, "purge file %s", id)
	return f.purgeFile(ctx, sess, id)
}

// purgeFile is Purge's body, with its errors bare for the sweep.
func (f *Files) purgeFile(ctx context.Context, sess sqlate.Session, id string) error {
	args := query.Args{"id": id}
	n, err := f.purge.Exec(ctx, sess, args)
	switch {
	case err != nil:
		return classifyDelete(err)
	case n > 0:
		return nil
	}
	// No row was removed: the row is gone, which is success, or it exists
	// in a status the removal refuses.
	file, err := f.byID.One(ctx, sess, args)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	case !file.Status.Mutable():
		// A concurrent Delete moved the row to deleting since the removal;
		// once deleting it stays so, so one more removal settles it.
		if _, err := f.purge.Exec(ctx, sess, args); err != nil {
			return classifyDelete(err)
		}
		return nil
	}
	return fmt.Errorf("the row is %s: %w", file.Status, blobfs.ErrNotDeleting)
}
