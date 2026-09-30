//go:build integration

package postgres_test

// This file holds the plan-shape and cost regression assertions for the
// statements the store runs. Each test seeds a fixture in its own
// database, explains a statement as the store composes it with EXPLAIN
// (ANALYZE, BUFFERS) through internal/dbtest, and asserts a plan shape and
// a buffer bound, never a time. The bounds carry a wide margin over the
// measured value and sit well below what the guarded regression would
// read. A plan shape is asserted only where the fixture makes the index
// the planner's own choice, and no test disables a plan type.

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/standards-lab/sqlate"
	sqlatepg "github.com/standards-lab/sqlate/postgres"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data"
	"github.com/standards-lab/blobfs/postgres"
	"github.com/standards-lab/blobfs/postgres/internal/dbtest"
)

// listingSizes is the fixture of the listing assertions: the big
// directory holds a tenth of the files, so a read of the whole directory
// is a scan over its own pages and not a sequential scan of the table,
// and its files were inserted first, so they sit on contiguous heap
// pages. stepSizes is the fixture of the per-row assertions: enough rows
// that a lookup by primary key or by a unique constraint is the planner's
// choice over a sequential scan on both tables.
var (
	listingSizes = dbtest.Sizes{Depth: 8, Directories: 3000, BigFiles: 8000, OtherFiles: 72000}
	stepSizes    = dbtest.Sizes{Depth: 8, Directories: 3000, BigFiles: 2000, OtherFiles: 8000}
)

// costPageSize is the page size of the listing assertions, and middlePage
// the number of the offset page whose cursor continues from the middle of
// the big directory.
const costPageSize = 20

var middlePage = listingSizes.BigFiles / costPageSize / 2

// sortIndexDDL creates the index a consumer's own migration set adds for
// a listing sorted by created_at; blobfs's set ships none.
const sortIndexDDL = "CREATE INDEX blobfs_ix_file_directory_created ON blobfs_file (directory_id, created_at)"

// costEnv is one cost test's throwaway database, seeded, and the store
// over the PostgreSQL variant under sqlate's postgres dialect.
type costEnv struct {
	ctx   context.Context
	db    *sqlate.DB
	store *data.Store
	tree  dbtest.Tree
	ex    *dbtest.Explainer
}

// openCost builds the environment over a fixture of sizes.
func openCost(t *testing.T, sizes dbtest.Sizes) costEnv {
	t.Helper()
	ctx := context.Background()
	d := dbtest.Migrated(t)
	db := d.Session(sqlatepg.Dialect{})
	c, err := query.NewCatalog(sqlatepg.Patterns(), data.Patterns())
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	store, err := data.New(c, db.Dialect(), data.WithEngine(postgres.Engine))
	if err != nil {
		t.Fatalf("data.New: %v", err)
	}
	return costEnv{ctx: ctx, db: db, store: store, tree: dbtest.SeedTree(ctx, t, db, sizes), ex: dbtest.NewExplainer(t, d.DSN)}
}

// recorder is a session over the pool that keeps every query's text and
// arguments, so a test explains exactly what the store ran. The embedded
// *sqlate.DB keeps MapError and Begin reachable.
type recorder struct {
	*sqlate.DB
	calls []call
}

// call is one query as the engine received it.
type call struct {
	sql  string
	args []any
}

func (r *recorder) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	r.calls = append(r.calls, call{sql: q, args: args})
	return r.DB.QueryContext(ctx, q, args...)
}

// one runs op through a recorder over db and returns the one query it ran.
func one(t *testing.T, db *sqlate.DB, op func(sess sqlate.Session) error) call {
	t.Helper()
	rec := &recorder{DB: db}
	if err := op(rec); err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("the operation ran %d queries, want 1", len(rec.calls))
	}
	return rec.calls[0]
}

// page runs a listing through a recorder over db and returns its page, the
// first of its two queries; the second, the directory's read by id, is
// covered by TestProtocolStepPlans.
func page(t *testing.T, db *sqlate.DB, op func(sess sqlate.Session) error) call {
	t.Helper()
	rec := &recorder{DB: db}
	if err := op(rec); err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 2 || !strings.Contains(rec.calls[1].sql, "WHERE d.id = CAST($1 AS uuid)") {
		t.Fatalf("the listing ran %d queries, want its page and the read of its directory", len(rec.calls))
	}
	return rec.calls[0]
}

