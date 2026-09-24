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

// TreeLockKey is the bigint key of the tree lock's advisory lock: the
// 64-bit FNV-1a hash of TreeLockName read as a signed integer, fixed for
// every install, since one install per database means one tree per
// database. A consumer that takes advisory locks of its own avoids it;
// this package's tests recompute it from the name.
const TreeLockKey int64 = -8521165719926625175

// Variant is the PostgreSQL implementation of data.Variant, the variant
// Engine builds. It embeds the store's standard baseline, as the
// data.Variant contract requires, so a variation point this package does
// not override runs as the baseline does, and it binds the compiled native
// statements to their handles. It overrides all three: the tree lock, which
// the baseline cannot take; path resolution in one statement; and the file
// hold, taken without writing a row version. It holds no session; every
// method takes one.
type Variant struct {
	*data.Standard
	stmts       *query.Statements
	lockTree    query.Statement
	resolvePath query.Rows[resolved]
	lockFile    query.Rows[string]
	lockFileAt  query.Rows[string]
}

// resolved is one row of resolve_path: the deepest directory reached and
// its depth, the number of segments matched. The embedded directory's
// columns flatten into the row, as the struct scanner maps them.
type resolved struct {
	blobfs.Directory
	Depth int `json:"depth"`
}

var (
	_ data.Engine    = Engine
	_ data.Variant   = (*Variant)(nil)
	_ query.Verifier = (*Variant)(nil)
)

// Engine is the PostgreSQL engine for data.WithEngine: it compiles the
// variant's own four statements against catalog for dialect, binds them, and
// returns a *Variant over base, the baseline data.New compiled, so the data
// package's statements are compiled once. A consumer selects it at its
// composition root with data.New(catalog, dialect, data.WithEngine(Engine)).
// The catalog must carry the blobfs namespace, registered from
// data.Patterns(), because resolve_path returns the published directory
// columns. The variant's statements are not returning commands, so either
// form of the store's returning commands suits it. No I/O happens here.
func Engine(catalog *query.Catalog, dialect sqlate.Dialect, base *data.Standard) (data.Variant, error) {
	stmts, err := catalog.Compile(statementFiles, "statements", dialect)
	if err != nil {
		return nil, fmt.Errorf("blobfs/postgres: %w", err)
	}
	return &Variant{
		Standard:    base,
		stmts:       stmts,
		lockTree:    stmts.Statement("lock_tree"),
		resolvePath: stmts.Statement("resolve_path").Scan(query.Scanner[resolved]()),
		lockFile:    stmts.Statement("lock_file").Scan(query.Scalar[string]),
		lockFileAt:  stmts.Statement("lock_file_at_version").Scan(query.Scalar[string]),
	}, nil
}

// Statements returns the variant's compiled inventory in name order, for a
// consumer that lists the SQL its program runs. The store's Statements
// appends it to the data package's own.
func (v *Variant) Statements() []query.Statement {
	return v.stmts.Statements()
}

// Verify prepares the variant's statements against the schema the session
// reaches. The store's Verify runs it in the same pass as its own, so a
// startup Verify covers the variant's statements.
func (v *Variant) Verify(ctx context.Context, sess sqlate.Session) error {
	return v.stmts.Verify(ctx, sess)
}

// LockTree takes the transaction-scoped advisory lock under TreeLockKey in
// tx and returns once it is held. A transaction that holds it blocks every
// other LockTree until it commits or rolls back.
func (v *Variant) LockTree(ctx context.Context, tx *sqlate.Tx) error {
	_, err := v.lockTree.Exec(ctx, tx, query.Args{"key": TreeLockKey})
	return err
}

// Serializes reports true: LockTree holds an engine lock to the end of the
// transaction.
func (*Variant) Serializes() bool {
	return true
}

// ResolvePath walks segments below the directory with startID in one
// statement, the recursive query resolve_path, and returns the deepest
// directory reached and its depth, where the baseline reads the start and
// then one child per segment. The segments bind as one text[] parameter,
// encoded by the driver from the Go slice, so no name is ever spliced into
// the text; no segments bind as an empty array. No row means the start does
// not exist and is blobfs.ErrNotFound. See data.Variant.
func (v *Variant) ResolvePath(ctx context.Context, sess sqlate.Session, startID string, segments []string) (blobfs.Directory, int, error) {
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

// HoldFile takes the file row's lock with lock_file, or
// lock_file_at_version when version is not nil: SELECT ... FOR NO KEY
// UPDATE, the lock the baseline's self-assigning update takes and
// delete_file waits on, without writing a row version. A row returned is a
// row held; no row, whether missing, deleting, or at another version, is
// false with no lock taken, and the store reads the row to classify, as it
// does over the baseline. See data.Variant.
func (v *Variant) HoldFile(ctx context.Context, tx *sqlate.Tx, id string, version *int64) (bool, error) {
	args := query.Args{"id": id}
	lock := v.lockFile
	if version != nil {
		args["version"] = *version
		lock = v.lockFileAt
	}
	_, err := lock.One(ctx, tx, args)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
