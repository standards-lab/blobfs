package datatest

import (
	"database/sql"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
)

// listAll is the request for a whole listing in the default order with
// the total.
func listAll() query.Directives { return query.Directives{} }

// firstPage is page 1 at size.
func firstPage(size int) query.Page { return query.Page{Number: 1, Size: size} }

// listing checks the two listings, Directories.List and Files.List with
// their Continue, against the rows a plain query returns, and the page,
// total, and cursor rules the query library composes for them.
func (s *suite) listing(t *testing.T) {
	f := s.seedListing(t)
	t.Run("FilesMatchAPlainQuery", func(t *testing.T) { s.filesMatchAPlainQuery(t, f) })
	t.Run("DirectoriesMatchAPlainQuery", func(t *testing.T) { s.directoriesMatchAPlainQuery(t, f) })
	t.Run("PageBoundaries", s.pageBoundaries)
	t.Run("EmptyPages", s.emptyPages)
	t.Run("TotalUnderConcurrentInserts", s.totalUnderConcurrentInserts)
	t.Run("Refusals", s.listingRefusals)
	t.Run("CursorIsAPosition", func(t *testing.T) { s.cursorIsAPosition(t, f) })
}

// listingFixture is the listing group's tree: directories at four depths
// below one top directory, with file names repeated across directories so
// a listing that leaked past its directory would show.
type listingFixture struct {
	top blobfs.Directory
	// dirs is every directory of the fixture by its path below top, the
	// empty path for top itself.
	dirs map[string]string
}

// seedListing builds the listing fixture, with files of every status and
// sizes that tie.
func (s *suite) seedListing(t *testing.T) listingFixture {
	t.Helper()
	top := s.mkdir(t, "listing-"+t.Name())
	f := listingFixture{top: top, dirs: map[string]string{"": top.ID}}
	dir := func(parent, n string) string {
		d := s.mkdirUnder(t, f.dirs[parent], n)
		path := strings.TrimPrefix(parent+"/"+n, "/")
		f.dirs[path] = d.ID
		return path
	}
	d1 := dir("", "d1")
	d2 := dir("", "d2")
	e1 := dir(d1, "e1")
	e2 := dir(d1, "e2")
	g := dir(e1, "g")
	dir(d1, "a-dir")
	for path, names := range map[string][]string{
		"": {"r1", "r2", "a1"},
		d1: {"a1", "a2", "a3", "b1", "b22"},
		e1: {"a1", "x1", "a3"},
		e2: {"y1"},
		g:  {"z1", "a1", "a2"},
		d2: {"w1", "a1"},
	} {
		for i, n := range names {
			status := []blobfs.Status{blobfs.StatusAvailable, blobfs.StatusPending, blobfs.StatusDeleting}[i%3]
			s.insertFile(t, f.dirs[path], n, status)
		}
	}
	return f
}

// fileSorts are the file sorts the listing group checks, each with the
// ORDER BY of the plain query that must return the same rows: by name in
// both directions, by a nullable field, and by created_at, each with the
// name appended as the tie-breaker.
var fileSorts = []struct {
	label   string
	orderBy string
	sort    []query.Sort
}{
	{"name", "f.name", nil},
	{"name desc", "f.name DESC", []query.Sort{{Field: "name", Descending: true}}},
	{"size then name", "f.size, f.name", []query.Sort{{Field: "size"}}},
	{"status desc then name desc", "f.status DESC, f.name DESC", []query.Sort{{Field: "status", Descending: true}}},
	{"created_at desc then name desc", "f.created_at DESC, f.name DESC", []query.Sort{{Field: "created_at", Descending: true}}},
}

// fileFilters are the file filters the listing group checks, each with
// the predicate of the plain query and its argument: none, a LIKE on the
// name, a status, and a null size.
var fileFilters = []struct {
	label     string
	predicate string
	arg       any
	filters   []query.Filter
}{
	{"all", "", nil, nil},
	{"like a%", "f.name LIKE %s", "a%", []query.Filter{{Field: "name", Op: query.OpLike, Value: "a%"}}},
	{"available", "f.status = %s", "available", []query.Filter{{Field: "status", Op: query.OpEq, Value: "available"}}},
	{"size null", "f.size IS NULL", nil, []query.Filter{{Field: "size", Op: query.OpIsNull}}},
}

