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

// Directories is the handle over blobfs_directory rows: the Store's
// Directories field. Every operation takes the context and the session
// first and a directory by id; a step that must run inside a transaction
// takes a *sqlate.Tx instead of a session.
type Directories struct {
	variant   Variant
	byID      query.Rows[blobfs.Directory]
	byName    query.Rows[blobfs.Directory]
	list      query.Projection[blobfs.Directory]
	ancestors query.Rows[ancestor]
	isWithin  query.Rows[int64]
	create    query.Returning[blobfs.Directory]
	move      query.Returning[blobfs.Directory]
	remove    query.Statement
	removeAt  query.Statement
	markDirs  query.Statement
	markFiles query.Statement
	deleting  query.Rows[blobfs.Directory]
}

// ancestor is one row of directory_ancestors: a directory's id, parent,
// and name on the chain up to the root, whose parent is nil and whose
// name is /.
type ancestor struct {
	ID       string  `json:"id"`
	ParentID *string `json:"parent_id"`
	Name     string  `json:"name"`
}

// newDirectories binds the directory statements of a compiled set.
func newDirectories(stmts *query.Statements, variant Variant) *Directories {
	directory := query.Scanner[blobfs.Directory]()
	return &Directories{
		variant:   variant,
		byID:      stmts.Statement("directory_by_id").Scan(directory),
		byName:    stmts.Statement("directory_by_name").Scan(directory),
		list:      stmts.Statement("directory_children").Project(directory),
		ancestors: stmts.Statement("directory_ancestors").Scan(query.Scanner[ancestor]()),
		isWithin:  stmts.Statement("directory_is_within").Scan(query.Scalar[int64]),
		create:    stmts.Statement("create_directory").Returning(directory),
		move:      stmts.Statement("move_directory").Returning(directory),
		remove:    stmts.Statement("delete_directory"),
		removeAt:  stmts.Statement("delete_directory_at_version"),
		markDirs:  stmts.Statement("mark_directory_deleting"),
		markFiles: stmts.Statement("mark_directory_files_deleting"),
		deleting:  stmts.Statement("deleting_branches").Scan(directory),
	}
}

// Find returns the directory with id, or blobfs.ErrNotFound. The root is
// Find of blobfs.RootID.
func (d *Directories) Find(ctx context.Context, sess sqlate.Session, id string) (blobfs.Directory, error) {
	dir, err := d.byID.One(ctx, sess, query.Args{"id": id})
	if err != nil {
		return blobfs.Directory{}, fmt.Errorf("data: find directory %s: %w", id, notFound(err))
	}
	return dir, nil
}

// FindByName returns the child of the directory with parentID named name,
// or blobfs.ErrNotFound. The name is normalized before it is compared, and
// a name ValidateName refuses is a blobfs.NameError before any SQL, since
// no row can hold it. Directories and files have separate name spaces: a
// file of the same name is not found here.
func (d *Directories) FindByName(ctx context.Context, sess sqlate.Session, parentID, name string) (blobfs.Directory, error) {
	name, err := validName(name)
	if err != nil {
		return blobfs.Directory{}, fmt.Errorf("data: find directory by name: %w", err)
	}
	dir, err := d.findByName(ctx, sess, parentID, name)
	if err != nil {
		return blobfs.Directory{}, fmt.Errorf("data: find directory %q under %s: %w", name, parentID, notFound(err))
	}
	return dir, nil
}

// findByName reads the child of parentID named name, already normalized,
// returning sql.ErrNoRows unmapped when there is none.
func (d *Directories) findByName(ctx context.Context, sess sqlate.Session, parentID, name string) (blobfs.Directory, error) {
	return d.byName.One(ctx, sess, query.Args{"parent_id": parentID, "name": name})
}

// Create creates a directory named name under the directory with parentID
// and returns the row as the database holds it. The name is normalized and
// validated first; a refusal is a blobfs.NameError, and an empty name is
// one, so no call creates a row without a name. Every row Create writes
// has a parent, so no call creates a root either: the one root is seeded
// by the schema. The id is minted, or taken from WithID and checked,
// before any SQL. A name already held by a directory under the same parent
// is blobfs.ErrNameTaken, an id another directory carries is
// blobfs.ErrIDTaken, a parent that does not exist is blobfs.ErrNotFound,
// and a parent that is deleting is blobfs.ErrDeleting. The insert selects
// the row from its parent only while the parent is active, and when it
// inserts nothing the parent is read to tell the two refusals apart.
//
// The insert is a returning command: it runs in the single-statement form
// where the dialect renders RETURNING, and otherwise in the fallback, the
// insert and a read of the row in one transaction, the caller's when sess is
// a *sqlate.Tx and one of its own when sess is the pool.
func (d *Directories) Create(ctx context.Context, sess sqlate.Session, parentID, name string, opts ...CreateOption) (blobfs.Directory, error) {
	name, err := validName(name)
	if err != nil {
		return blobfs.Directory{}, fmt.Errorf("data: create directory: %w", err)
	}
	id, err := rowID(opts)
	if err != nil {
		return blobfs.Directory{}, fmt.Errorf("data: create directory %q: %w", name, err)
	}
	dir, err := d.insert(ctx, sess, id, parentID, name)
	if err != nil {
		return blobfs.Directory{}, fmt.Errorf("data: create directory %q under %s: %w", name, parentID, err)
	}
	return dir, nil
}

