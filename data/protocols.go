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

// ObjectPutter is the consumer's object store as Store.Write and
// Store.Ensure call it. PutObject stores size bytes of body under key in
// contentType, all or nothing, replacing whatever the key held, and
// reports the stored object as Files.Complete records it.
type ObjectPutter interface {
	PutObject(ctx context.Context, key string, body io.Reader, contentType string, size int64) (blobfs.Object, error)
}

// ObjectStore is the consumer's object store as the write protocol calls
// it: the put, and the delete that abandons a write or undoes a put that
// lost its row.
type ObjectStore interface {
	ObjectPutter
	ObjectDeleter
}

// Write runs the two-phase write of the file begin creates, with size bytes
// of body as its object. It runs begin in one transaction on db, where the
// caller checks its own scope and runs Files.Create or Files.Ensure, so the
// pending row commits before any byte is stored. It then puts body under
// the row's Key through objects, outside any transaction, in the content
// type the row declares, and completes the row on the pool, returning it
// available. begin's error, or its transaction's, is returned as it came.
// See The two-phase write in docs/concepts.md.
//
// A put or a completion that fails abandons the write through Remove, so
// the name is free for a retry; the abandon and the delete below run under
// ctx without its cancellation, so a caller that hangs up mid-body still
// leaves nothing. An abandon that fails too leaves the row pending or
// deleting, a stale row the sweep reclaims; begin therefore inserts no row
// that references the file, since a reference would refuse the reclaim's
// purge. A completion refused with blobfs.ErrDeleting or
// blobfs.ErrNotFound means a sweep reached the row before the put landed:
// the write deletes the object it put, which that sweep could not have
// deleted, and leaves the row to the sweep.
func (s *Store) Write(ctx context.Context, db *sqlate.DB, objects ObjectStore, body io.Reader, size int64, begin func(*sqlate.Tx) (blobfs.File, error)) (_ blobfs.File, err error) {
	file, err := db.Transact(ctx, begin)
	if err != nil {
		return blobfs.File{}, err
	}
	defer wrap(&err, "write file %s", file.ID)
	return s.store(ctx, db, objects, body, size, file)
}

// Ensure is the retry-safe form of Write for a file under the fixed id,
// such as a seed's: begin runs in one transaction on db, where the caller
// runs Files.Ensure with WithID(id), and reports what it did. A row it
// created, or a pending row an earlier write under id left, is written as
// Write writes it, and stored reports true. An available row under id is
// returned as it stands, nothing put. begin's error, or its transaction's,
// is returned as it came.
//
// A row found under another id is not the caller's: an upload of the same
// name, pending or complete. Ensure leaves it as it stands, nothing put
// and nothing removed, and reports blobfs.ErrNameTaken.
//
// Two writers may race for one row. The loser of the insert has its
// transaction aborted by the violation, so begin runs once more, in a
// fresh transaction, on blobfs.ErrNameTaken or blobfs.ErrIDTaken, and
// finds the winner's row. Once the pending row commits, the other writer
// resumes it, so the two may share one row. The abandon begins the delete
// only at the pending version it holds, so it never removes a row the
// other writer completed. When the other writer completed the row first,
// the completion finds a stale version or a row already available, and
// Ensure reads the row back and returns it as found, stored false. The two
// puts store the same bytes under the same key, all or nothing, so the
// object is whole whichever lands last. The key is the row's id and name,
// so a write under its fixed id after a reset of the tables, but not of
// the store, puts over the object the earlier write left and completes.
//
// Refusals: blobfs.IDError before any SQL; begin's; blobfs.ErrNameTaken
// for a row under another id; a blobfs.DeletingError for a deleting row
// under id; Write's.
func (s *Store) Ensure(ctx context.Context, db *sqlate.DB, objects ObjectStore, id string, body io.Reader, size int64, begin func(*sqlate.Tx) (blobfs.File, WriteOutcome, error)) (_ blobfs.File, stored bool, err error) {
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
		// A concurrent writer committed the row between the lookup and the
		// insert, whose failure aborted the transaction; a fresh one finds
		// it. An id a row holds under another name fails again.
		e, err = db.Transact(ctx, first)
	}
	if err != nil {
		return blobfs.File{}, false, err
	}
	defer wrap(&err, "ensure file %s", canonical)
	switch {
	case e.outcome != WriteCreated && e.file.ID != canonical:
		return blobfs.File{}, false, fmt.Errorf("the file %s holds the name: %w", e.file.ID, blobfs.ErrNameTaken)
	case e.outcome == WritePresent && e.file.Status != blobfs.StatusAvailable:
		return blobfs.File{}, false, s.Files.dirs.deletingFile(ctx, db, e.file, nil)
	case e.outcome == WritePresent:
		return e.file, false, nil
	}
	file, err := s.store(ctx, db, objects, body, size, e.file, AtVersion(e.file.Version))
	if !errors.Is(err, query.ErrVersionMismatch) && !errors.Is(err, blobfs.ErrInvalidTransition) {
		return file, err == nil, err
	}
	// Another writer completed the row first.
	found, ferr := s.Files.byID.One(ctx, db, query.Args{"id": e.file.ID})
	if ferr != nil || found.Status != blobfs.StatusAvailable {
		return blobfs.File{}, false, errors.Join(err, notFound(ferr))
	}
	return found, false, nil
}

