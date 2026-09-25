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

// Store is blobfs's persistence: the embedded statements compiled once
// against the consumer's catalog and bound to their typed handles, and the
// operation handles over them. It holds no session; every operation takes
// one.
type Store struct {
	// Directories holds the operations over blobfs_directory rows.
	Directories *Directories

	// Files holds the operations over blobfs_file rows.
	Files *Files

	stmts   *query.Statements
	variant Variant
}

// New compiles blobfs's statements against catalog for dialect and binds
// them. The catalog must carry the blobfs namespace, registered from
// Patterns(), and the query library's own namespace from query.Patterns():
// the statements include patterns of both. A catalog without the blobfs
// namespace is refused before compiling, naming it; any other compile
// failure is returned as the loader reports it. The dialect chooses the form
// of each returning command: the single-statement form where it renders
// RETURNING, and the fallback, the command and its read, otherwise. No I/O
// happens here.
//
// The options choose the variant the store forwards its variation points to
// (see Variant). New binds the baseline over the statements it compiled.
// Without WithEngine the store runs the baseline; with it, New passes the
// baseline to the Engine and runs the variant the Engine returns. The
// statements are compiled once either way. An Engine's error is returned
// wrapped as "data: engine: ...".
func New(catalog *query.Catalog, dialect sqlate.Dialect, opts ...Option) (*Store, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if !slices.Contains(catalog.Namespaces(), Namespace) {
		return nil, fmt.Errorf("data: the catalog has no %q namespace; register data.Patterns() in it", Namespace)
	}
	stmts, err := catalog.Compile(statementFiles, "statements", dialect)
	if err != nil {
		return nil, fmt.Errorf("data: %w", err)
	}
	dirs := newDirectoryReads(stmts)
	base := newStandard(stmts, dirs)
	var variant Variant = base
	if o.engine != nil {
		v, err := o.engine(catalog, dialect, base)
		if err != nil {
			return nil, fmt.Errorf("data: engine: %w", err)
		}
		if v == nil {
			return nil, errors.New("data: engine: returned no variant")
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

// Statements returns the compiled inventory in name order, for a consumer
// that lists or registers the SQL its program runs: the data package's own
// statements, followed by the variant's own (see Variant).
func (s *Store) Statements() []query.Statement {
	return append(s.stmts.Statements(), s.variant.Statements()...)
}

// Verify prepares every statement against the schema the session reaches, so
// a statement the migrated schema does not satisfy fails at startup and not
// at first use. The two listings are verified as projections too: each
// declared field is compared with its declared type over the base, and a
// page past a cursor is prepared, so a field contract the schema does not
// satisfy and the keyset predicate fail here as well. The variant verifies
// its own statements in the same pass (see Variant).
func (s *Store) Verify(ctx context.Context, sess sqlate.Session) error {
	return query.Verify(ctx, sess, s.stmts, s.Directories.list.projection, s.Files.list.projection, s.variant)
}