// Ensure returns the directory named name under the directory with parentID,
// creating it when none exists, and reports whether this call created it. It
// is the insert-or-find a seeder needs: a seeded directory is read on every
// run after the first, without the seeder catching blobfs.ErrNameTaken and
// looking the name up itself. The name is normalized and validated and the
// id resolved as in Create, before any SQL; a found row keeps its own id
// whatever WithID supplied.
//
// The lookup runs first and the insert only when it found no row, so the
// common case runs no failing statement and composes into a caller's
// transaction, where a seeder writes its own rows beside the directory. A
// creator that commits between the lookup and the insert makes the insert
// fail as blobfs.ErrNameTaken. On the pool the row is then looked up again
// and returned as found. Inside a transaction the error is returned instead,
// because on PostgreSQL the failed insert has aborted the transaction, and
// the caller retries the transaction. A found row that is deleting is
// blobfs.ErrDeleting, since its branch is being removed and it takes no
// child. The other refusals are Create's.
func (d *Directories) Ensure(ctx context.Context, sess sqlate.Session, parentID, name string, opts ...CreateOption) (blobfs.Directory, bool, error) {
	name, err := validName(name)
	if err != nil {
		return blobfs.Directory{}, false, fmt.Errorf("data: ensure directory: %w", err)
	}
	id, err := rowID(opts)
	if err != nil {
		return blobfs.Directory{}, false, fmt.Errorf("data: ensure directory %q: %w", name, err)
	}
	dir, created, err := insertOrFind(ctx, sess,
		func(ctx context.Context, sess sqlate.Session) (blobfs.Directory, error) {
			return d.findByName(ctx, sess, parentID, name)
		},
		func(ctx context.Context, sess sqlate.Session) (blobfs.Directory, error) {
			return d.insert(ctx, sess, id, parentID, name)
		})
	switch {
	case err != nil:
		return blobfs.Directory{}, false, fmt.Errorf("data: ensure directory %q under %s: %w", name, parentID, err)
	case !dir.Status.Mutable():
		return blobfs.Directory{}, false, fmt.Errorf("data: ensure directory %q under %s: the directory %s is %s: %w", name, parentID, dir.ID, dir.Status, blobfs.ErrDeleting)
	}
	return dir, created, nil
}

// insert runs create_directory under id and returns the row as the
// database holds it. The name is normalized and validated already. A
// constraint violation is classified through the write mapping, and an
// insert that selected no row from its parent by reading the parent, and
// either is returned without context, so each caller adds its own. The
// read of an insert that selected no row finds nothing, or finds a row
// another insert left under a caller-supplied id; either way nothing was
// inserted, and the parent says why.
func (d *Directories) insert(ctx context.Context, sess sqlate.Session, id, parentID, name string) (blobfs.Directory, error) {
	dir, changed, err := d.create.One(ctx, sess, query.Args{"id": id, "parent_id": parentID, "name": name})
	switch {
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return blobfs.Directory{}, classifyWrite(err)
	case err != nil || !changed:
		return blobfs.Directory{}, refusedUnder(ctx, sess, d.byID, parentID)
	}
	return dir, nil
}

// Delete removes the directory with id. It takes no version: the one case a
// stale version would catch, a directory that gained children since the
// caller read it, the foreign keys already refuse. The root is refused with
// blobfs.ErrRootDirectory before any SQL, and the statement itself never
// removes a row without a parent. A directory that still has child
// directories or files is blobfs.ErrNotEmpty, reported by the foreign keys
// blobfs_fk_directory_parent and blobfs_fk_file_directory, since there is no
// cascade; a consumer removes the contents first, deepest first. A
// consumer's own foreign key into blobfs_directory refuses the removal as
// blobfs.ErrReferenced, with the sqlate.ConstraintError reachable. A
// directory that does not exist is blobfs.ErrNotFound. Delete runs one
// statement, so the session may be the pool or a transaction; a consumer
// that keeps a row of its own about the directory removes both in one
// transaction. With AtVersion, the directory is removed only at that
// version, in the same statement; a directory at another version is
// query.ErrVersionMismatch, told apart from a missing one by a read.
func (d *Directories) Delete(ctx context.Context, sess sqlate.Session, id string, opts ...DeleteOption) error {
	if id == blobfs.RootID {
		return fmt.Errorf("data: delete directory %s: %w", id, blobfs.ErrRootDirectory)
	}
	var o holdOptions
	for _, opt := range opts {
		opt(&o)
	}
	stmt, args := d.remove, query.Args{"id": id}
	if o.hasVersion {
		stmt, args = d.removeAt, args.With("version", o.version)
	}
	n, err := stmt.Exec(ctx, sess, args)
	if err != nil {
		return fmt.Errorf("data: delete directory %s: %w", id, classifyDelete(err))
	}
	if n > 0 {
		return nil
	}
	if !o.hasVersion {
		return fmt.Errorf("data: delete directory %s: %w", id, blobfs.ErrNotFound)
	}
	dir, err := d.byID.One(ctx, sess, query.Args{"id": id})
	if err != nil {
		return fmt.Errorf("data: delete directory %s: %w", id, notFound(err))
	}
	return fmt.Errorf("data: delete directory %s: %w", id, versionMismatch(o.version, dir.Version))
}