// filesMatchAPlainQuery checks, for every directory of the fixture and a
// missing one, under every sort and filter, at several page sizes, that
// the pages read by number and concatenated equal the rows a plain query
// over blobfs_file returns, that every counted page reports the plain
// query's count, and that the pages without the total return the same
// rows and query.NoTotal.
func (s *suite) filesMatchAPlainQuery(t *testing.T, f listingFixture) {
	p := s.db.Dialect().Placeholder
	dirs := maps.Clone(f.dirs)
	dirs["missing"] = blobfs.NewID()
	for path, dir := range dirs {
		for _, filter := range fileFilters {
			for _, sort := range fileSorts {
				text := "SELECT f.id FROM blobfs_file f WHERE f.directory_id = " + p(1)
				args := []any{dir}
				if filter.predicate != "" {
					if filter.arg != nil {
						text += " AND " + fmt.Sprintf(filter.predicate, p(2))
						args = append(args, filter.arg)
					} else {
						text += " AND " + filter.predicate
					}
				}
				want := s.column(t, text+" ORDER BY "+sort.orderBy, args...)
				req := query.Directives{Sort: sort.sort, Filters: filter.filters}
				for _, size := range []int{1, 2, 3, 10} {
					got, total := s.fileOffsetWalk(t, dir, req, size)
					if !slices.Equal(got, want) || total != len(want) {
						t.Errorf("/%s %s by %s, size %d: the listing %v with total %d, the plain query %v", path, filter.label, sort.label, size, got, total, want)
					}
				}
				req.Total = query.TotalNone
				if got, total := s.fileOffsetWalk(t, dir, req, 2); !slices.Equal(got, want) || total != query.NoTotal {
					t.Errorf("/%s %s by %s without the total: the listing %v with total %d, the plain query %v", path, filter.label, sort.label, got, total, want)
				}
			}
		}
	}
}

// directoriesMatchAPlainQuery checks the directory listing the same way,
// by name in both directions and by created_at, with and without a LIKE
// filter.
func (s *suite) directoriesMatchAPlainQuery(t *testing.T, f listingFixture) {
	p := s.db.Dialect().Placeholder
	dirs := maps.Clone(f.dirs)
	dirs["missing"] = blobfs.NewID()
	sorts := []struct {
		label   string
		orderBy string
		sort    []query.Sort
	}{
		{"name", "d.name", nil},
		{"name desc", "d.name DESC", []query.Sort{{Field: "name", Descending: true}}},
		{"created_at then name", "d.created_at, d.name", []query.Sort{{Field: "created_at"}}},
	}
	for path, dir := range dirs {
		for _, like := range []string{"", "a%"} {
			var filters []query.Filter
			text := "SELECT d.id FROM blobfs_directory d WHERE d.parent_id = " + p(1)
			args := []any{dir}
			if like != "" {
				filters = []query.Filter{{Field: "name", Op: query.OpLike, Value: like}}
				text += " AND d.name LIKE " + p(2)
				args = append(args, like)
			}
			for _, sort := range sorts {
				want := s.column(t, text+" ORDER BY "+sort.orderBy, args...)
				for _, size := range []int{1, 2, 10} {
					var got []string
					total := query.NoTotal
					for n := 1; n <= 100; n++ {
						c, err := s.store.Directories.List(s.ctx, s.db, dir, query.Directives{Sort: sort.sort, Filters: filters}, query.Page{Number: n, Size: size})
						if err != nil {
							t.Fatalf("Directories.List: %v", err)
						}
						if n == 1 || len(c.Items) > 0 {
							total = c.Total
						}
						for _, d := range c.Items {
							if d.IsRoot() {
								t.Errorf("a listing holds the root")
							}
							got = append(got, d.ID)
						}
						if !c.More {
							break
						}
					}
					if !slices.Equal(got, want) || total != len(want) {
						t.Errorf("/%s like %q by %s, size %d: the listing %v with total %d, the plain query %v", path, like, sort.label, size, got, total, want)
					}
				}
			}
		}
	}
}

