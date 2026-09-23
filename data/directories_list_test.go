package data_test

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/standards-lab/sqlate/query"
	"github.com/standards-lab/sqlate/sqltest"

	"github.com/standards-lab/blobfs"
)

// A parent that is not the root, for the listings under one.
const parentID = "0199a0b0-0000-7000-8000-000000000001"

// count scripts the count a listing runs before its page under
// query.TotalExact.
func count(n int64) sqltest.Response {
	return sqltest.Response{Columns: []string{"count"}, Rows: [][]driver.Value{{n}}}
}

// children scripts one page of directory rows under parent, one per name,
// with ids derived from the names so a test can tell them apart.
func children(parent string, names ...string) sqltest.Response {
	now := time.Now()
	r := sqltest.Response{Columns: directoryColumns}
	for _, n := range names {
		r.Rows = append(r.Rows, []driver.Value{"id-" + n, parent, n, int64(1), now, now})
	}
	return r
}

// directoryNames returns the names of a page's directories, in order.
func directoryNames(c query.Collection[blobfs.Directory]) []string {
	var out []string
	for _, d := range c.Items {
		out = append(out, d.Name)
	}
	return out
}

// queries returns the recorder's query calls, in order.
func queries(rec *sqltest.Recorder) []sqltest.Call {
	var out []sqltest.Call
	for _, c := range rec.Calls() {
		if c.Op == sqltest.OpQuery {
			out = append(out, c)
		}
	}
	return out
}

// directoryBase is the listing's base as the page wraps it, anchored on
// its parent by the first placeholder.
const directoryBase = "FROM blobfs_directory d\nWHERE d.parent_id = CAST($1 AS uuid)) q"

// TestListDirectories proves List by page number: the count under the
// filters, then the page, both over the base anchored on the parent,
// sorted by the caller's terms with name appended as the tie-breaker, and
// fetching one row past the page to report More. The first page with More
// carries a cursor; the last page reports no More and no cursor. Under
// TotalNone the count does not run and the total is NoTotal.
func TestListDirectories(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback,
		count(3), children(parentID, "a", "b", "c"),
		count(3), children(parentID, "c"),
		children(parentID, "a", "b", "c"),
	)
	req := query.Directives{Filters: []query.Filter{{Field: "version", Op: query.OpGe, Value: 1}}}

	first, err := s.Directories.List(ctx, db, parentID, req, query.Page{Number: 1, Size: 2})
	if err != nil {
		t.Fatalf("List page 1: %v", err)
	}
	if got := directoryNames(first); !slices.Equal(got, []string{"a", "b"}) || first.Total != 3 || !first.More || first.Next == "" {
		t.Errorf("page 1 = %v total %d more %v next %q, want [a b] of 3 with more and a cursor", got, first.Total, first.More, first.Next)
	}
	last, err := s.Directories.List(ctx, db, parentID, req, query.Page{Number: 2, Size: 2})
	if err != nil {
		t.Fatalf("List page 2: %v", err)
	}
	if got := directoryNames(last); !slices.Equal(got, []string{"c"}) || last.Total != 3 || last.More || last.Next != "" {
		t.Errorf("page 2 = %v total %d more %v next %q, want [c] of 3, no more, no cursor", got, last.Total, last.More, last.Next)
	}
	untotalled, err := s.Directories.List(ctx, db, parentID, query.Directives{Total: query.TotalNone}, query.Page{Number: 1, Size: 2})
	if err != nil {
		t.Fatalf("List under TotalNone: %v", err)
	}
	if untotalled.Total != query.NoTotal || !untotalled.More || len(untotalled.Items) != 2 {
		t.Errorf("TotalNone page = %+v, want two rows, NoTotal, and More", untotalled)
	}

	calls := queries(rec)
	if len(calls) != 5 {
		t.Fatalf("ran %d queries, want count and page twice, then one page", len(calls))
	}
	wantCount := "SELECT COUNT(*) FROM (SELECT d.id, d.parent_id, d.name, d.version, d.created_at, d.updated_at\n" + directoryBase + " WHERE q.version >= CAST($2 AS bigint)"
	if calls[0].SQL != wantCount || !slices.Equal(calls[0].Args, []any{parentID, 1}) {
		t.Errorf("count = %q %v, want %q over the parent and the filter", calls[0].SQL, calls[0].Args, wantCount)
	}
	wantPage := directoryBase + " WHERE q.version >= CAST($2 AS bigint) ORDER BY q.name OFFSET $3 ROWS FETCH NEXT $4 ROWS ONLY"
	if !strings.HasSuffix(calls[1].SQL, wantPage) || !slices.Equal(calls[1].Args, []any{parentID, 1, 0, 3}) {
		t.Errorf("page 1 = %q %v, want the suffix %q at offset 0 fetching 3", calls[1].SQL, calls[1].Args, wantPage)
	}
	if !slices.Equal(calls[3].Args, []any{parentID, 1, 2, 3}) {
		t.Errorf("page 2 args = %v, want offset 2 fetching 3", calls[3].Args)
	}
	if strings.Contains(calls[4].SQL, "COUNT") || !slices.Equal(calls[4].Args, []any{parentID, 0, 3}) {
		t.Errorf("TotalNone page = %q %v, want the page alone", calls[4].SQL, calls[4].Args)
	}
}

