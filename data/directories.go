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

// Directories is the handle over blobfs_directory rows, the Store's
// Directories field. Every operation takes a directory by id.
type Directories struct {
	variant   Variant
	dirs      directoryReads
	list      listing[blobfs.Directory]
	ancestors query.Rows[ancestor]
	isWithin  query.Rows[int64]
	create    query.Returning[blobfs.Directory]
	move      query.Returning[blobfs.Directory]
	remove    query.Statement
	markDirs  query.Statement
	markFiles query.Statement
	deleting  query.Rows[blobfs.Directory]
}

// ancestor is one row of directory_ancestors.
type ancestor struct {
	ID       string  `json:"id"`
	ParentID *string `json:"parent_id"`
	Name     string  `json:"name"`
}

// newDirectories binds the directory statements of a compiled set.
func newDirectories(stmts *query.Statements, dirs directoryReads, variant Variant) *Directories {
	directory := query.Scanner[blobfs.Directory]()
	return &Directories{
		variant: variant,
		dirs:    dirs,
		list: listing[blobfs.Directory]{
			projection: stmts.Statement("directory_children").Project(directory),
			anchor:     "parent_id",
			deleting:   string(blobfs.DirectoryStatusDeleting),
			dirs:       dirs,
		},
		ancestors: stmts.Statement("directory_ancestors").Scan(query.Scanner[ancestor]()),
		isWithin:  stmts.Statement("directory_is_within").Scan(query.Scalar[int64]),
		create:    stmts.Statement("create_directory").Returning(directory),
		move:      stmts.Statement("move_directory").Returning(directory),
		remove:    stmts.Statement("delete_directory"),
		markDirs:  stmts.Statement("mark_directory_deleting"),
		markFiles: stmts.Statement("mark_directory_files_deleting"),
		deleting:  stmts.Statement("deleting_branches").Scan(directory),
	}
}

// Find returns the directory with id, or blobfs.ErrNotFound. The root is
// Find of blobfs.RootID.
func (d *Directories) Find(ctx context.Context, sess sqlate.Session, id string) (_ blobfs.Directory, err error) {
	defer wrap(&err, "find directory %s", id)
	dir, err := d.dirs.byID.One(ctx, sess, query.Args{"id": id})
	if err != nil {
		return blobfs.Directory{}, notFound(err)
	}
	return dir, nil
}

// FindByName returns the child directory of parentID named name, which is
// normalized first; a file of the same name is not found. Refusals:
// blobfs.NameError before any SQL, blobfs.ErrNotFound.
func (d *Directories) FindByName(ctx context.Context, sess sqlate.Session, parentID, name string) (_ blobfs.Directory, err error) {
	defer wrap(&err, "find directory %q under %s", name, parentID)
	if name, err = validName(name); err != nil {
		return blobfs.Directory{}, err
	}
	dir, err := d.findByName(ctx, sess, parentID, name)
	if err != nil {
		return blobfs.Directory{}, notFound(err)
	}
	return dir, nil
}

// findByName reads the child of parentID named name, already normalized;
// sql.ErrNoRows is returned unmapped.
func (d *Directories) findByName(ctx context.Context, sess sqlate.Session, parentID, name string) (blobfs.Directory, error) {
	return d.dirs.byName.One(ctx, sess, query.Args{"parent_id": parentID, "name": name})
}

// Create creates a directory named name under parentID and returns the
// row as the database holds it. The name is normalized and validated and
// the id minted or taken from WithID, before any SQL.
//
// Refusals: blobfs.NameError; blobfs.IDError; blobfs.ErrNameTaken for a
// name a directory under the parent holds; blobfs.ErrIDTaken;
// blobfs.ErrNotFound for a missing parent; blobfs.ErrDeleting for a
// deleting parent.
func (d *Directories) Create(ctx context.Context, sess sqlate.Session, parentID, name string, opts ...CreateOption) (_ blobfs.Directory, err error) {
	defer wrap(&err, "create directory %q under %s", name, parentID)
	if name, err = validName(name); err != nil {
		return blobfs.Directory{}, err
	}
	id, err := rowID(opts)
	if err != nil {
		return blobfs.Directory{}, err
	}
	return d.insert(ctx, sess, id, parentID, name)
}

// Ensure returns the directory named name under parentID, creating it when
// none exists, and reports whether this call created it: the insert-or-find
// a seeder runs. It looks the name up first and inserts only when no row
// holds it; a found row keeps its own id whatever WithID supplied, and an
// active one is returned without a read of its parent, a straggler under
// a deleting parent included.
//
// Refusals: Create's; blobfs.ErrDeleting for a found directory that is
// deleting; and, inside a transaction only, blobfs.ErrNameTaken when a
// creator commits the name between the lookup and the insert, since the
// failed insert may have aborted the transaction. See Directories in
// docs/features.md.
func (d *Directories) Ensure(ctx context.Context, sess sqlate.Session, parentID, name string, opts ...CreateOption) (_ blobfs.Directory, _ bool, err error) {
	defer wrap(&err, "ensure directory %q under %s", name, parentID)
	if name, err = validName(name); err != nil {
		return blobfs.Directory{}, false, err
	}
	id, err := rowID(opts)
	if err != nil {
		return blobfs.Directory{}, false, err
	}
	dir, created, err := insertOrFind(ctx, sess,
		func(ctx context.Context, sess sqlate.Session) (blobfs.Directory, error) {
			return d.findByName(ctx, sess, parentID, name)
		},
		func(ctx context.Context, sess sqlate.Session) (blobfs.Directory, error) {
			return d.insert(ctx, sess, id, parentID, name)
		})
	if err == nil {
		err = closed(dir)
	}
	if err != nil {
		return blobfs.Directory{}, false, err
	}
	return dir, created, nil
}

