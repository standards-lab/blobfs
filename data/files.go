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

// Files is the handle over blobfs_file rows: the Store's Files field. Every
// operation takes the context and the session first and a file by id; a
// step that must run inside a transaction takes a *sqlate.Tx instead of a
// session.
//
// A file is written in two steps around the object put, which the consumer
// runs: Create (or Ensure) inserts the row as pending and returns the key
// to store the object under, and Complete records what the store reported
// and makes the row available. A file is deleted in two steps around the
// object delete: Delete marks the row deleting and returns its key, and
// Purge removes the row. Every other mutation refuses a deleting row.
type Files struct {
	byID     query.Rows[blobfs.File]
	byName   query.Rows[blobfs.File]
	create   query.Returning[blobfs.File]
	complete query.RowGuard[blobfs.File]
	move     query.RowGuard[blobfs.File]
	remove   query.Returning[blobfs.File]
	purge    query.Statement
	hold     query.Statement
	holdAt   query.Statement
}

// newFiles binds the file statements of a compiled set.
func newFiles(stmts *query.Statements) *Files {
	file := query.Scanner[blobfs.File]()
	version := func(f blobfs.File) int64 { return f.Version }
	return &Files{
		byID:     stmts.Statement("file_by_id").Scan(file),
		byName:   stmts.Statement("file_by_name").Scan(file),
		create:   stmts.Statement("create_file").Returning(file),
		complete: stmts.Statement("complete_file").Returning(file).Guarded("version", version),
		move:     stmts.Statement("move_file").Returning(file).Guarded("version", version),
		remove:   stmts.Statement("delete_file").Returning(file),
		purge:    stmts.Statement("purge_file"),
		hold:     stmts.Statement("hold_file"),
		holdAt:   stmts.Statement("hold_file_at_version"),
	}
}

// Find returns the file with id, whatever its status, or
// blobfs.ErrNotFound.
func (f *Files) Find(ctx context.Context, sess sqlate.Session, id string) (blobfs.File, error) {
	file, err := f.byID.One(ctx, sess, query.Args{"id": id})
	if err != nil {
		return blobfs.File{}, fmt.Errorf("data: find file %s: %w", id, notFound(err))
	}
	return file, nil
}

// FindByName returns the file named name in the directory with
// directoryID, whatever its status, or blobfs.ErrNotFound. The name is
// normalized before it is compared, and a name ValidateName refuses is a
// blobfs.NameError before any SQL, since no row can hold it. Directories
// and files have separate name spaces: a directory of the same name is not
// found here. It is the last step of resolving a file's path, after the
// directory's, and how a retried write finds the pending row it resumes.
func (f *Files) FindByName(ctx context.Context, sess sqlate.Session, directoryID, name string) (blobfs.File, error) {
	name, err := validName(name)
	if err != nil {
		return blobfs.File{}, fmt.Errorf("data: find file by name: %w", err)
	}
	file, err := f.findByName(ctx, sess, directoryID, name)
	if err != nil {
		return blobfs.File{}, fmt.Errorf("data: find file %q in %s: %w", name, directoryID, notFound(err))
	}
	return file, nil
}

// findByName reads the file of directoryID named name, already normalized,
// returning sql.ErrNoRows unmapped when there is none.
func (f *Files) findByName(ctx context.Context, sess sqlate.Session, directoryID, name string) (blobfs.File, error) {
	return f.byName.One(ctx, sess, query.Args{"directory_id": directoryID, "name": name})
}

// Create is the first step of a file write: it inserts the file's row as
// blobfs.StatusPending, before any object exists, and returns the row as
// the database holds it. The row's Key is what the consumer stores the
// object under. The name is normalized and validated (a refusal is a
// blobfs.NameError), the id is minted or taken from WithID and checked (a
// refusal is a blobfs.IDError), and the key is built from the id and the
// name by blobfs.NewKey and validated against keys (a refusal is a
// blobfs.KeyError), all before any SQL. A name already held in the
// directory, by a file of any status, is blobfs.ErrNameTaken, an id
// another file carries is blobfs.ErrIDTaken, and a directory that does not
// exist is blobfs.ErrNotFound. The content type is what the caller
// declares; the row's size and entity tag stay nil until Complete.
//
// The insert returns its row: in one statement where the dialect renders
// RETURNING, and otherwise as the insert and a read of the row in one
// transaction, the caller's when sess is a *sqlate.Tx and one of its own
// when sess is the pool. Inside a caller's transaction the pending row
// commits with the caller's own rows. The caller then stores the object
// under the row's Key and calls Complete with the row's ID and Version. A
// stop between the two steps leaves the row pending, where FindByName or
// Ensure finds it for a retry to complete, and an abandoned write is
// removed through Delete and Purge.
func (f *Files) Create(ctx context.Context, sess sqlate.Session, keys blobfs.KeyValidator, directoryID, name, contentType string, opts ...CreateOption) (blobfs.File, error) {
	name, id, key, err := newFile("create file", keys, name, opts)
	if err != nil {
		return blobfs.File{}, err
	}
	file, err := f.insert(ctx, sess, id, directoryID, name, key, contentType)
	if err != nil {
		return blobfs.File{}, fmt.Errorf("data: create file %q in %s: %w", name, directoryID, err)
	}
	return file, nil
}

