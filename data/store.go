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
// (see Variant). New binds Standard over the statements it compiled. Without
// WithEngine the store runs Standard; with it, New passes Standard to the
// Engine as the baseline and runs the variant the Engine returns. The
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
	base := newStandard(stmts)
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
		Directories: newDirectories(stmts, variant),
		Files:       newFiles(stmts, variant),
		stmts:       stmts,
		variant:     variant,
	}, nil
}

// Statements returns the compiled inventory in name order, for a consumer
// that lists or registers the SQL its program runs: the data package's own
// statements, followed by the variant's own when it compiled any.
func (s *Store) Statements() []query.Statement {
	out := s.stmts.Statements()
	if inv, ok := s.variant.(inventory); ok {
		out = append(out, inv.Statements()...)
	}
	return out
}

// Verify prepares every statement against the schema the session reaches, so
// a statement the migrated schema does not satisfy fails at startup and not
// at first use. The two listings are verified as projections too: each
// declared field is compared with its declared type over the base, and a
// page past a cursor is prepared, so a field contract the schema does not
// satisfy and the keyset predicate fail here as well. A variant that can
// verify itself, as an Engine's variant that compiled statements of its own
// does, is verified in the same pass (see Engine).
func (s *Store) Verify(ctx context.Context, sess sqlate.Session) error {
	vs := []query.Verifier{s.stmts, s.Directories.list, s.Files.list}
	if v, ok := s.variant.(query.Verifier); ok {
		vs = append(vs, v)
	}
	return query.Verify(ctx, sess, vs...)
}

// inventory is the optional capability of a variant that compiled statements
// of its own (see Engine). Standard has none: its statements are the data
// package's own, which the Store already lists and verifies.
type inventory interface {
	Statements() []query.Statement
}