// explainList captures the file listing of dir as the store composes it,
// deleting files hidden, and explains it.
func (e costEnv) explainList(t *testing.T, dir string, req query.Directives, pg query.Page) dbtest.Plan {
	t.Helper()
	c := page(t, e.db, func(sess sqlate.Session) error {
		_, err := e.store.Files.List(e.ctx, sess, dir, req, pg)
		return err
	})
	return e.ex.Explain(e.ctx, t, c.sql, c.args...)
}

// explainContinue captures the page past after and explains it.
func (e costEnv) explainContinue(t *testing.T, dir string, req query.Directives, after query.Cursor) dbtest.Plan {
	t.Helper()
	c := page(t, e.db, func(sess sqlate.Session) error {
		_, err := e.store.Files.Continue(e.ctx, sess, dir, req, after, costPageSize)
		return err
	})
	return e.ex.Explain(e.ctx, t, c.sql, c.args...)
}

// cursorAt returns the cursor of offset page number under req.
func (e costEnv) cursorAt(t *testing.T, dir string, req query.Directives, number int) query.Cursor {
	t.Helper()
	req.Total = query.TotalNone
	c, err := e.store.Files.List(e.ctx, e.db, dir, req, query.Page{Number: number, Size: costPageSize})
	if err != nil || c.Next == "" {
		t.Fatalf("page %d = %+v, %v; want a cursor", number, c, err)
	}
	return c.Next
}

// TestListingPlans checks the listing's plan on the big directory, deleting
// files hidden: under TotalNone the first and a continued page, either
// direction, are an index scan on blobfs_uq_file_directory_name with no
// sort, window, or sequential scan; under TotalExact the first page reads
// the directory once under one WindowAgg. The bounds catch a keyset
// predicate the index cannot serve, a total over the table, and a second
// pass.
func TestListingPlans(t *testing.T) {
	e := openCost(t, listingSizes)
	big := e.tree.Big.ID
	const bound = 96
	for _, tc := range []struct {
		label   string
		sort    []query.Sort
		compare string
	}{
		{"name ascending", nil, "name >"},
		{"name descending", []query.Sort{{Field: "name", Descending: true}}, "name <"},
	} {
		req := query.Directives{Sort: tc.sort, Total: query.TotalNone}
		first := e.explainList(t, big, req, query.Page{Number: 1, Size: costPageSize})
		middle := e.explainContinue(t, big, req, e.cursorAt(t, big, req, middlePage))
		t.Logf("%s without the total: page 1 costs %d buffers, the cursor page from the middle %d", tc.label, first.Buffers, middle.Buffers)
		for _, p := range []struct {
			label string
			plan  dbtest.Plan
		}{{"page 1", first}, {"the cursor page", middle}} {
			switch {
			case p.plan.Has("Seq Scan"), p.plan.Has("Sort"), p.plan.Has("WindowAgg"), !p.plan.Has("using blobfs_uq_file_directory_name"):
				t.Errorf("%s, %s: not an index scan on the name index without a sort or a window:\n%s", tc.label, p.label, p.plan.Text)
			case p.plan.Buffers > bound:
				t.Errorf("%s, %s: reads %d buffers, more than %d:\n%s", tc.label, p.label, p.plan.Buffers, bound, p.plan.Text)
			}
		}
		if !indexCondHas(middle, tc.compare) {
			t.Errorf("%s: the keyset comparison %q is not an index condition:\n%s", tc.label, tc.compare, middle.Text)
		}
	}

	heap := dbtest.HeapBlocks(e.ctx, t, e.db, big)
	table := dbtest.RelationPages(e.ctx, t, e.db, "blobfs_file")
	counted := e.explainList(t, big, query.Directives{}, query.Page{Number: 1, Size: costPageSize})
	exactBound := 3 * heap
	t.Logf("with the exact total page 1 costs %d buffers; the bound is %d, the directory has %d heap pages and the table %d", counted.Buffers, exactBound, heap, table)
	if table <= exactBound {
		t.Fatalf("the fixture is too small: the table has %d pages, within the bound %d", table, exactBound)
	}
	switch {
	case counted.Has("Seq Scan"), counted.Has("SubPlan"), counted.Has("InitPlan"), !counted.Has("WindowAgg"):
		t.Errorf("the counted page is not one index-backed read under a WindowAgg:\n%s", counted.Text)
	case counted.HeapScans("blobfs_file") != 1:
		t.Errorf("the counted page reads blobfs_file %d times, want once:\n%s", counted.HeapScans("blobfs_file"), counted.Text)
	case counted.Buffers > exactBound:
		t.Errorf("the counted page reads %d buffers, more than %d:\n%s", counted.Buffers, exactBound, counted.Text)
	case counted.Buffers <= bound:
		t.Errorf("the counted page reads %d buffers, within the uncounted bound %d; the window did not read the directory", counted.Buffers, bound)
	}
}

