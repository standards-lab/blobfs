package postgres_test

import (
	"context"
	"database/sql/driver"
	"errors"
	"hash/fnv"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/standards-lab/sqlate"
	sqlatepg "github.com/standards-lab/sqlate/postgres"
	"github.com/standards-lab/sqlate/query"
	"github.com/standards-lab/sqlate/sqltest"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data"
	"github.com/standards-lab/blobfs/postgres"
)

// fallback is sqlate's postgres dialect with its capabilities hidden: the
// same placeholders and error mapping, and no query.Returner, so every
// returning command compiles to its fallback.
type fallback struct{ sqlate.Dialect }

// catalog builds the catalog a consumer on PostgreSQL builds: the engine's
// overlay of the library's patterns, and blobfs's.
func catalog(t *testing.T) *query.Catalog {
	t.Helper()
	c, err := query.NewCatalog(sqlatepg.Patterns(), data.Patterns())
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	return c
}

// newStore compiles the store over the engine under dialect and returns
// it with the variant the engine built for it.
func newStore(t *testing.T, dialect sqlate.Dialect) (*data.Store, *postgres.Variant) {
	t.Helper()
	var v *postgres.Variant
	capture := func(c *query.Catalog, d sqlate.Dialect, base *data.Standard) (data.Variant, error) {
		dv, err := postgres.Engine(c, d, base)
		if err != nil {
			return nil, err
		}
		v = dv.(*postgres.Variant)
		return dv, nil
	}
	s, err := data.New(catalog(t), dialect, data.WithEngine(capture))
	if err != nil {
		t.Fatalf("data.New: %v", err)
	}
	return s, v
}

// TestEngine proves the engine compiles against the consumer's catalog
// under the engine's dialect: two statements, both native tier, each with
// a port note, the lock requiring a transaction and the resolution not;
// the variant reporting that it serializes; the store's Verify preparing
// both beside its own; and the engine refusing a catalog without the
// blobfs namespace.
func TestEngine(t *testing.T) {
	s, v := newStore(t, sqlatepg.Dialect{})
	var names []string
	for _, st := range v.Statements() {
		names = append(names, st.Name())
		if st.Tier() != query.TierNative {
			t.Errorf("%s is %s tier, want native", st.Name(), st.Tier())
		}
		if !strings.Contains(st.Native(), "Port:") {
			t.Errorf("%s carries no port note: %q", st.Name(), st.Native())
		}
		if st.TransactionRequired() != (st.Name() == "lock_tree") {
			t.Errorf("%s: TransactionRequired = %v; only lock_tree requires one", st.Name(), st.TransactionRequired())
		}
	}
	if want := []string{"lock_tree", "resolve_path"}; !slices.Equal(names, want) {
		t.Errorf("Statements = %v, want %v", names, want)
	}
	if !v.Serializes() {
		t.Error("Serializes = false, want true")
	}

	pool, rec := sqltest.Open(t)
	if err := s.Verify(context.Background(), sqlate.Wrap(pool, sqltest.Dialect{})); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	prepared := rec.SQL(sqltest.OpPrepare)
	for _, st := range v.Statements() {
		if n := len(slices.DeleteFunc(slices.Clone(prepared), func(text string) bool { return text != st.Text() })); n != 1 {
			t.Errorf("the store's Verify prepared %s %d times, want once", st.Name(), n)
		}
	}

	bare, err := query.NewCatalog(sqlatepg.Patterns())
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	// data.New refuses such a catalog before it reaches an engine, so the
	// engine is called directly; it fails compiling, before it binds base.
	if _, err := postgres.Engine(bare, sqlatepg.Dialect{}, nil); err == nil {
		t.Error("Engine without the blobfs namespace compiled; resolve_path includes blobfs.directory_columns")
	}
}