// WriteOutcome is what Files.Ensure did with the name: inserted a pending
// row, took up a pending row an earlier write left, or found the name held
// by a row that is available or deleting and inserted nothing.
type WriteOutcome string

const (
	// WriteCreated reports a new pending row: the write's first step ran.
	WriteCreated WriteOutcome = "created"

	// WriteResumed reports a pending row an earlier write left, returned
	// for the caller to store the object under its Key and complete at its
	// Version.
	WriteResumed WriteOutcome = "resumed"

	// WritePresent reports a row that is available or deleting, returned
	// unchanged; its Status says which. The caller decides what that
	// means: a put refuses the name, a copy skips or replaces it, and a
	// seeder skips it.
	WritePresent WriteOutcome = "present"
)

// Ensure is the first step of a file write as a retry-safe operation: it
// returns the file row that holds name in the directory with directoryID
// and the WriteOutcome that says how. No row is Create under the same
// arguments and WriteCreated. A pending row is returned as it is and
// WriteResumed, so the caller stores the object under its Key and
// completes it at its Version, as a retry of a stopped write does. An
// available or deleting row is returned as it is and WritePresent, and
// nothing is inserted. The name, the id, and the key are checked as in
// Create, before any SQL; a found row keeps its own id and key whatever
// WithID supplied.
//
// The lookup runs first and the insert only when it found no row, so the
// common paths run no failing statement and compose into a caller's
// transaction. A writer that commits the name between the lookup and the
// insert makes the insert fail as blobfs.ErrNameTaken. On the pool the row
// is then looked up again and reported by its status. Inside a transaction
// the error is returned instead, because on Postgres the failed insert has
// aborted the transaction, and the caller retries the transaction. The
// other refusals are Create's.
func (f *Files) Ensure(ctx context.Context, sess sqlate.Session, keys blobfs.KeyValidator, directoryID, name, contentType string, opts ...CreateOption) (blobfs.File, WriteOutcome, error) {
	name, id, key, err := newFile("ensure file", keys, name, opts)
	if err != nil {
		return blobfs.File{}, "", err
	}
	file, created, err := insertOrFind(ctx, sess,
		func(ctx context.Context, sess sqlate.Session) (blobfs.File, error) {
			return f.findByName(ctx, sess, directoryID, name)
		},
		func(ctx context.Context, sess sqlate.Session) (blobfs.File, error) {
			return f.insert(ctx, sess, id, directoryID, name, key, contentType)
		})
	switch {
	case err != nil:
		return blobfs.File{}, "", fmt.Errorf("data: ensure file %q in %s: %w", name, directoryID, err)
	case created:
		return file, WriteCreated, nil
	case file.Status == blobfs.StatusPending:
		return file, WriteResumed, nil
	}
	return file, WritePresent, nil
}

// newFile runs the checks a file's insert makes before any SQL: the name
// normalized and validated, the id minted or taken from the options and
// checked, and the key built from the two and validated against keys. It
// returns the normalized name, the id, and the key, or the refusal wrapped
// with op, the operation's own words.
func newFile(op string, keys blobfs.KeyValidator, name string, opts []CreateOption) (string, string, string, error) {
	name, err := validName(name)
	if err != nil {
		return "", "", "", fmt.Errorf("data: %s: %w", op, err)
	}
	id, err := rowID(opts)
	if err != nil {
		return "", "", "", fmt.Errorf("data: %s %q: %w", op, name, err)
	}
	key, err := blobfs.NewKey(keys, id, name)
	if err != nil {
		return "", "", "", fmt.Errorf("data: %s %q: %w", op, name, err)
	}
	return name, id, key, nil
}