// TestRowValueCursorCost checks a cursor page by created_at over the
// consumer's index, under the engine's keyset overlay, is an index scan
// whose cost does not grow with the cursor's position; the regression is
// the expanded disjunct chain applied as a filter.
func TestRowValueCursorCost(t *testing.T) {
	e := openCost(t, listingSizes)
	for _, stmt := range []string{sortIndexDDL, "ANALYZE blobfs_file"} {
		if _, err := e.db.ExecContext(e.ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	const bound = 100
	big := e.tree.Big.ID
	for _, tc := range []struct {
		label   string
		sort    []query.Sort
		compare string
	}{
		{"created_at ascending", []query.Sort{{Field: "created_at"}}, "created_at >="},
		{"created_at descending", []query.Sort{{Field: "created_at", Descending: true}}, "created_at <="},
	} {
		req := query.Directives{Sort: tc.sort, Total: query.TotalNone}
		start := e.explainContinue(t, big, req, e.cursorAt(t, big, req, 1))
		middle := e.explainContinue(t, big, req, e.cursorAt(t, big, req, middlePage))
		t.Logf("%s: the cursor page costs %d buffers at the start and %d in the middle", tc.label, start.Buffers, middle.Buffers)
		for _, p := range []struct {
			position string
			plan     dbtest.Plan
		}{{"start", start}, {"middle", middle}} {
			switch {
			case p.plan.Has("Seq Scan"), !p.plan.Has("using blobfs_ix_file_directory_created"):
				t.Errorf("%s, %s: the cursor page is not an index scan on the created_at index:\n%s", tc.label, p.position, p.plan.Text)
			case !indexCondHas(p.plan, tc.compare):
				t.Errorf("%s, %s: the bound %q is not an index condition:\n%s", tc.label, p.position, tc.compare, p.plan.Text)
			case p.plan.Buffers > bound:
				t.Errorf("%s, %s: the cursor page reads %d buffers, more than %d:\n%s", tc.label, p.position, p.plan.Buffers, bound, p.plan.Text)
			}
		}
		if middle.Buffers > 2*start.Buffers {
			t.Errorf("%s: the cursor page reads %d buffers in the middle and %d at the start; the cost depends on the position:\n%s", tc.label, middle.Buffers, start.Buffers, middle.Text)
		}
	}
}

// TestPathPlans checks the tree walks cost the depth and not the table:
// the baseline's step, the variant's one-statement resolution, and the
// upward walks of Path and the cycle check, each through an index and no
// sequential scan, within a per-level buffer bound.
func TestPathPlans(t *testing.T) {
	e := openCost(t, stepSizes)
	stmts := statementsByName(e.store)
	const depth = 6
	bound := 8 * depth
	deepest := e.tree.Chain[len(e.tree.Chain)-1].ID

	step := stmts["directory_by_name"]
	p := e.ex.Explain(e.ctx, t, step.Text(), bindArgs(t, step, query.Args{"parent_id": e.tree.Chain[4].ID, "name": e.tree.Chain[5].Name})...)
	t.Logf("one step of the baseline's walk costs %d buffers", p.Buffers)
	switch {
	case p.Has("Seq Scan"), !p.Has("Index Scan using blobfs_uq_directory_parent_name"):
		t.Errorf("the step is not an index scan on the unique constraint:\n%s", p.Text)
	case !indexCondHas(p, "parent_id ="), !indexCondHas(p, "name ="):
		t.Errorf("the step's index condition does not carry both columns:\n%s", p.Text)
	case p.Buffers > 8:
		t.Errorf("the step reads %d buffers, more than 8:\n%s", p.Buffers, p.Text)
	}

	c := one(t, e.db, func(sess sqlate.Session) error {
		d, err := e.store.Directories.FindByPath(e.ctx, sess, blobfs.RootID, e.tree.Path(depth))
		if err == nil && d.ID != e.tree.Chain[depth-1].ID {
			t.Fatalf("resolved %s to %s, want %s", e.tree.Path(depth), d.ID, e.tree.Chain[depth-1].ID)
		}
		return err
	})
	p = e.ex.Explain(e.ctx, t, c.sql, c.args...)
	t.Logf("the variant's resolution of a depth-%d path costs %d buffers; the bound is %d", depth, p.Buffers, bound)
	switch {
	case p.Has("Seq Scan"), !p.Has("Recursive Union"), !p.Has("Index Scan using blobfs_pk_directory"), !p.Has("Index Scan using blobfs_uq_directory_parent_name"):
		t.Errorf("the resolution is not a recursion over the two indexes:\n%s", p.Text)
	case !indexCondHas(p, "parent_id ="), !indexCondHas(p, "name ="):
		t.Errorf("the recursive step's index condition does not carry both columns:\n%s", p.Text)
	case p.Buffers > bound:
		t.Errorf("the resolution reads %d buffers, more than %d:\n%s", p.Buffers, bound, p.Text)
	}

	levels := len(e.tree.Chain) + 1
	for _, tc := range []struct {
		name string
		args query.Args
	}{
		{"directory_ancestors", query.Args{"id": deepest}},
		{"directory_is_within", query.Args{"id": deepest, "ancestor_id": e.tree.Chain[0].ID}},
	} {
		st := stmts[tc.name]
		p := e.ex.Explain(e.ctx, t, st.Text(), bindArgs(t, st, tc.args)...)
		t.Logf("%s from depth %d costs %d buffers", tc.name, len(e.tree.Chain), p.Buffers)
		switch {
		case p.Has("Seq Scan"), !p.Has("Recursive Union"), !p.Has("Index Scan using blobfs_pk_directory"):
			t.Errorf("%s is not a recursion over the primary key:\n%s", tc.name, p.Text)
		case p.Buffers > 8*levels:
			t.Errorf("%s reads %d buffers, more than %d:\n%s", tc.name, p.Buffers, 8*levels, p.Text)
		}
	}
}

// TestProtocolStepPlans checks every protocol step, in its single-statement
// form, finds its row through the primary key, with and without a
// nullable version, within 32 buffers. A create finds its directory or
// parent the same way. No step scans a table, including a create's or a
// move's probe for a deleting row that holds the name. A tenth of the
// fixture's files are pending, so the stale partial index is not the
// cheapest path to a row by id.
func TestProtocolStepPlans(t *testing.T) {
	e := openCost(t, stepSizes)
	stmts := statementsByName(e.store)
	const bound = 32
	if _, err := e.db.ExecContext(e.ctx, "UPDATE blobfs_file SET status = 'pending', size = NULL "+
		"WHERE id IN (SELECT id FROM blobfs_file ORDER BY id LIMIT 1000)"); err != nil {
		t.Fatalf("seed the pending rows: %v", err)
	}
	if _, err := e.db.ExecContext(e.ctx, "ANALYZE blobfs_file"); err != nil {
		t.Fatalf("ANALYZE: %v", err)
	}
	insert := func(name, status string) string {
		t.Helper()
		id, err := insertFile(e.ctx, e.db, e.tree.Big.ID, name, status)
		if err != nil {
			t.Fatalf("insert %s: %v", name, err)
		}
		return id
	}
	available := insert("step.txt", "available")
	pending := insert("pending.txt", "pending")
	deleting := insert("deleting.txt", "deleting")
	dir := e.tree.Chain[3]
	root := blobfs.RootID
	empty, err := insertDirectory(e.ctx, e.db, &root, ptr("empty"))
	if err != nil {
		t.Fatalf("insert an empty directory: %v", err)
	}
	for _, tc := range []struct {
		name  string
		index string
		args  query.Args
	}{
		{"create_file", "blobfs_pk_directory", query.Args{"id": blobfs.NewID(), "directory_id": e.tree.Big.ID, "name": "created.txt", "key": "created", "content_type": "text/plain"}},
		{"complete_file", "blobfs_pk_file", query.Args{"id": pending, "version": int64(1), "size": int64(3), "content_type": "text/plain", "etag": "etag"}},
		{"move_file", "blobfs_pk_file", query.Args{"id": available, "version": int64(1), "directory_id": e.tree.Chain[0].ID, "name": "moved.txt"}},
		{"delete_file", "blobfs_pk_file", query.Args{"id": available, "version": nil}},
		{"delete_file", "blobfs_pk_file", query.Args{"id": available, "version": int64(1)}},
		{"hold_file", "blobfs_pk_file", query.Args{"id": available, "version": nil}},
		{"hold_file", "blobfs_pk_file", query.Args{"id": available, "version": int64(1)}},
		{"lock_file", "blobfs_pk_file", query.Args{"id": available, "version": nil}},
		{"lock_file", "blobfs_pk_file", query.Args{"id": available, "version": int64(1)}},
		{"purge_file", "blobfs_pk_file", query.Args{"id": deleting}},
		{"file_by_id", "blobfs_pk_file", query.Args{"id": available}},
		{"create_directory", "blobfs_pk_directory", query.Args{"id": blobfs.NewID(), "parent_id": blobfs.RootID, "name": "created"}},
		{"move_directory", "blobfs_pk_directory", query.Args{"id": dir.ID, "version": int64(1), "parent_id": blobfs.RootID, "name": "moved"}},
		{"delete_directory", "blobfs_pk_directory", query.Args{"id": empty, "version": nil}},
		{"delete_directory", "blobfs_pk_directory", query.Args{"id": empty, "version": int64(1)}},
		{"directory_by_id", "blobfs_pk_directory", query.Args{"id": dir.ID}},
	} {
		st, ok := stmts[tc.name]
		if !ok {
			t.Fatalf("the store has no %s statement", tc.name)
		}
		text := st.Text()
		if st.Reads() != "" {
			if text = st.ReturningText(); !strings.Contains(text, "RETURNING") {
				t.Fatalf("%s has no single-statement form:\n%s", tc.name, text)
			}
		}
		p := e.ex.Explain(e.ctx, t, text, bindArgs(t, st, tc.args)...)
		t.Logf("%s with %v costs %d buffers", tc.name, tc.args, p.Buffers)
		switch {
		case p.Has("Seq Scan"), !p.Has("Index Scan using " + tc.index), !indexCondHas(p, "id ="):
			t.Errorf("%s with %v does not find the row through %s:\n%s", tc.name, tc.args, tc.index, p.Text)
		case p.Buffers > bound:
			t.Errorf("%s with %v reads %d buffers, more than %d:\n%s", tc.name, tc.args, p.Buffers, bound, p.Text)
		}
	}
}

// statementsByName indexes a store's inventory by name.
func statementsByName(store *data.Store) map[string]query.Statement {
	out := map[string]query.Statement{}
	for _, st := range store.Statements() {
		out[st.Name()] = st
	}
	return out
}

// bindArgs orders args by the statement's parameters, as the query
// library does before it runs the statement.
func bindArgs(t *testing.T, st query.Statement, args query.Args) []any {
	t.Helper()
	out := make([]any, 0, len(st.Params()))
	for _, name := range st.Params() {
		v, ok := args[name]
		if !ok {
			t.Fatalf("%s: no value for %s", st.Name(), name)
		}
		out = append(out, v)
	}
	return out
}

// indexCondHas reports whether an Index Cond line of the plan contains s,
// so a comparison the planner applies as a filter instead does not pass.
func indexCondHas(p dbtest.Plan, s string) bool {
	for _, line := range p.Lines("Index Cond:") {
		if strings.Contains(line, s) {
			return true
		}
	}
	return false
}

// TestDeletingPlan checks deleting_branches reaches its candidates through
// blobfs_ix_directory_deleting and their parents through the primary key,
// within a bound set by the deleting directories and not the table.
func TestDeletingPlan(t *testing.T) {
	e := openCost(t, stepSizes)
	if _, err := e.db.Transact(e.ctx, func(tx *sqlate.Tx) (data.Marked, error) {
		return e.store.Directories.MarkDeleting(e.ctx, tx, e.tree.Chain[2].ID)
	}); err != nil {
		t.Fatalf("MarkDeleting: %v", err)
	}
	if _, err := e.db.ExecContext(e.ctx, "ANALYZE blobfs_directory"); err != nil {
		t.Fatalf("ANALYZE: %v", err)
	}
	c := one(t, e.db, func(sess sqlate.Session) error {
		roots, err := e.store.Directories.Deleting(e.ctx, sess, 10)
		if err == nil && (len(roots) != 1 || roots[0].ID != e.tree.Chain[2].ID) {
			t.Fatalf("Deleting = %+v, want the marked directory alone", roots)
		}
		return err
	})
	p := e.ex.Explain(e.ctx, t, c.sql, c.args...)
	rows := dbtest.Int(e.ctx, t, e.db, "SELECT COUNT(*) FROM blobfs_directory")
	deleting := dbtest.Int(e.ctx, t, e.db, "SELECT COUNT(*) FROM blobfs_directory WHERE status = 'deleting'")
	index := dbtest.RelationPages(e.ctx, t, e.db, "blobfs_ix_directory_deleting")
	bound := deleting + index + 8
	t.Logf("the roots' read costs %d buffers over %d deleting directories; the bound is %d, the table has %d rows", p.Buffers, deleting, bound, rows)
	switch {
	case !p.Has("blobfs_ix_directory_deleting"):
		t.Errorf("the roots' read does not reach the candidates through the partial index:\n%s", p.Text)
	case !p.Has("Index Scan using blobfs_pk_directory on blobfs_directory p"), !indexCondHas(p, "id = d.parent_id"):
		t.Errorf("the roots' read does not read each parent through the primary key:\n%s", p.Text)
	case p.Buffers > bound:
		t.Errorf("the roots' read reads %d buffers, more than %d:\n%s", p.Buffers, bound, p.Text)
	}
}

// TestStalePlan checks stale_files_before, as Store.Sweep binds it, reads
// the oldest stale rows through blobfs_ix_file_stale in the index's order,
// bounded by the age, with no sort, and stops at the page.
func TestStalePlan(t *testing.T) {
	e := openCost(t, stepSizes)
	for _, seed := range []struct{ status, age string }{
		{"pending", "2 hours"}, {"deleting", "2 hours"}, {"pending", "0 seconds"}, {"deleting", "0 seconds"},
	} {
		if _, err := e.db.ExecContext(e.ctx, "UPDATE blobfs_file SET status = $1, updated_at = now() - CAST($2 AS interval) "+
			"WHERE id IN (SELECT id FROM blobfs_file WHERE status = 'available' ORDER BY id LIMIT 250)", seed.status, seed.age); err != nil {
			t.Fatalf("seed the stale rows: %v", err)
		}
	}
	if _, err := e.db.ExecContext(e.ctx, "ANALYZE blobfs_file"); err != nil {
		t.Fatalf("ANALYZE: %v", err)
	}
	const fetch = 20
	st := statementsByName(e.store)["stale_files_before"]
	args := bindArgs(t, st, query.Args{"before": time.Now().Add(-time.Hour), "offset": 0, "fetch": fetch})
	p := e.ex.Explain(e.ctx, t, st.Text(), args...)
	rows := dbtest.Int(e.ctx, t, e.db, "SELECT COUNT(*) FROM blobfs_file")
	stale := dbtest.Int(e.ctx, t, e.db, "SELECT COUNT(*) FROM blobfs_file WHERE status IN ('pending', 'deleting')")
	index := dbtest.RelationPages(e.ctx, t, e.db, "blobfs_ix_file_stale")
	bound := fetch + index + 8
	t.Logf("the stale read costs %d buffers for a page of %d over %d stale rows; the bound is %d, the table has %d rows", p.Buffers, fetch, stale, bound, rows)
	switch {
	case p.Has("Seq Scan"), !p.Has("Index Scan using blobfs_ix_file_stale"), !indexCondHas(p, "updated_at <"):
		t.Errorf("the stale read does not reach the old stale rows through the partial index:\n%s", p.Text)
	case p.Has("Sort"):
		t.Errorf("the stale read sorts the stale rows instead of reading them in the index's order:\n%s", p.Text)
	case p.Buffers > bound:
		t.Errorf("the stale read reads %d buffers, more than %d:\n%s", p.Buffers, bound, p.Text)
	}
}