// TestStoreForms proves the store over the engine compiles under the
// engine's dialect and under the dialect with its capabilities hidden:
// the store lists its own statements and then the variant's, and each of
// the six returning commands carries its single-statement form, a
// RETURNING over the read's columns, under the engine's dialect and none
// under the other, which runs the fallback.
func TestStoreForms(t *testing.T) {
	returning := []string{"complete_file", "create_directory", "create_file", "delete_file", "move_directory", "move_file"}
	for _, c := range []struct {
		name    string
		dialect sqlate.Dialect
		single  bool
	}{
		{"SingleStatement", sqlatepg.Dialect{}, true},
		{"Fallback", fallback{sqlatepg.Dialect{}}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _ := newStore(t, c.dialect)
			stmts := s.Statements()
			if n := len(stmts); n < 3 || stmts[n-2].Name() != "lock_tree" || stmts[n-1].Name() != "resolve_path" {
				t.Fatalf("the store's inventory does not end with the variant's two statements: %d statements", n)
			}
			var got []string
			for _, st := range stmts {
				if st.Reads() == "" {
					continue
				}
				got = append(got, st.Name())
				text := st.ReturningText()
				switch {
				case c.single && (!strings.Contains(text, "\nRETURNING ") || !strings.HasPrefix(text, strings.Fields(st.Text())[0])):
					t.Errorf("%s has no single-statement form under the engine's dialect: %q", st.Name(), text)
				case !c.single && text != "":
					t.Errorf("%s has a single-statement form under the fallback dialect: %q", st.Name(), text)
				}
			}
			if !slices.Equal(got, returning) {
				t.Errorf("the returning commands are %v, want %v", got, returning)
			}
		})
	}
}

// TestTreeLockKey pins the key: the 64-bit FNV-1a hash of TreeLockName,
// so a consumer that must avoid it, or a port that must derive the same
// one, can recompute it.
func TestTreeLockKey(t *testing.T) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(postgres.TreeLockName))
	if want := int64(h.Sum64()); postgres.TreeLockKey != want {
		t.Errorf("TreeLockKey = %d, want %d", postgres.TreeLockKey, want)
	}
}

// TestLockTreeSQL proves LockTree runs the advisory lock statement in the
// transaction with the key bound.
func TestLockTreeSQL(t *testing.T) {
	_, v := newStore(t, sqltest.Dialect{})
	pool, rec := sqltest.Open(t, sqltest.Response{Affected: 0})
	db := sqlate.Wrap(pool, sqltest.Dialect{})
	ctx := context.Background()
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := v.LockTree(ctx, tx); err != nil {
		t.Fatalf("LockTree: %v", err)
	}
	_ = tx.Commit()
	execs := rec.SQL(sqltest.OpExec)
	if len(execs) != 1 || execs[0] != "SELECT pg_advisory_xact_lock(CAST($1 AS bigint))" {
		t.Errorf("LockTree ran %q", execs)
	}
	for _, c := range rec.Calls() {
		if c.Op == sqltest.OpExec && !slices.Equal(c.Args, []any{postgres.TreeLockKey}) {
			t.Errorf("LockTree bound %v, want the key %d", c.Args, postgres.TreeLockKey)
		}
	}
}

// resolvedColumns is the resolve_path row: the directory columns then the
// depth.
var resolvedColumns = []string{"id", "parent_id", "name", "version", "created_at", "updated_at", "depth"}

// resolvedResponse scripts the deepest row a walk reached, at depth.
func resolvedResponse(id, parent, name string, depth int64) sqltest.Response {
	now := time.Now()
	return sqltest.Response{Columns: resolvedColumns, Rows: [][]driver.Value{{id, parent, name, int64(1), now, now, depth}}}
}

// openStore compiles the store over the engine under the stub dialect
// and opens a scripted pool over the responses.
func openStore(t *testing.T, responses ...sqltest.Response) (*data.Store, *sqlate.DB, *sqltest.Recorder) {
	t.Helper()
	s, _ := newStore(t, sqltest.Dialect{})
	pool, rec := sqltest.Open(t, responses...)
	return s, sqlate.Wrap(pool, sqltest.Dialect{}), rec
}

