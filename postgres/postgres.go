package postgres

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data"
)

//go:embed statements/*.sql
var statementFiles embed.FS

// TreeLockName is the name the tree lock's key is derived from: the
// blobfs_ namespace and the table whose shape the lock serializes.
const TreeLockName = "blobfs_directory.tree"

// TreeLockKey is the tree lock's advisory lock key: the 64-bit FNV-1a hash
// of TreeLockName read as a signed integer. A consumer that takes advisory
// locks of its own avoids it.
const TreeLockKey int64 = -8521165719926625175

// variant is the PostgreSQL data.Variant Engine builds: it embeds the base
// it is given and overrides the three variation points and the inventory.
type variant struct {
	data.Variant
	stmts       *query.Statements
	lockTree    query.Statement
	resolvePath query.Rows[resolved]
	lockFile    query.Rows[string]
}

// resolved is one row of resolve_path: the directory reached and its
// depth.
type resolved struct {
	blobfs.Directory
	Depth int `json:"depth"`
}

var (
	_ data.Engine  = Engine
	_ data.Variant = (*variant)(nil)
)

// Engine is the PostgreSQL engine for data.WithEngine: it compiles the
// variant's three statements against catalog for dialect, with no I/O,
// and returns the PostgreSQL variant over base. The catalog must carry the
// blobfs namespace, since resolve_path includes the published directory
// columns.
func Engine(catalog *query.Catalog, dialect sqlate.Dialect, base data.Variant) (data.Variant, error) {
	stmts, err := catalog.Compile(statementFiles, "statements", dialect)
	if err != nil {
		return nil, fmt.Errorf("blobfs/postgres: %w", err)
	}
	return &variant{
		Variant:     base,
		stmts:       stmts,
		lockTree:    stmts.Statement("lock_tree"),
		resolvePath: stmts.Statement("resolve_path").Scan(query.Scanner[resolved]()),
		lockFile:    stmts.Statement("lock_file").Scan(query.Scalar[string]),
	}, nil
}

// Statements returns the variant's compiled statements, which the store's
// Statements appends to its own.
func (v *variant) Statements() []query.Statement {
	return v.stmts.Statements()
}

// Verify prepares the variant's statements against the schema sess
// reaches, in the store's Verify.
func (v *variant) Verify(ctx context.Context, sess sqlate.Session) error {
	return v.stmts.Verify(ctx, sess)
}

// LockTree takes the transaction-scoped advisory lock under TreeLockKey in
// tx and returns once it is held. A transaction that holds it blocks every
// other LockTree until it commits or rolls back.
func (v *variant) LockTree(ctx context.Context, tx *sqlate.Tx) error {
	_, err := v.lockTree.Exec(ctx, tx, query.Args{"key": TreeLockKey})
	return err
}

// Serializes reports true: LockTree holds an engine lock to the end of the
// transaction.
func (*variant) Serializes() bool {
	return true
}

// ResolvePath walks segments below startID in one statement, resolve_path,
// with the segments bound as one text[] parameter; no segments bind as an
// empty array. See data.Variant.
func (v *variant) ResolvePath(ctx context.Context, sess sqlate.Session, startID string, segments []string) (blobfs.Directory, int, error) {
	if segments == nil {
		segments = []string{}
	}
	r, err := v.resolvePath.One(ctx, sess, query.Args{"start_id": startID, "segments": segments})
	if errors.Is(err, sql.ErrNoRows) {
		return blobfs.Directory{}, 0, blobfs.ErrNotFound
	}
	if err != nil {
		return blobfs.Directory{}, 0, err
	}
	return r.Directory, r.Depth, nil
}

// HoldFile takes the file row's lock with lock_file, writing no row
// version; a nil version binds NULL, no guard. A row returned is a row
// held. See data.Variant.
func (v *variant) HoldFile(ctx context.Context, tx *sqlate.Tx, id string, version *int64) (bool, error) {
	args := query.Args{"id": id, "version": nil}
	if version != nil {
		args["version"] = *version
	}
	_, err := v.lockFile.One(ctx, tx, args)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
