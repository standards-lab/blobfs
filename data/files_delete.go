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
// the reference-then-delete rule. Refusals: blobfs.ErrNotFound, a
// blobfs.DeletingError, and query.ErrVersionMismatch under opts.Version.
// See Reference-then-delete in docs/concepts.md.
func (f *Files) Hold(ctx context.Context, tx *sqlate.Tx, id string, opts VersionOptions) (err error) {
	defer wrap(&err, "hold file %s", id)
	version := opts.at()
	held, err := f.variant.HoldFile(ctx, tx, id, version)
	switch {
	case err != nil:
		return err
	case held:
		return nil
	}
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

// Delete is the delete's first step: it moves the row of the file with id
// to deleting and returns it; the caller deletes the object under its Key
// and then calls Purge. A row already deleting is returned as it is, the
// step's retry. The consumer checks for its own references in tx after the
// call. Refusals: blobfs.ErrNotFound; query.ErrVersionMismatch under
// opts.Version. See Files in docs/features.md.
func (f *Files) Delete(ctx context.Context, tx *sqlate.Tx, id string, opts VersionOptions) (_ blobfs.File, err error) {
	defer wrap(&err, "delete file %s", id)
	file, _, err := f.deleteFile(ctx, tx, id, opts.at())
	return file, err
}

func (f *Files) deleteFile(ctx context.Context, tx *sqlate.Tx, id string, version *int64) (_ blobfs.File, marked bool, _ error) {
	file, changed, err := f.remove.One(ctx, tx, withVersion(query.Args{"id": id}, version))
	switch {
	case err != nil:
		return blobfs.File{}, false, notFound(err)
	case version != nil && file.Status.Mutable():
		return blobfs.File{}, false, versionMismatch(*version, file.Version)
	}
	return file, changed, nil
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

func (f *Files) purgeFile(ctx context.Context, sess sqlate.Session, id string) error {
	args := query.Args{"id": id}
	n, err := f.purge.Exec(ctx, sess, args)
	switch {
	case err != nil:
		return classifyDelete(err)
	case n > 0:
		return nil
	}
	file, err := f.byID.One(ctx, sess, args)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	case !file.Status.Mutable():
		if _, err := f.purge.Exec(ctx, sess, args); err != nil {
			return classifyDelete(err)
		}
		return nil
	}
	return fmt.Errorf("the row is %s: %w", file.Status, blobfs.ErrNotDeleting)
}