// fileOffsetWalk reads the file listing of dir page by page by number
// until a page reports no More, checking that every page after an earlier
// one's More holds rows, that a short page is the last, and that every
// counted page with rows reports the same total, and returns the ids
// concatenated and the total, query.NoTotal when the pages carried none.
func (s *suite) fileOffsetWalk(t *testing.T, dir string, req query.Directives, size int) ([]string, int) {
	t.Helper()
	var ids []string
	total := query.NoTotal
	for n := 1; n <= 100; n++ {
		c, err := s.store.Files.List(s.ctx, s.db, dir, req, query.Page{Number: n, Size: size})
		if err != nil {
			t.Fatalf("page %d of size %d: %v", n, size, err)
		}
		if len(c.Items) > size {
			t.Fatalf("page %d of size %d holds %d rows", n, size, len(c.Items))
		}
		if n == 1 || len(c.Items) > 0 {
			if n > 1 && c.Total != total {
				t.Errorf("page %d of size %d reports total %d, earlier pages %d", n, size, c.Total, total)
			}
			total = c.Total
		}
		for _, f := range c.Items {
			ids = append(ids, f.ID)
		}
		if len(c.Items) < size && c.More {
			t.Errorf("page %d of size %d holds %d rows and reports More", n, size, len(c.Items))
		}
		if !c.More {
			return ids, total
		}
		if c.Next == "" && req.Sort == nil {
			t.Errorf("page %d under the default order reports More and no cursor", n)
		}
	}
	t.Fatalf("the listing of size %d never ended", size)
	return nil, 0
}

