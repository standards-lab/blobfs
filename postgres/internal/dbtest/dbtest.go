//go:build integration

package dbtest

import (
	"context"
	"errors"
	"testing"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/migrate"

	"github.com/standards-lab/blobfs/postgres"
)

// Env names the environment variable that holds the admin DSN of the
// server the integration tier creates its databases on, and Prefix starts
// their names, blobfs_test_<hex>. A test passes both to pgtest.Create or
// pgtest.Migrated, which create the database and drop it when the test
// ends.
const (
	Env    = "BLOBFS_DSN"
	Prefix = "blobfs"
)

// Migrations returns blobfs's migration set, for pgtest.Migrated or a
// migrator a test builds itself, failing the test when it does not load.
func Migrations(t testing.TB) migrate.Set {
	t.Helper()
	set, err := postgres.Migrations()
	if err != nil {
		t.Fatalf("Migrations: %v", err)
	}
	return set
}

// Constraint returns the constraint name a classified violation carries,
// or fails the test when err is not a sqlate.ConstraintError of class.
func Constraint(t testing.TB, err error, class error) string {
	t.Helper()
	var ce *sqlate.ConstraintError
	if !errors.As(err, &ce) {
		t.Fatalf("error %v is not a sqlate.ConstraintError", err)
	}
	if !errors.Is(ce.Class, class) {
		t.Fatalf("error %v has class %v, want %v", err, ce.Class, class)
	}
	return ce.Constraint
}

// Exists reports whether the relation named, a table or an index, exists
// in the connected database.
func Exists(ctx context.Context, t testing.TB, sess sqlate.Session, relation string) bool {
	t.Helper()
	var exists bool
	scan(ctx, t, sess, &exists, "SELECT to_regclass($1) IS NOT NULL", relation)
	return exists
}

// Int runs a one-row, one-column query and returns the value.
func Int(ctx context.Context, t testing.TB, sess sqlate.Session, query string, args ...any) int {
	t.Helper()
	var n int
	scan(ctx, t, sess, &n, query, args...)
	return n
}

func scan(ctx context.Context, t testing.TB, sess sqlate.Session, dest any, query string, args ...any) {
	t.Helper()
	rows, err := sess.QueryContext(ctx, query, args...)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		t.Fatalf("%s: no row: %v", query, rows.Err())
	}
	if err := rows.Scan(dest); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}