// insert runs create_directory and classifies its refusal bare: a
// violation through the write mapping, and an insert that selected no row
// by a read of the parent.
func (d *Directories) insert(ctx context.Context, sess sqlate.Session, id, parentID, name string) (blobfs.Directory, error) {
	dir, changed, err := d.create.One(ctx, sess, query.Args{"id": id, "parent_id": parentID, "name": name})
	switch {
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return blobfs.Directory{}, classifyWrite(err)
	case err != nil || !changed:
		return blobfs.Directory{}, d.dirs.refusedUnder(ctx, sess, parentID)
	}
	return dir, nil
}

// Delete removes the empty directory with id, only at the version
// AtVersion names when it is given. There is no cascade: a branch is
// deleted with MarkDeleting and Store.Sweep.
//
// Refusals: blobfs.ErrRootDirectory before any SQL; blobfs.ErrNotEmpty for
// a directory with child directories or files; blobfs.ErrReferenced for a
// consumer's foreign key; blobfs.ErrNotFound; query.ErrVersionMismatch
// under AtVersion.
func (d *Directories) Delete(ctx context.Context, sess sqlate.Session, id string, opts ...VersionOption) (err error) {
	defer wrap(&err, "delete directory %s", id)
	return d.deleteDirectory(ctx, sess, id, atVersion(opts))
}

// deleteDirectory is Delete's body, at version when it is not nil, with
// its errors bare for the sweep.
func (d *Directories) deleteDirectory(ctx context.Context, sess sqlate.Session, id string, version *int64) error {
	if id == blobfs.RootID {
		return blobfs.ErrRootDirectory
	}
	n, err := d.remove.Exec(ctx, sess, withVersion(query.Args{"id": id}, version))
	switch {
	case err != nil:
		return classifyDelete(err)
	case n > 0:
		return nil
	case version == nil:
		return blobfs.ErrNotFound
	}
	dir, err := d.dirs.byID.One(ctx, sess, query.Args{"id": id})
	if err != nil {
		return notFound(err)
	}
	return versionMismatch(*version, dir.Version)
}

// Marked counts the rows Directories.MarkDeleting moved to deleting,
// directories and files; a row deleting already is not counted.
type Marked struct {
	Directories int64
	Files       int64
}

// MarkDeleting is the first step of a branch's delete: it marks the
// directory with id, every directory beneath it, and every file in them
// deleting, advancing each changed row's version once, and reports what it
// changed. It runs two statements in tx under the tree lock and waits on a
// Files.Hold as Files.Delete does. The branch the two statements walk is
// one branch only where the lock serializes; on a variant whose Serializes
// is false, a mover's course from Moves in docs/concepts.md covers the
// mark too. A repeated mark converges and reaches a straggler. See
// Deleting a branch in docs/concepts.md.
//
// Refusals: blobfs.ErrRootDirectory before any SQL; blobfs.ErrNotFound;
// query.ErrVersionMismatch under AtVersion for an active directory.
func (d *Directories) MarkDeleting(ctx context.Context, tx *sqlate.Tx, id string, opts ...VersionOption) (_ Marked, err error) {
	defer wrap(&err, "mark directory %s deleting", id)
	return d.markDeleting(ctx, tx, id, atVersion(opts))
}

// markDeleting is MarkDeleting's body, at version when it is not nil, with
// its errors bare for the sweep. The version guards the directories'
// update in the same statement, and the files' update runs only once that
// statement marked the directory or found it deleting.
func (d *Directories) markDeleting(ctx context.Context, tx *sqlate.Tx, id string, version *int64) (Marked, error) {
	if id == blobfs.RootID {
		return Marked{}, blobfs.ErrRootDirectory
	}
	if err := d.variant.LockTree(ctx, tx); err != nil {
		return Marked{}, fmt.Errorf("lock tree: %w", err)
	}
	args := query.Args{"id": id}
	dirs, err := d.markDirs.Exec(ctx, tx, withVersion(args, version))
	if err != nil {
		return Marked{}, err
	}
	if dirs == 0 {
		// Nothing was marked: the directory is missing, active at another
		// version, or deleting already, the mark's retry, which the read
		// tells apart.
		dir, err := d.dirs.byID.One(ctx, tx, args)
		switch {
		case err != nil:
			return Marked{}, notFound(err)
		case dir.Status.Mutable() && version != nil:
			return Marked{}, versionMismatch(*version, dir.Version)
		case dir.Status.Mutable():
			return Marked{}, fmt.Errorf("the mark changed no row, yet the directory %s is %s", id, dir.Status)
		}
	}
	files, err := d.markFiles.Exec(ctx, tx, args)
	if err != nil {
		return Marked{}, err
	}
	return Marked{Directories: dirs, Files: files}, nil
}

// Deleting returns at most limit roots of the branches being deleted, in
// id order: each deleting directory under an active parent. A limit below
// 1 is refused before any SQL.
func (d *Directories) Deleting(ctx context.Context, sess sqlate.Session, limit int) (_ []blobfs.Directory, err error) {
	defer wrap(&err, "deleting directories")
	if limit < 1 {
		return nil, fmt.Errorf("the limit %d is below 1", limit)
	}
	dirs, err := d.deleting.All(ctx, sess, query.Args{"offset": 0, "fetch": limit})
	if err != nil {
		return nil, err
	}
	return dirs, nil
}