// pageBoundaries checks More, the total, and the cursor at the exact
// boundaries: three files at size 3 fill one page with no More and no
// cursor; at size 2 the first page has More and a cursor and the second
// one row and no More, by number and by cursor; four files at size 2
// fill page 2 exactly with no More; the rule holds without the total;
// and a sort by a nullable field, which a cursor cannot continue, reports
// More with no cursor, the state that says to page by number.
func (s *suite) pageBoundaries(t *testing.T) {
	top := s.mkdir(t, "boundaries-"+t.Name())
	three := s.mkdirUnder(t, top.ID, "three").ID
	four := s.mkdirUnder(t, top.ID, "four").ID
	s.mkdirUnder(t, top.ID, "empty")
	for _, n := range []string{"a", "b", "c"} {
		s.insertFile(t, three, n, blobfs.StatusAvailable)
	}
	for _, n := range []string{"a", "b", "c", "d"} {
		s.insertFile(t, four, n, blobfs.StatusAvailable)
	}
	list := func(dir string, req query.Directives, number, size int) query.Collection[blobfs.File] {
		t.Helper()
		c, err := s.store.Files.List(s.ctx, s.db, dir, req, query.Page{Number: number, Size: size})
		if err != nil {
			t.Fatalf("Files.List: %v", err)
		}
		return c
	}
	check := func(label string, items, total int, more, next bool, gotItems, gotTotal int, gotMore bool, gotNext query.Cursor) {
		t.Helper()
		if gotItems != items || gotTotal != total || gotMore != more || (gotNext != "") != next {
			t.Errorf("%s = %d rows, total %d, more %v, cursor %v; want %d rows, total %d, more %v, cursor %v", label, gotItems, gotTotal, gotMore, gotNext != "", items, total, more, next)
		}
	}
	checkFiles := func(label string, c query.Collection[blobfs.File], items, total int, more, next bool) {
		t.Helper()
		check(label, items, total, more, next, len(c.Items), c.Total, c.More, c.Next)
	}
	none := query.Directives{Total: query.TotalNone}
	bySize := query.Directives{Sort: []query.Sort{{Field: "size"}}}

	checkFiles("three at size 3", list(three, listAll(), 1, 3), 3, 3, false, false)
	first := list(three, listAll(), 1, 2)
	checkFiles("three at size 2, page 1", first, 2, 3, true, true)
	checkFiles("three at size 2, page 2", list(three, listAll(), 2, 2), 1, 3, false, false)
	next, err := s.store.Files.Continue(s.ctx, s.db, three, listAll(), first.Next, 2)
	if err != nil {
		t.Fatalf("Files.Continue: %v", err)
	}
	checkFiles("three at size 2, past the cursor", next, 1, 3, false, false)
	checkFiles("four at size 2, page 2", list(four, listAll(), 2, 2), 2, 4, false, false)
	checkFiles("four at size 2, page 1, no total", list(four, none, 1, 2), 2, query.NoTotal, true, true)
	checkFiles("four at size 2, page 2, no total", list(four, none, 2, 2), 2, query.NoTotal, false, false)
	checkFiles("three by size, page 1", list(three, bySize, 1, 2), 2, 3, true, false)
	checkFiles("three by size, page 2", list(three, bySize, 2, 2), 1, 3, false, false)

	dirs, err := s.store.Directories.List(s.ctx, s.db, top.ID, listAll(), firstPage(2))
	if err != nil {
		t.Fatalf("Directories.List: %v", err)
	}
	check("directories at size 2", 2, 3, true, true, len(dirs.Items), dirs.Total, dirs.More, dirs.Next)
	rest, err := s.store.Directories.Continue(s.ctx, s.db, top.ID, listAll(), dirs.Next, 2)
	if err != nil {
		t.Fatalf("Directories.Continue: %v", err)
	}
	check("directories past the cursor", 1, 3, false, false, len(rest.Items), rest.Total, rest.More, rest.Next)
	dirs, err = s.store.Directories.List(s.ctx, s.db, top.ID, query.Directives{Sort: []query.Sort{{Field: "parent_id"}}}, firstPage(2))
	if err != nil {
		t.Fatalf("Directories.List by parent_id: %v", err)
	}
	check("directories by parent_id at size 2", 2, 3, true, false, len(dirs.Items), dirs.Total, dirs.More, dirs.Next)
}

// emptyPages checks the counted total's rules for a page with no row: an
// empty first page reports the exact total 0; a page past the last, by
// number, carries no count and reports query.NoTotal; and an empty
// continued page, past a cursor whose remaining rows were deleted,
// reports query.NoTotal as well. None reports More or a cursor. A
// continued page with rows reports the whole listing's total, not the
// rows from the cursor on.
func (s *suite) emptyPages(t *testing.T) {
	dir := s.mkdir(t, "empty-pages-"+t.Name())
	empty, err := s.store.Files.List(s.ctx, s.db, dir.ID, listAll(), firstPage(5))
	if err != nil || len(empty.Items) != 0 || empty.Total != 0 || empty.More || empty.Next != "" {
		t.Errorf("an empty first page = %+v, %v; want no rows, the exact total 0, no More", empty, err)
	}
	ids := []string{
		s.insertFile(t, dir.ID, "a", blobfs.StatusAvailable),
		s.insertFile(t, dir.ID, "b", blobfs.StatusAvailable),
		s.insertFile(t, dir.ID, "c", blobfs.StatusAvailable),
	}
	past, err := s.store.Files.List(s.ctx, s.db, dir.ID, listAll(), query.Page{Number: 3, Size: 5})
	if err != nil || len(past.Items) != 0 || past.Total != query.NoTotal || past.More || past.Next != "" {
		t.Errorf("a page past the last = %+v, %v; want no rows, NoTotal, no More", past, err)
	}
	filtered, err := s.store.Files.List(s.ctx, s.db, dir.ID, query.Directives{Filters: []query.Filter{{Field: "name", Op: query.OpEq, Value: "none"}}}, firstPage(5))
	if err != nil || len(filtered.Items) != 0 || filtered.Total != 0 || filtered.More {
		t.Errorf("an empty first page under a filter = %+v, %v; want no rows and the exact total 0", filtered, err)
	}
	first, err := s.store.Files.List(s.ctx, s.db, dir.ID, listAll(), firstPage(1))
	if err != nil || first.Next == "" {
		t.Fatalf("page 1 at size 1 = %+v, %v; want a cursor", first, err)
	}
	continued, err := s.store.Files.Continue(s.ctx, s.db, dir.ID, listAll(), first.Next, 1)
	if err != nil || len(continued.Items) != 1 || continued.Total != 3 || !continued.More {
		t.Errorf("a continued page = %d rows, total %d, more %v, %v; want one row, the whole listing's total 3, and More", len(continued.Items), continued.Total, continued.More, err)
	}
	s.exec(t, "DELETE FROM blobfs_file WHERE id IN ("+s.db.Dialect().Placeholder(1)+", "+s.db.Dialect().Placeholder(2)+")", ids[1], ids[2])
	gone, err := s.store.Files.Continue(s.ctx, s.db, dir.ID, listAll(), first.Next, 1)
	if err != nil || len(gone.Items) != 0 || gone.Total != query.NoTotal || gone.More || gone.Next != "" {
		t.Errorf("an empty continued page = %+v, %v; want no rows, NoTotal, no More", gone, err)
	}
}

