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

// Files is the handle over blobfs_file rows, the Store's Files field. A
// file is written with Create (or Ensure) and Complete around the
// consumer's put, and deleted with Delete and Purge around its object
// delete. See The two-phase write in docs/concepts.md.
type Files struct {
	variant  Variant
	dirs     directoryReads
	byID     query.Rows[blobfs.File]
	byName   query.Rows[blobfs.File]
	list     listing[blobfs.File]
	create   query.Returning[blobfs.File]
	complete query.Returning[blobfs.File]
	move     query.Returning[blobfs.File]
	remove   query.Returning[blobfs.File]
	purge    query.Statement
	stale    query.Rows[blobfs.File]
}

// newFiles binds the file statements of a compiled set.
func newFiles(stmts *query.Statements, dirs directoryReads, variant Variant) *Files {
	file := query.Scanner[blobfs.File]()
	return &Files{
		variant: variant,
		dirs:    dirs,
		byID:    stmts.Statement("file_by_id").Scan(file),
		byName:  stmts.Statement("file_by_name").Scan(file),
		list: listing[blobfs.File]{
			projection: stmts.Statement("directory_files").Project(file),
			anchor:     "directory_id",
			deleting:   string(blobfs.StatusDeleting),
			dirs:       dirs,
		},
		create:   stmts.Statement("create_file").Returning(file),
		complete: stmts.Statement("complete_file").Returning(file),
		move:     stmts.Statement("move_file").Returning(file),
		remove:   stmts.Statement("delete_file").Returning(file),
		purge:    stmts.Statement("purge_file"),
		stale:    stmts.Statement("stale_files_before").Scan(file),
	}
}

// Find returns the file with id, whatever its status, or
// blobfs.ErrNotFound.
func (f *Files) Find(ctx context.Context, sess sqlate.Session, id string) (_ blobfs.File, err error) {
	defer wrap(&err, "find file %s", id)
	file, err := f.byID.One(ctx, sess, query.Args{"id": id})
	if err != nil {
		return blobfs.File{}, notFound(err)
	}
	return file, nil
}

// FindByName returns the file named name in directoryID, whatever its
// status; the name is normalized first, and a directory of the same name
// is not found. It is how a retried write finds its pending row.
// Refusals: blobfs.NameError before any SQL, blobfs.ErrNotFound.
func (f *Files) FindByName(ctx context.Context, sess sqlate.Session, directoryID, name string) (_ blobfs.File, err error) {
	defer wrap(&err, "find file %q in %s", name, directoryID)
	if name, err = validName(name); err != nil {
		return blobfs.File{}, err
	}
	file, err := f.findByName(ctx, sess, directoryID, name)
	if err != nil {
		return blobfs.File{}, notFound(err)
	}
	return file, nil
}

// findByName reads the file of directoryID named name, already normalized;
// sql.ErrNoRows is returned unmapped.
func (f *Files) findByName(ctx context.Context, sess sqlate.Session, directoryID, name string) (blobfs.File, error) {
	return f.byName.One(ctx, sess, query.Args{"directory_id": directoryID, "name": name})
}

// Create is the write's first step: it inserts the row pending with the
// declared contentType and a key built by blobfs.NewKey and checked by
// keys, and returns it. The name, the id, and the key are checked before
// any SQL. A name a deleting file holds is refused with that file's
// blobfs.DeletingError, not blobfs.ErrNameTaken. See Files in
// docs/features.md.
func (f *Files) Create(ctx context.Context, sess sqlate.Session, keys blobfs.KeyValidator, directoryID, name, contentType string, opts ...CreateOption) (_ blobfs.File, err error) {
	defer wrap(&err, "create file %q in %s", name, directoryID)
	name, id, key, err := newFile(keys, name, opts)
	if err != nil {
		return blobfs.File{}, err
	}
	return f.insert(ctx, sess, id, directoryID, name, key, contentType)
}

// WriteOutcome is what Files.Ensure did with the name.
type WriteOutcome string

const (
	// WriteCreated reports a new pending row: the write's first step ran.
	WriteCreated WriteOutcome = "created"

	// WriteResumed reports a pending row an earlier write left, for the
	// caller to put under its Key and complete at its Version.
	WriteResumed WriteOutcome = "resumed"

	// WritePresent reports a row that is available or deleting, returned
	// unchanged; its Status says which, and the caller decides what it
	// means.
	WritePresent WriteOutcome = "present"
)

// Ensure is the retry-safe first step: it returns the row that holds name
// in directoryID, creating it pending when none does, and the WriteOutcome
// that says which. A found row, a deleting one included, keeps its own id
// and key. Its refusals are Create's, except that a deleting row that
// holds the name is returned as WritePresent, and blobfs.ErrNameTaken
// inside a transaction for a writer that commits the name after the
// lookup. See Files in docs/features.md.
func (f *Files) Ensure(ctx context.Context, sess sqlate.Session, keys blobfs.KeyValidator, directoryID, name, contentType string, opts ...CreateOption) (_ blobfs.File, _ WriteOutcome, err error) {
	defer wrap(&err, "ensure file %q in %s", name, directoryID)
	name, id, key, err := newFile(keys, name, opts)
	if err != nil {
		return blobfs.File{}, "", err
	}
	file, created, err := insertOrFind(ctx, sess, id, func(f blobfs.File) string { return f.ID },
		func(ctx context.Context, sess sqlate.Session) (blobfs.File, error) {
			return f.findByName(ctx, sess, directoryID, name)
		},
		func(ctx context.Context, sess sqlate.Session) (blobfs.File, error) {
			return f.insert(ctx, sess, id, directoryID, name, key, contentType)
		})
	switch {
	case err != nil:
		return blobfs.File{}, "", err
	case created:
		return file, WriteCreated, nil
	case file.Status == blobfs.StatusPending:
		return file, WriteResumed, nil
	}
	return file, WritePresent, nil
}

