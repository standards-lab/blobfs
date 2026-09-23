package data

import (
	"context"
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
	move      query.RowGuard[blobfs.Directory]
	remove    query.Statement
}

// ancestor is one row of directory_ancestors: a directory's parent and
// name on the chain up to the root, whose parent is nil and whose name
// is /.
type ancestor struct {
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
		move: stmts.Statement("move_directory").Returning(directory).
			Guarded("version", func(d blobfs.Directory) int64 { return d.Version }),
		remove: stmts.Statement("delete_directory"),
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
// blobfs.ErrIDTaken, and a parent that does not exist is
// blobfs.ErrNotFound.
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
// the caller retries the transaction. The other refusals are Create's.
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
	if err != nil {
		return blobfs.Directory{}, false, fmt.Errorf("data: ensure directory %q under %s: %w", name, parentID, err)
	}
	return dir, created, nil
}

// insert runs create_directory under id and returns the row as the
// database holds it. The name is normalized and validated already. A
// constraint violation is classified through the write mapping and
// returned without context, so each caller adds its own.
func (d *Directories) insert(ctx context.Context, sess sqlate.Session, id, parentID, name string) (blobfs.Directory, error) {
	dir, _, err := d.create.One(ctx, sess, query.Args{"id": id, "parent_id": parentID, "name": name})
	if err != nil {
		return blobfs.Directory{}, classifyWrite(err)
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
// transaction.
func (d *Directories) Delete(ctx context.Context, sess sqlate.Session, id string) error {
	if id == blobfs.RootID {
		return fmt.Errorf("data: delete directory %s: %w", id, blobfs.ErrRootDirectory)
	}
	n, err := d.remove.Exec(ctx, sess, query.Args{"id": id})
	if err != nil {
		return fmt.Errorf("data: delete directory %s: %w", id, classifyDelete(err))
	}
	if n == 0 {
		return fmt.Errorf("data: delete directory %s: %w", id, blobfs.ErrNotFound)
	}
	return nil
}