// TestResolvePathIsOneStatement proves FindByPath through the variant is
// one query whatever the depth: the recursive statement bound to the
// start id and the segments as one slice, in path order and normalized;
// the empty path binding an empty slice; a walk that stops short
// reporting the failing prefix as the baseline spells it; and no row
// reporting ErrNotFound for the start, with no prefix.
func TestResolvePathIsOneStatement(t *testing.T) {
	ctx := context.Background()
	nfd := "cafe" + string(rune(0x0301))
	nfc := "caf" + string(rune(0x00E9))
	s, db, rec := openStore(t, resolvedResponse("D", "P", "z", 3))
	d, err := s.Directories.FindByPath(ctx, db, blobfs.RootID, "a/"+nfd+"/z")
	if err != nil || d.ID != "D" || d.Name != "z" {
		t.Fatalf("FindByPath = %+v, %v, want the resolved row", d, err)
	}
	if ops := rec.Ops(); !slices.Equal(ops, []sqltest.Op{sqltest.OpQuery}) {
		t.Fatalf("ops = %v, want one query for three segments", ops)
	}
	c := rec.Calls()[0]
	if !strings.HasPrefix(c.SQL, "WITH RECURSIVE walk (id, parent_id, name, version, created_at, updated_at, depth) AS (") ||
		!strings.Contains(c.SQL, "WHERE d.id = CAST($1 AS uuid)") ||
		!strings.Contains(c.SQL, "d.name = (CAST($2 AS text[]))[w.depth + 1]") ||
		!strings.HasSuffix(c.SQL, "WHERE w.depth = (SELECT max(x.depth) FROM walk x)") {
		t.Errorf("the statement is not resolve_path:\n%s", c.SQL)
	}
	if len(c.Args) != 2 || c.Args[0] != blobfs.RootID {
		t.Fatalf("resolve bound %v, want the root id and the segments", c.Args)
	}
	if segs, ok := c.Args[1].([]string); !ok || !slices.Equal(segs, []string{"a", nfc, "z"}) {
		t.Errorf("the segments bound as %#v, want the normalized names as one []string", c.Args[1])
	}

	s, db, rec = openStore(t, resolvedResponse("S", blobfs.RootID, "s", 0))
	if _, err := s.Directories.FindByPath(ctx, db, "S", ""); err != nil {
		t.Fatalf("FindByPath(S, \"\") = %v", err)
	}
	if segs, ok := rec.Calls()[0].Args[1].([]string); !ok || len(segs) != 0 {
		t.Errorf("the empty path bound %#v, want an empty []string", rec.Calls()[0].Args[1])
	}

	// Ten segments, the walk stopping at the second: one query, and the
	// prefix names the segment that failed.
	s, db, rec = openStore(t, resolvedResponse("A", blobfs.RootID, "a", 1))
	_, err = s.Directories.FindByPath(ctx, db, blobfs.RootID, "a/missing/c/d/e/f/g/h/i/j")
	if !errors.Is(err, blobfs.ErrNotFound) || !strings.HasSuffix(err.Error(), " at a/missing: "+blobfs.ErrNotFound.Error()) {
		t.Errorf("FindByPath with the second segment missing = %v, want ErrNotFound at a/missing", err)
	}
	if ops := rec.Ops(); !slices.Equal(ops, []sqltest.Op{sqltest.OpQuery}) {
		t.Errorf("ops = %v, want one query for ten segments", ops)
	}

	s, db, _ = openStore(t, sqltest.Response{Columns: resolvedColumns})
	_, err = s.Directories.FindByPath(ctx, db, "S", "x")
	if !errors.Is(err, blobfs.ErrNotFound) || strings.Contains(err.Error(), " at ") {
		t.Errorf("FindByPath from a missing start = %v, want ErrNotFound with no prefix", err)
	}
}