// newFile runs a file insert's checks before any SQL and returns the
// normalized name, the id, and the key, or the refusal bare.
func newFile(keys blobfs.KeyValidator, name string, opts []CreateOption) (string, string, string, error) {
	name, err := validName(name)
	if err != nil {
		return "", "", "", err
	}
	id, err := rowID(opts)
	if err != nil {
		return "", "", "", err
	}
	key, err := blobfs.NewKey(keys, id, name)
	if err != nil {
		return "", "", "", err
	}
	return name, id, key, nil
}

// insert runs create_file and classifies its refusal bare, rerunning an
// unexplained one once.
func (f *Files) insert(ctx context.Context, sess sqlate.Session, id, directoryID, name, key, contentType string) (blobfs.File, error) {
	args := query.Args{"id": id, "directory_id": directoryID, "name": name, "key": key, "content_type": contentType}
	return rerunOnce(func() (blobfs.File, bool, error) {
		file, changed, err := f.create.One(ctx, sess, args)
		switch {
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return blobfs.File{}, false, classifyWrite(err)
		case err == nil && changed:
			return file, false, nil
		}
		unexplained, err := f.dirs.refusedUnder(ctx, sess, directoryID, func() error {
			return f.refusedName(ctx, sess, directoryID, name)
		})
		return blobfs.File{}, unexplained, err
	})
}

// refusedName reads the file that holds name in directoryID after a create
// or a move selected no row: a deleting holder is refused, in a
// holderError; nil means none explains the refusal.
func (f *Files) refusedName(ctx context.Context, sess sqlate.Session, directoryID, name string) error {
	holder, err := f.findByName(ctx, sess, directoryID, name)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("the file that holds the name is unread: %w", err)
	case holder.Status.Mutable():
		return nil
	}
	return &holderError{kind: "file", id: holder.ID, err: f.dirs.deletingFile(ctx, sess, holder, nil)}
}

// Complete is the write's last step: it moves the pending row with id, at
// version, to available with what the store reported in obj, and returns
// it. Refusals: blobfs.ErrNotFound; a blobfs.DeletingError for a deleting
// row; query.ErrVersionMismatch, which a retry after a completion that
// committed meets too; a blobfs.TransitionError for a row available at
// version. See Files in docs/features.md.
func (f *Files) Complete(ctx context.Context, sess sqlate.Session, id string, version int64, obj blobfs.Object) (_ blobfs.File, err error) {
	defer wrap(&err, "complete file %s", id)
	return f.completeFile(ctx, sess, id, version, obj)
}

// completeFile is Complete's body, with its errors bare for Store.WriteFile.
func (f *Files) completeFile(ctx context.Context, sess sqlate.Session, id string, version int64, obj blobfs.Object) (blobfs.File, error) {
	file, changed, err := f.complete.One(ctx, sess, query.Args{
		"id": id, "size": obj.Size, "content_type": obj.ContentType, "etag": obj.ETag, "version": version,
	})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return blobfs.File{}, blobfs.ErrNotFound
	case err != nil:
		return blobfs.File{}, err
	case changed:
		return file, nil
	case !file.Status.Mutable() || file.Version == version:
		// Deleting outranks the stale version; a row at the expected
		// version is no longer pending.
		switch err := blobfs.Transition(file.Status, blobfs.StatusAvailable); {
		case err != nil && !file.Status.Mutable():
			return blobfs.File{}, f.dirs.deletingFile(ctx, sess, file, err)
		case err != nil:
			return blobfs.File{}, err
		}
		return blobfs.File{}, fmt.Errorf("the update matched no row, yet the row is %s at version %d", file.Status, file.Version)
	}
	return blobfs.File{}, versionMismatch(version, file.Version)
}

// Move moves the file with id into directoryID as name, guarded by
// version, and returns the row; a rename is a move within its directory.
// The key is untouched. Refusals: blobfs.NameError, blobfs.ErrNotFound,
// blobfs.ErrNameTaken, a blobfs.DeletingError, and
// query.ErrVersionMismatch. See Files in docs/features.md.
func (f *Files) Move(ctx context.Context, sess sqlate.Session, id, directoryID, name string, version int64) (_ blobfs.File, err error) {
	defer wrap(&err, "move file %s into %s as %q", id, directoryID, name)
	if name, err = validName(name); err != nil {
		return blobfs.File{}, err
	}
	args := query.Args{"id": id, "directory_id": directoryID, "name": name, "version": version}
	return rerunOnce(func() (blobfs.File, bool, error) {
		file, changed, err := f.move.One(ctx, sess, args)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return blobfs.File{}, false, blobfs.ErrNotFound
		case err != nil:
			return blobfs.File{}, false, classifyWrite(err)
		case changed:
			return file, false, nil
		case !file.Status.Mutable():
			// Deleting outranks the stale version.
			return blobfs.File{}, false, f.dirs.deletingFile(ctx, sess, file, nil)
		}
		// The directories tell a closed or missing one from a stale
		// version, and then the name's holder tells a deleting one; an
		// update none explains runs once more, as rerunOnce says.
		if err := f.dirs.refusedMove(ctx, sess, &file.DirectoryID, directoryID, version, file.Version); err != nil {
			return blobfs.File{}, false, err
		}
		if err := f.refusedName(ctx, sess, directoryID, name); err != nil {
			return blobfs.File{}, false, err
		}
		return blobfs.File{}, true, fmt.Errorf("the update matched no row, yet the row is %s at version %d", file.Status, file.Version)
	})
}