// TestListRoot proves the listing of the root is the listing anchored on
// blobfs.RootID: the depth-one directories, whose parent is the root. The
// root itself has no parent, so the base never returns it.
func TestListRoot(t *testing.T) {
	s, db, rec := openStore(t, fallback, count(1), children(blobfs.RootID, "docs"))
	c, err := s.Directories.List(context.Background(), db, blobfs.RootID, query.Directives{}, query.Page{Number: 1, Size: 10})
	if err != nil {
		t.Fatalf("List(root): %v", err)
	}
	if len(c.Items) != 1 || c.Items[0].ParentID == nil || *c.Items[0].ParentID != blobfs.RootID || c.Total != 1 || c.More || c.Next != "" {
		t.Errorf("List(root) = %+v, want the one depth-one directory", c)
	}
	for _, call := range queries(rec) {
		if call.Args[0] != blobfs.RootID {
			t.Errorf("%q bound %v first, want the root's id", call.SQL, call.Args[0])
		}
	}
}

// TestContinueDirectories proves Continue walks the order List began: the
// page past the cursor's name, by the keyset predicate in place of an
// offset, with the same total a first page reports, and a cursor of its
// own while rows remain. A descending sort continues descending.
func TestContinueDirectories(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback,
		count(5), children(parentID, "a", "b", "c"),
		count(5), children(parentID, "c", "d", "e"),
		children(parentID, "e", "d", "c"),
		children(parentID, "c", "b"),
	)
	first, err := s.Directories.List(ctx, db, parentID, query.Directives{}, query.Page{Number: 1, Size: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	next, err := s.Directories.Continue(ctx, db, parentID, query.Directives{}, first.Next, 2)
	if err != nil {
		t.Fatalf("Continue: %v", err)
	}
	if got := directoryNames(next); !slices.Equal(got, []string{"c", "d"}) || next.Total != 5 || !next.More || next.Next == "" || next.Next == first.Next {
		t.Errorf("continued page = %v total %d more %v, want [c d] of 5 with a cursor of its own", got, next.Total, next.More)
	}
	calls := queries(rec)
	want := directoryBase + " WHERE (q.name > CAST($2 AS text)) ORDER BY q.name OFFSET $3 ROWS FETCH NEXT $4 ROWS ONLY"
	if !strings.HasSuffix(calls[3].SQL, want) || !slices.Equal(calls[3].Args, []any{parentID, "b", 0, 3}) {
		t.Errorf("continued page = %q %v, want the suffix %q past b", calls[3].SQL, calls[3].Args, want)
	}

	desc := query.Directives{Sort: []query.Sort{{Field: "name", Descending: true}}, Total: query.TotalNone}
	down, err := s.Directories.List(ctx, db, parentID, desc, query.Page{Number: 1, Size: 2})
	if err != nil {
		t.Fatalf("List descending: %v", err)
	}
	if _, err := s.Directories.Continue(ctx, db, parentID, desc, down.Next, 2); err != nil {
		t.Fatalf("Continue descending: %v", err)
	}
	calls = queries(rec)
	want = " WHERE (q.name < CAST($2 AS text)) ORDER BY q.name DESC OFFSET $3 ROWS FETCH NEXT $4 ROWS ONLY"
	if !strings.HasSuffix(calls[5].SQL, want) || calls[5].Args[1] != "d" {
		t.Errorf("descending continuation = %q %v, want the suffix %q past d", calls[5].SQL, calls[5].Args, want)
	}
}

// TestListDirectoriesRefusesDirectives proves a request naming a field the
// base does not declare, a page below 1, and an empty cursor are refused
// by the query library before any SQL, each unwrapping to ErrDirectives
// and the unknown field reachable as an UnknownFieldError.
func TestListDirectoriesRefusesDirectives(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback)
	for _, c := range []struct {
		name string
		req  query.Directives
		use  query.FieldUse
	}{
		{"sort", query.Directives{Sort: []query.Sort{{Field: "path"}}}, query.FieldUseSort},
		{"filter", query.Directives{Filters: []query.Filter{{Field: "key", Op: query.OpEq, Value: "x"}}}, query.FieldUseFilter},
	} {
		_, err := s.Directories.List(ctx, db, parentID, c.req, query.Page{Number: 1, Size: 10})
		var unknown *query.UnknownFieldError
		if !errors.As(err, &unknown) || unknown.Use != c.use || !errors.Is(err, query.ErrDirectives) {
			t.Errorf("%s on an undeclared field = %v, want an UnknownFieldError for its %s", c.name, err, c.use)
		}
	}
	if _, err := s.Directories.List(ctx, db, parentID, query.Directives{}, query.Page{Number: 0, Size: 10}); !errors.Is(err, query.ErrDirectives) {
		t.Errorf("page 0 = %v, want ErrDirectives", err)
	}
	if _, err := s.Directories.Continue(ctx, db, parentID, query.Directives{}, "", 10); !errors.Is(err, query.ErrDirectives) {
		t.Errorf("Continue without a cursor = %v, want ErrDirectives", err)
	}
	if n := len(rec.Calls()); n != 0 {
		t.Errorf("refused requests ran %d calls, want none", n)
	}
}

