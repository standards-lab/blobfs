package data

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"slices"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"
)

//go:embed statements/*.sql
var statementFiles embed.FS

// Store is blobfs's persistence: the statements compiled once against the
// consumer's catalog and the operation handles over them. It holds no
// session; every operation takes one.
type Store struct {
	// Directories holds the operations over blobfs_directory rows.
	Directories *Directories

	// Files holds the operations over blobfs_file rows.
	Files *Files

	stmts   *query.Statements
	variant Variant
}

// New compiles blobfs's statements against catalog for dialect and binds
// them, with no I/O. The catalog must carry the blobfs namespace from
// Patterns() and the query library's from query.Patterns(); a catalog
// without the blobfs namespace is refused before compiling, and any other
// compile failure is returned as the loader reports it. The dialect
// chooses the form of each returning command. With WithEngine, New passes
// the baseline it bound to the Engine and runs the variant it returns; an
// Engine's error is returned as "data: new store: engine: ...".
func New(catalog *query.Catalog, dialect sqlate.Dialect, opts ...Option) (_ *Store, err error) {
	defer wrap(&err, "new store")
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if !slices.Contains(catalog.Namespaces(), Namespace) {
		return nil, fmt.Errorf("the catalog has no %q namespace; register data.Patterns() in it", Namespace)
	}
	stmts, err := catalog.Compile(statementFiles, "statements", dialect)
	if err != nil {
		return nil, err
	}
	dirs := newDirectoryReads(stmts)
	base := newStandard(stmts, dirs)
	var variant Variant = base
	if o.engine != nil {
		v, err := o.engine(catalog, dialect, base)
		if err != nil {
			return nil, fmt.Errorf("engine: %w", err)
		}
		if v == nil {
			return nil, errors.New("engine: returned no variant")
		}
		variant = v
	}
	return &Store{
		Directories: newDirectories(stmts, dirs, variant),
		Files:       newFiles(stmts, dirs, variant),
		stmts:       stmts,
		variant:     variant,
	}, nil
}

// Statements returns the compiled inventory in name order, followed by the
// variant's own statements.
func (s *Store) Statements() []query.Statement {
	return append(s.stmts.Statements(), s.variant.Statements()...)
}

// Verify prepares every statement, the variant's included, against the
// schema the session reaches, and probes both listings' field contracts
// and a page past a cursor, so a program that calls it at startup fails
// there and not at first use.
func (s *Store) Verify(ctx context.Context, sess sqlate.Session) error {
	return query.Verify(ctx, sess, s.stmts, s.Directories.list.projection, s.Files.list.projection, s.variant)
}