// totalUnderConcurrentInserts checks that a page's total agrees with the
// rows of the same statement while another connection inserts between
// calls. On the pool each call sees its own snapshot. Inside a read-only
// repeatable-read transaction, an insert committed between two pages
// changes neither the total nor the rows: both pages come from the
// transaction's snapshot, and the row inserted meanwhile appears, with
// the larger total, only to a listing after it.
func (s *suite) totalUnderConcurrentInserts(t *testing.T) {
	dir := s.mkdir(t, "busy-"+t.Name()).ID
	for _, n := range []string{"a", "b", "c"} {
		s.insertFile(t, dir, n, blobfs.StatusAvailable)
	}
	list := func(sess sqlate.Session, number, size int) query.Collection[blobfs.File] {
		t.Helper()
		c, err := s.store.Files.List(s.ctx, sess, dir, listAll(), query.Page{Number: number, Size: size})
		if err != nil {
			t.Fatalf("Files.List page %d: %v", number, err)
		}
		return c
	}
	if before := list(s.db, 1, 10); before.Total != 3 || len(before.Items) != 3 {
		t.Fatalf("before the insert: total %d, %d rows; want 3 and 3", before.Total, len(before.Items))
	}
	s.insertFile(t, dir, "d", blobfs.StatusAvailable)
	if after := list(s.db, 1, 10); after.Total != 4 || len(after.Items) != 4 {
		t.Errorf("after the insert: total %d, %d rows; want 4 and 4", after.Total, len(after.Items))
	}
	tx := s.beginTx(t, sqlate.ReadOnly(), sqlate.Isolation(sql.LevelRepeatableRead))
	defer func() { _ = tx.Rollback() }()
	first := list(tx, 1, 2)
	s.insertFile(t, dir, "aa", blobfs.StatusAvailable)
	second := list(tx, 2, 2)
	if first.Total != 4 || second.Total != 4 || len(first.Items) != 2 || len(second.Items) != 2 {
		t.Errorf("under repeatable read: totals %d and %d, rows %d and %d; want 4, 4, 2, 2", first.Total, second.Total, len(first.Items), len(second.Items))
	}
	var names []string
	for _, f := range append(first.Items, second.Items...) {
		names = append(names, f.Name)
	}
	if !slices.Equal(names, []string{"a", "b", "c", "d"}) {
		t.Errorf("the two pages under one snapshot = %v, want a b c d, the row inserted meanwhile excluded", names)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if final := list(s.db, 1, 10); final.Total != 5 || len(final.Items) != 5 || final.Items[1].Name != "aa" {
		t.Errorf("after the transaction: total %d, %d rows; want 5 and 5 with aa second", final.Total, len(final.Items))
	}
}