// store is the write after its first transaction: the put of body under
// the pending file's key, outside any transaction, then the completion on
// the pool, with the abandon and the delete Write describes, its errors
// bare. guard is the abandon's version guard: none for a row the writer
// holds alone, the pending version for a row Ensure may share. A guarded
// write abandons nothing when its completion finds the version moved on or
// the row available, since another writer completed it.
func (s *Store) store(ctx context.Context, db *sqlate.DB, objects ObjectStore, body io.Reader, size int64, file blobfs.File, guard ...VersionOption) (blobfs.File, error) {
	// The cleanup outlives ctx's cancellation: a caller that hangs up
	// mid-body cancels ctx, and the abandon and the delete must still run
	// rather than leave the row to the stale reclaim.
	cleanup := context.WithoutCancel(ctx)
	abandon := func(err error) (blobfs.File, error) {
		if rerr := s.remove(cleanup, db, objects, file.ID, atVersion(guard)); rerr != nil {
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
	case len(guard) > 0 && (errors.Is(err, query.ErrVersionMismatch) || errors.Is(err, blobfs.ErrInvalidTransition)):
		return blobfs.File{}, err
	case err != nil:
		return abandon(err)
	}
	return done, nil
}

// Remove runs the two-phase delete of the file pick names. It runs pick in
// one transaction on db, where the caller checks its own scope and removes
// its own references to the file, and begins the delete there with
// Files.Delete under opts; then it deletes the object through objects and
// purges the row, as Purge does. Every step converges on a retry, and a
// delete begun at any version is the retry of one already begun. pick's
// error, or its transaction's, is returned as it came. See The two-phase
// delete in docs/concepts.md.
//
// Refusals: pick's; Files.Delete's; Purge's.
func (s *Store) Remove(ctx context.Context, db *sqlate.DB, objects ObjectDeleter, pick func(*sqlate.Tx) (string, error), opts ...VersionOption) (err error) {
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

// remove is Remove of the file with id, at version when it is not nil,
// with its errors bare for the write's abandon.
func (s *Store) remove(ctx context.Context, db *sqlate.DB, objects ObjectDeleter, id string, version *int64) error {
	file, err := db.Transact(ctx, func(tx *sqlate.Tx) (blobfs.File, error) {
		return s.Files.deleteFile(ctx, tx, id, version)
	})
	if err != nil {
		return err
	}
	return s.purge(ctx, db, objects, file)
}

// Purge runs the delete's last two steps, for a file whose Files.Delete
// the caller ran in a transaction of its own: it deletes the object under
// the deleting file's Key through objects, then purges the row on db. It
// converges on a retry as Remove does; a purge that never runs leaves a
// deleting row the sweep's stale reclaim finishes.
//
// Refusals: the object delete's error, which leaves the row deleting;
// Files.Purge's.
func (s *Store) Purge(ctx context.Context, db *sqlate.DB, objects ObjectDeleter, file blobfs.File) (err error) {
	defer wrap(&err, "purge file %s", file.ID)
	return s.purge(ctx, db, objects, file)
}

// purge is Purge's body, with its errors bare.
func (s *Store) purge(ctx context.Context, db *sqlate.DB, objects ObjectDeleter, file blobfs.File) error {
	if err := objects.DeleteObject(ctx, file.Key); err != nil {
		return fmt.Errorf("delete the object: %w", err)
	}
	if err := s.Files.purgeFile(ctx, db, file.ID); err != nil {
		return fmt.Errorf("purge the row: %w", err)
	}
	return nil
}
