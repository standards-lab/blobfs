package data

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
)

// ObjectPutter is the consumer's object store as Store.WriteFile and
// Store.EnsureFile call it. PutObject stores size bytes of body
// under key in contentType, all or nothing, replacing whatever the key
// held, and reports the stored object as Files.Complete records it.
type ObjectPutter interface {
	PutObject(ctx context.Context, key string, body io.Reader, contentType string, size int64) (blobfs.Object, error)
}

// ObjectStore is the consumer's object store as the protocols call it: the
// key check, the put, and the delete. The consumer implements it with one
// adapter over its store, which the write's begin also passes to
// Files.Create or Files.Ensure as their KeyValidator.
type ObjectStore interface {
	blobfs.KeyValidator
	ObjectPutter
	ObjectDeleter
}

// WriteFile runs the two-phase write of the file begin creates, with size
// bytes of body as its object. begin runs Files.Create in one transaction
// on db; the put runs outside any transaction; the completion runs on the
// pool and returns the row available. A failed put or completion abandons
// the write. begin's error, or its transaction's, is returned as it came.
// See The protocols in docs/features.md.
func (s *Store) WriteFile(ctx context.Context, db *sqlate.DB, objects ObjectStore, body io.Reader, size int64, begin func(*sqlate.Tx) (blobfs.File, error)) (_ blobfs.File, err error) {
	file, err := db.Transact(ctx, begin)
	if err != nil {
		return blobfs.File{}, err
	}
	defer wrap(&err, "write file %s", file.ID)
	return s.store(ctx, db, objects, body, size, file)
}

// EnsureFile is the retry-safe WriteFile of the file under the fixed id:
// begin runs Files.Ensure with WithID(id) in one transaction on db. A row
// begin created, or a pending one under id, is written as WriteFile
// writes it, and stored reports true; an available row under id is
// returned as it stands. A row under another id is not the caller's and is
// refused. See The protocols in docs/features.md.
func (s *Store) EnsureFile(ctx context.Context, db *sqlate.DB, objects ObjectStore, id string, body io.Reader, size int64, begin func(*sqlate.Tx) (blobfs.File, WriteOutcome, error)) (_ blobfs.File, stored bool, err error) {
	canonical, err := blobfs.ParseID(id)
	if err != nil {
		return blobfs.File{}, false, fmt.Errorf("data: ensure file %s: %w", id, err)
	}
	type ensured struct {
		file    blobfs.File
		outcome WriteOutcome
	}
	first := func(tx *sqlate.Tx) (ensured, error) {
		file, outcome, err := begin(tx)
		return ensured{file, outcome}, err
	}
	e, err := db.Transact(ctx, first)
	if errors.Is(err, blobfs.ErrNameTaken) || errors.Is(err, blobfs.ErrIDTaken) {
		// A concurrent writer's insert won, and the violation aborted the
		// transaction, which Files.Ensure cannot recover inside; a fresh
		// one finds the row. An id held under another name fails again.
		e, err = db.Transact(ctx, first)
	}
	if err != nil {
		return blobfs.File{}, false, err
	}
	defer wrap(&err, "ensure file %s", canonical)
	switch {
	case e.file.ID != canonical && e.outcome == WriteCreated:
		err := fmt.Errorf("begin created the file %s, not %s", e.file.ID, canonical)
		if rerr := s.remove(context.WithoutCancel(ctx), db, objects, e.file.ID, &e.file.Version); rerr != nil {
			err = errors.Join(err, fmt.Errorf("abandon: %w", rerr))
		}
		return blobfs.File{}, false, err
	case e.outcome == WritePresent && e.file.Status != blobfs.StatusAvailable:
		// A deleting row, under id or another, is refused by its delete.
		return blobfs.File{}, false, s.Files.dirs.deletingFile(ctx, db, e.file, nil)
	case e.file.ID != canonical:
		return blobfs.File{}, false, fmt.Errorf("the file %s holds the name: %w", e.file.ID, blobfs.ErrNameTaken)
	case e.outcome == WritePresent:
		return e.file, false, nil
	}
	file, err := s.store(ctx, db, objects, body, size, e.file)
	switch {
	case err == nil:
		return file, true, nil
	case errors.Is(err, blobfs.ErrDeleting), errors.Is(err, blobfs.ErrNotFound):
		// A sweep reached the row, or it is gone: no writer completed it.
		// Checked first, since a DeletingError wraps its TransitionError.
		return blobfs.File{}, false, err
	case !errors.Is(err, query.ErrVersionMismatch) && !errors.Is(err, blobfs.ErrInvalidTransition):
		return blobfs.File{}, false, err
	}
	// Another writer completed the row first.
	found, ferr := s.Files.byID.One(ctx, db, query.Args{"id": e.file.ID})
	if ferr != nil || found.Status != blobfs.StatusAvailable {
		return blobfs.File{}, false, errors.Join(err, notFound(ferr))
	}
	return found, false, nil
}