// insert runs create_file under id and key and returns the row as the
// database holds it. The name is normalized and validated and the key
// validated already. A constraint violation is classified through the
// write mapping and returned without context, so each caller adds its own.
func (f *Files) insert(ctx context.Context, sess sqlate.Session, id, directoryID, name, key, contentType string) (blobfs.File, error) {
	file, _, err := f.create.One(ctx, sess, query.Args{
		"id": id, "directory_id": directoryID, "name": name, "key": key, "content_type": contentType,
	})
	if err != nil {
		return blobfs.File{}, classifyWrite(err)
	}
	return file, nil
}

// Complete is the last step of a file write: it moves the pending row with
// id to blobfs.StatusAvailable, records what the consumer's store reported
// about the object, advances the version, and returns the row as the
// database holds it afterward. The update is guarded by version, the value
// the caller read from the pending row, through the query library's
// optimistic-concurrency protocol, and by the row's status: only a pending
// row completes.
//
// A row that does not exist is blobfs.ErrNotFound. A row whose version
// moved on is query.ErrVersionMismatch, with the expected and current
// versions in the text. A row at the expected version that is no longer
// pending is a blobfs.TransitionError from its status to available, which
// matches blobfs.ErrDeleting when a delete began in the meantime and
// blobfs.ErrInvalidTransition when the write was completed already. The
// update returns the row: one statement where the dialect renders
// RETURNING, the update and a read of the row otherwise, and the refusals
// are told apart from the row that read returns, with no further
// statement. One statement changes the row, so the session may be the pool
// or a transaction.
func (f *Files) Complete(ctx context.Context, sess sqlate.Session, id string, version int64, obj blobfs.Object) (blobfs.File, error) {
	file, err := f.complete.Run(ctx, sess, version, query.Args{
		"id": id, "size": obj.Size, "content_type": obj.ContentType, "etag": obj.ETag,
	})
	var refused *query.RefusedError[blobfs.File]
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return blobfs.File{}, fmt.Errorf("data: complete file %s: %w", id, blobfs.ErrNotFound)
	case errors.As(err, &refused):
		// The row is at the expected version and the status predicate
		// refused it: the row is no longer pending.
		if terr := blobfs.Transition(refused.Row.Status, blobfs.StatusAvailable); terr != nil {
			return blobfs.File{}, fmt.Errorf("data: complete file %s: %w", id, terr)
		}
		return blobfs.File{}, fmt.Errorf("data: complete file %s: the update matched no row, yet the row is %s at version %d", id, refused.Row.Status, refused.Row.Version)
	case err != nil:
		return blobfs.File{}, fmt.Errorf("data: complete file %s: %w", id, err)
	}
	return file, nil
}

// Move moves the file with id into the directory with directoryID as name,
// which also renames it when the name differs, and returns the row as the
// database holds it afterward. The update is guarded by version, the value
// the caller read from the file's row, through the query library's
// optimistic-concurrency protocol, and by the row's status: a deleting row
// is left as it is. The key is untouched, so the object stays where it is
// and a rename moves nothing in the store. A pending row may move: its key
// was fixed at the insert, and a retry of its write finds it by its new
// name. No lock and no cycle check precede the update, because a file
// cannot be its own ancestor, so the session may be the pool or a
// transaction. The update returns the row: one statement where the dialect
// renders RETURNING, the update and a read of the row otherwise.
//
// A file that does not exist is blobfs.ErrNotFound, and so is a directory
// that does not exist, through the foreign key blobfs_fk_file_directory. A
// name already held by a file in the directory, by a row of any status, is
// blobfs.ErrNameTaken. A row whose version moved on is
// query.ErrVersionMismatch. A row at the expected version that is deleting
// is blobfs.ErrDeleting. A refused name is a blobfs.NameError.
func (f *Files) Move(ctx context.Context, sess sqlate.Session, id, directoryID, name string, version int64) (blobfs.File, error) {
	name, err := validName(name)
	if err != nil {
		return blobfs.File{}, fmt.Errorf("data: move file %s: %w", id, err)
	}
	file, err := f.move.Run(ctx, sess, version, query.Args{"id": id, "directory_id": directoryID, "name": name})
	var refused *query.RefusedError[blobfs.File]
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return blobfs.File{}, fmt.Errorf("data: move file %s: %w", id, blobfs.ErrNotFound)
	case errors.As(err, &refused):
		// The row is at the expected version and the status predicate
		// refused it: the row is deleting.
		return blobfs.File{}, fmt.Errorf("data: move file %s: the row is %s: %w", id, refused.Row.Status, blobfs.ErrDeleting)
	case err != nil:
		return blobfs.File{}, fmt.Errorf("data: move file %s into %s as %q: %w", id, directoryID, name, classifyWrite(err))
	}
	return file, nil
}
