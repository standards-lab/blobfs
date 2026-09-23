//go:build integration

// Package dbtest is the support the integration tier's tests share: it
// gives each test its own throwaway database on the server BLOBFS_DSN
// names, so no test depends on the state of the compose stack's database
// or on another test, and drops the database when the test ends. It
// applies blobfs's migration set through the query library's migrator,
// explains statements for the plan-shape assertions, and seeds the cost
// fixtures. A missing BLOBFS_DSN fails the test rather than skipping it:
// the integration tag states that the stack is expected.
package dbtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/migrate"
	sqlatepg "github.com/standards-lab/sqlate/postgres"

	"github.com/standards-lab/blobfs/postgres"
)

// Database is one test's throwaway database: the pool over it and its DSN.
type Database struct {
	Pool *sql.DB
	DSN  string
}

// Session wraps the pool with dialect. The integration tier's tests wrap
// one pool twice: under sqlate's postgres dialect for the migrator, which
// needs its lock and catalog, and under the dialect the run under test
// compiles its store for.
func (d Database) Session(dialect sqlate.Dialect) *sqlate.DB {
	return sqlate.Wrap(d.Pool, dialect)
}

// Create makes a uniquely named database on the server BLOBFS_DSN names
// and opens a pool to it. The pool is closed and the database dropped,
// with FORCE so open sessions do not block the drop, when the test ends.
func Create(t testing.TB) Database {
	t.Helper()
	dsn := os.Getenv("BLOBFS_DSN")
	if dsn == "" {
		t.Fatal("BLOBFS_DSN is not set; run under mise with the compose stack up")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open %s: %v", dsn, err)
	}
	name := "blobfs_test_" + suffix(t)
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		_ = admin.Close()
		t.Fatalf("create database %s: %v", name, err)
	}
	t.Cleanup(func() {
		// WITH (FORCE) terminates the database's other sessions, and under
		// load the engine can report that one is still closing. Each attempt
		// gets its own deadline, so one long stall does not use up the
		// retries.
		var err error
		for range 4 {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_, err = admin.ExecContext(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
			cancel()
			if err == nil {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
		_ = admin.Close()
	})

	testDSN := databaseDSN(t, dsn, name)
	pool, err := sql.Open("pgx", testDSN)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if err := pool.PingContext(ctx); err != nil {
		t.Fatalf("ping %s: %v", name, err)
	}
	return Database{Pool: pool, DSN: testDSN}
}

// Migrated is Create with blobfs's migration set applied through
// migrate.New under sqlate's postgres dialect.
func Migrated(t testing.TB) Database {
	t.Helper()
	d := Create(t)
	set, err := postgres.Migrations()
	if err != nil {
		t.Fatalf("Migrations: %v", err)
	}
	m, err := migrate.New(d.Session(sqlatepg.Dialect{}), []migrate.Set{set}, migrate.Options{})
	if err != nil {
		t.Fatalf("migrate.New: %v", err)
	}
	if err := m.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	return d
}

// suffix returns eight random hex characters, so parallel packages never
// pick the same database name.
func suffix(t testing.TB) string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("random suffix: %v", err)
	}
	return hex.EncodeToString(b[:])
}

// databaseDSN returns dsn with its database replaced by name.
func databaseDSN(t testing.TB, dsn, name string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse %s: %v", dsn, err)
	}
	u.Path = "/" + name
	return u.String()
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

// scan runs a one-row, one-column query into dest.
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