// store writes the pending file begin returned, its errors bare. The
// abandon runs only at the pending version, and a completion that finds
// the version moved on or the row available abandons nothing: another
// writer moved or completed the row.
func (s *Store) store(ctx context.Context, db *sqlate.DB, objects ObjectStore, body io.Reader, size int64, file blobfs.File) (blobfs.File, error) {
	if err := blobfs.Transition(file.Status, blobfs.StatusAvailable); err != nil {
		if !file.Status.Mutable() {
			return blobfs.File{}, s.Files.dirs.deletingFile(ctx, db, file, err)
		}
		return blobfs.File{}, err
	}
	// The cleanup outlives ctx's cancellation: a caller that hangs up
	// mid-body cancels ctx, and the abandon and the delete must still run
	// rather than leave the row to the stale reclaim.
	cleanup := context.WithoutCancel(ctx)
	abandon := func(err error) (blobfs.File, error) {
		if rerr := s.remove(cleanup, db, objects, file.ID, &file.Version); rerr != nil {
			return blobfs.File{}, errors.Join(err, fmt.Errorf("abandon: %w", rerr))
		}
		return blobfs.File{}, err
	}
	obj, err := objects.PutObject(ctx, file.Key, body, file.ContentType, size)
	if err != nil {
		return abandon(fmt.Errorf("put the object: %w", err))
	}
	done, err := s.Files.completeFile(ctx, db, file.ID, file.Version, obj)
	switch {
	case errors.Is(err, blobfs.ErrDeleting), errors.Is(err, blobfs.ErrNotFound):
		if derr := objects.DeleteObject(cleanup, file.Key); derr != nil {
			return blobfs.File{}, errors.Join(err, fmt.Errorf("delete the object: %w", derr))
		}
		return blobfs.File{}, err
	case errors.Is(err, query.ErrVersionMismatch), errors.Is(err, blobfs.ErrInvalidTransition):
		return blobfs.File{}, err
	case err != nil:
		return abandon(err)
	}
	return done, nil
}

// RemoveFile runs the two-phase delete of the file pick names. pick and
// Files.Delete under opts run in one transaction on db, where the caller
// removes its own references to the file; RemoveFile then deletes the
// object and purges the row, as PurgeFile does. A retry of an interrupted
// RemoveFile finishes it; a retry of a finished one returns
// blobfs.ErrNotFound. pick's error, or its transaction's, is returned as it
// came. See The protocols in docs/features.md.
func (s *Store) RemoveFile(ctx context.Context, db *sqlate.DB, objects ObjectDeleter, pick func(*sqlate.Tx) (string, error), opts ...VersionOption) (err error) {
	version := atVersion(opts)
	var id string
	picked := false
	file, err := db.Transact(ctx, func(tx *sqlate.Tx) (blobfs.File, error) {
		var err error
		if id, err = pick(tx); err != nil {
			return blobfs.File{}, err
		}
		picked = true
		return s.Files.deleteFile(ctx, tx, id, version)
	})
	if !picked {
		return err
	}
	defer wrap(&err, "remove file %s", id)
	if err != nil {
		return err
	}
	return s.purge(ctx, db, objects, file)
}

// RemoveFileID is RemoveFile of the file with id, for a caller with no
// scope to check and no reference to remove in the delete's transaction.
func (s *Store) RemoveFileID(ctx context.Context, db *sqlate.DB, objects ObjectDeleter, id string, opts ...VersionOption) (err error) {
	defer wrap(&err, "remove file %s", id)
	return s.remove(ctx, db, objects, id, atVersion(opts))
}

// remove is RemoveFileID at version, or at any version when version is
// nil, with its errors bare for the write's abandon.
func (s *Store) remove(ctx context.Context, db *sqlate.DB, objects ObjectDeleter, id string, version *int64) error {
	file, err := db.Transact(ctx, func(tx *sqlate.Tx) (blobfs.File, error) {
		return s.Files.deleteFile(ctx, tx, id, version)
	})
	if err != nil {
		return err
	}
	return s.purge(ctx, db, objects, file)
}

// PurgeFile runs the delete's last two steps for a file whose
// Files.Delete the caller ran itself: it deletes the object under the
// file's Key, then purges the row on db. An object delete that fails
// leaves the row deleting, which a retry or the sweep's stale reclaim
// finishes.
func (s *Store) PurgeFile(ctx context.Context, db *sqlate.DB, objects ObjectDeleter, file blobfs.File) (err error) {
	defer wrap(&err, "purge file %s", file.ID)
	return s.purge(ctx, db, objects, file)
}

// purge is PurgeFile's body, with its errors bare.
func (s *Store) purge(ctx context.Context, db *sqlate.DB, objects ObjectDeleter, file blobfs.File) error {
	if err := objects.DeleteObject(ctx, file.Key); err != nil {
		return fmt.Errorf("delete the object: %w", err)
	}
	if err := s.Files.purgeFile(ctx, db, file.ID); err != nil {
		return fmt.Errorf("purge the row: %w", err)
	}
	return nil
}