// TestDirectoriesNonContinuableSort proves a sort that cannot be continued,
// one whose terms mix directions or name the nullable parent_id, still
// pages by number and reports More, but carries no cursor, and Continue
// under it is refused as unsupported before any SQL.
func TestDirectoriesNonContinuableSort(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name string
		sort []query.Sort
	}{
		{"mixed", []query.Sort{{Field: "created_at"}, {Field: "name", Descending: true}}},
		{"nullable", []query.Sort{{Field: "parent_id"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, db, rec := openStore(t, fallback, children(parentID, "a", "b", "c"), children(parentID, "a", "b", "c"))
			req := query.Directives{Sort: c.sort, Total: query.TotalNone}
			page, err := s.Directories.List(ctx, db, parentID, req, query.Page{Number: 1, Size: 2})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if !page.More || page.Next != "" {
				t.Errorf("page = more %v next %q, want More without a cursor", page.More, page.Next)
			}
			byName, err := s.Directories.List(ctx, db, parentID, query.Directives{Total: query.TotalNone}, query.Page{Number: 1, Size: 2})
			if err != nil || byName.Next == "" {
				t.Fatalf("List by name = %v, %v, want a cursor", byName, err)
			}
			calls := len(rec.Calls())
			_, err = s.Directories.Continue(ctx, db, parentID, req, byName.Next, 2)
			var cur *query.CursorError
			if !errors.As(err, &cur) || cur.Reason != query.CursorUnsupported {
				t.Errorf("Continue under %s = %v, want CursorUnsupported", c.name, err)
			}
			if len(rec.Calls()) != calls {
				t.Error("the refused Continue ran SQL")
			}
		})
	}
}

// editCursor decodes a cursor, replaces old with new in its body, and
// encodes it again with its check prefix unchanged, as a caller who edits
// a cursor's position would.
func editCursor(t *testing.T, c query.Cursor, old, new string) query.Cursor {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(string(c))
	if err != nil || !bytes.Contains(raw, []byte(old)) {
		t.Fatalf("cursor %q does not decode to a body holding %s: %v", c, old, err)
	}
	return query.Cursor(base64.RawURLEncoding.EncodeToString(bytes.Replace(raw, []byte(old), []byte(new), 1)))
}

// TestDirectoriesCursorRefusals proves a cursor is refused before any SQL
// when its position was edited, when it comes from the file listing, and when it is
// relayed under other filters or another sort than the page that issued
// it.
func TestDirectoriesCursorRefusals(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback,
		children(parentID, "a", "b", "c"),
		filesIn(parentID, "a", "b", "c"),
	)
	none := query.Directives{Total: query.TotalNone}
	dirs, err := s.Directories.List(ctx, db, parentID, none, query.Page{Number: 1, Size: 2})
	if err != nil {
		t.Fatalf("List directories: %v", err)
	}
	files, err := s.Files.List(ctx, db, parentID, none, query.Page{Number: 1, Size: 2})
	if err != nil {
		t.Fatalf("List files: %v", err)
	}
	edited := editCursor(t, dirs.Next, `"b"`, `"z"`)
	filtered := query.Directives{Total: query.TotalNone, Filters: []query.Filter{{Field: "version", Op: query.OpEq, Value: 1}}}
	for _, c := range []struct {
		name   string
		req    query.Directives
		after  query.Cursor
		reason query.CursorReason
	}{
		{"edited", none, query.Cursor(edited), query.CursorMalformed},
		{"garbage", none, "not a cursor", query.CursorMalformed},
		{"file listing", none, files.Next, query.CursorMismatch},
		{"other filters", filtered, dirs.Next, query.CursorMismatch},
		{"other sort", query.Directives{Sort: []query.Sort{{Field: "created_at"}}}, dirs.Next, query.CursorMismatch},
		{"other direction", query.Directives{Sort: []query.Sort{{Field: "name", Descending: true}}}, dirs.Next, query.CursorMismatch},
	} {
		_, err := s.Directories.Continue(ctx, db, parentID, c.req, c.after, 2)
		var cur *query.CursorError
		if !errors.As(err, &cur) || cur.Reason != c.reason || !errors.Is(err, query.ErrDirectives) {
			t.Errorf("%s cursor = %v, want CursorError %s", c.name, err, c.reason)
		}
	}
	if n := len(queries(rec)); n != 2 {
		t.Errorf("ran %d queries, want only the two issuing pages", n)
	}
}