// Marked is what Directories.MarkDeleting moved to deleting: the number of
// directories, the one named and those beneath it, and the number of files
// in them. A row that was deleting already is not counted, so a repeated
// mark reports only what it reached anew, and none when nothing was.
type Marked struct {
	Directories int64
	Files       int64
}

// MarkDeleting is the first step of a branch's delete: it marks the
// directory with id, every directory beneath it, and every file in them
// blobfs.DirectoryStatusDeleting and blobfs.StatusDeleting, advancing the
// version of each row it changes, and reports how many of each it changed.
// From then on the branch is closed: a create, an ensure, or a move under a
// deleting directory is blobfs.ErrDeleting, and so is a move of a directory
// or file out of one. A mark is never undone; the branch's rows are removed
// by the file delete's later steps and by Delete, deepest first.
//
// It runs in tx under the tree lock, LockTree, so the branch the two
// statements walk is not reshaped by a move between them: one recursive
// update marks the directories and a second one, over the same walk, marks
// the files. The lock does not stop a create that read the parent as
// active before the mark committed; such a straggler lands in the branch
// active, and a repeated mark, which walks through rows already deleting,
// reaches it. The file update takes each file's row lock, so it waits on a
// Files.Hold another transaction took, as Files.Delete does.
//
// The root is blobfs.ErrRootDirectory, refused before any SQL, and neither
// statement ever marks a row without a parent. A directory that does not
// exist is blobfs.ErrNotFound. A branch marked already is no error: the
// mark changes nothing it marked before and reports what it changed.
//
// With AtVersion, the directory is marked only at that version, read under
// the tree lock, which every change to a directory's version takes; a
// directory at another version is query.ErrVersionMismatch, and nothing is
// marked. A directory that is deleting already is the mark's retry, which
// converges whatever the version, as a file's is in Files.Delete.
func (d *Directories) MarkDeleting(ctx context.Context, tx *sqlate.Tx, id string, opts ...VersionOption) (Marked, error) {
	if id == blobfs.RootID {
		return Marked{}, fmt.Errorf("data: mark directory %s deleting: %w", id, blobfs.ErrRootDirectory)
	}
	var o holdOptions
	for _, opt := range opts {
		opt(&o)
	}
	if err := d.LockTree(ctx, tx); err != nil {
		return Marked{}, fmt.Errorf("data: mark directory %s deleting: %w", id, err)
	}
	args := query.Args{"id": id}
	if o.hasVersion {
		dir, err := d.byID.One(ctx, tx, args)
		switch {
		case err != nil:
			return Marked{}, fmt.Errorf("data: mark directory %s deleting: %w", id, notFound(err))
		case dir.Status.Mutable() && dir.Version != o.version:
			return Marked{}, fmt.Errorf("data: mark directory %s deleting: %w", id, versionMismatch(o.version, dir.Version))
		}
	}
	dirs, err := d.markDirs.Exec(ctx, tx, args)
	if err != nil {
		return Marked{}, fmt.Errorf("data: mark directory %s deleting: %w", id, err)
	}
	if dirs == 0 {
		// Nothing was marked: the directory is missing, or its branch was
		// marked already, which the read tells apart.
		if _, err := d.byID.One(ctx, tx, args); err != nil {
			return Marked{}, fmt.Errorf("data: mark directory %s deleting: %w", id, notFound(err))
		}
	}
	files, err := d.markFiles.Exec(ctx, tx, args)
	if err != nil {
		return Marked{}, fmt.Errorf("data: mark directory %s deleting: %w", id, err)
	}
	return Marked{Directories: dirs, Files: files}, nil
}

// Deleting returns at most limit of the roots of the branches being
// deleted, in id order: each directory that is deleting under a parent
// that is active, the directory a MarkDeleting named, and none beneath it,
// since a mark reaches every directory in its branch. It is how a sweeper
// finds the branches whose delete stopped before their rows were removed;
// the rest of each branch is reached from its root, through the listings
// with IncludeDeleting. No branch being deleted returns no rows and no
// error. A limit below 1 is refused before any SQL. Its cost is the
// deleting rows where the engine indexes them, as the postgres migrations
// do, and the directory table where it does not.
func (d *Directories) Deleting(ctx context.Context, sess sqlate.Session, limit int) ([]blobfs.Directory, error) {
	if limit < 1 {
		return nil, fmt.Errorf("data: deleting directories: the limit %d is below 1", limit)
	}
	dirs, err := d.deleting.All(ctx, sess, query.Args{"offset": 0, "fetch": limit})
	if err != nil {
		return nil, fmt.Errorf("data: deleting directories: %w", err)
	}
	return dirs, nil
}
