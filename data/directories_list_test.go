package data_test

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/base64"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/standards-lab/sqlate/query"
	"github.com/standards-lab/sqlate/sqltest"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data"
)

// A parent that is not the root, for the listings under one.
const parentID = "0199a0b0-0000-7000-8000-000000000001"

// children scripts one page of directory rows under parent, one per name,
// with ids derived from the names so a test can tell them apart. A test
// under query.TotalExact wraps it with sqltest.WithTotal; a TotalNone page
// scripts it unwrapped.
func children(parent string, names ...string) sqltest.Response {
	now := time.Now()
	r := sqltest.Response{Columns: directoryColumns}
	for _, n := range names {
		r.Rows = append(r.Rows, []driver.Value{"id-" + n, parent, n, "active", int64(1), now, now})
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

// listed scripts the read of the listed directory that a listing without
// IncludeDeleting runs after its page: parentID, active.
func listed() sqltest.Response {
	return directoryResponse(parentID, blobfs.RootID, "parent", 1)
}

// directoryRead is the text of that read, directory_by_id.
const directoryRead = "SELECT d.id, d.parent_id, d.name, d.status, d.version, d.created_at, d.updated_at\nFROM blobfs_directory d\nWHERE d.id = CAST($1 AS uuid)"

// pages returns the recorder's query calls without the reads of the
// listed directory, failing the test unless each page is followed by one,
// bound to the page's own anchor.
func pages(t *testing.T, rec *sqltest.Recorder) []sqltest.Call {
	t.Helper()
	calls := queries(rec)
	if len(calls)%2 != 0 {
		t.Fatalf("ran %d queries, want each page followed by the read of its directory", len(calls))
	}
	var out []sqltest.Call
	for i := 0; i < len(calls); i += 2 {
		if read := calls[i+1]; read.SQL != directoryRead || read.Args[0] != calls[i].Args[0] {
			t.Errorf("query %d = %q %v, want the read of the listed directory %v", i+1, read.SQL, read.Args, calls[i].Args[0])
		}
		out = append(out, calls[i])
	}
	return out
}

// hideDeleting is the predicate the listing appends after the caller's
// filters when IncludeDeleting is not given, at placeholder n.
func hideDeleting(n int) string {
	return "q.status <> CAST($" + strconv.Itoa(n) + " AS text)"
}

// directoryBase is the listing's base as the plain, uncounted page wraps
// it, anchored on its parent by the first placeholder.
const directoryBase = "FROM blobfs_directory d\nWHERE d.parent_id = CAST($1 AS uuid)) q"

// directoryCounted is the listing's base as a counted page wraps it: the
// plain base inside the window that counts the rows under the listing's
// filters, itself re-aliased as q for the keyset predicate, the order, and
// the paging outside it. A listing's own filters, if any, close the inner
// layer before the count's closing parenthesis.
const directoryCounted = "SELECT * FROM (SELECT q.*, COUNT(*) OVER () AS sqlate_total FROM (SELECT d.id, d.parent_id, d.name, d.status, d.version, d.created_at, d.updated_at\n" + directoryBase

// TestListDirectories proves List by page number: the total counted in the
// page's own statement, one query per page, over the base anchored on the
// parent, under the caller's filters and then the one that hides
// deleting directories, sorted by the caller's terms with name appended as
// the tie-breaker, and fetching one row past the page to report More, and
// after each page the read of the parent that tells a deleting one. The
// first page with More carries a cursor; the last page reports no More
// and no cursor. Under TotalNone the page carries no count column and the
// total is NoTotal.
func TestListDirectories(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback,
		sqltest.WithTotal(children(parentID, "a", "b", "c"), 3), listed(),
		sqltest.WithTotal(children(parentID, "c"), 3), listed(),
		children(parentID, "a", "b", "c"), listed(),
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

	calls := pages(t, rec)
	if len(calls) != 3 {
		t.Fatalf("ran %d pages, want one per call", len(calls))
	}
	wantPage := directoryCounted + " WHERE q.version >= CAST($2 AS bigint) AND " + hideDeleting(3) + ") q ORDER BY q.name OFFSET $4 ROWS FETCH NEXT $5 ROWS ONLY"
	if calls[0].SQL != wantPage || !slices.Equal(calls[0].Args, []any{parentID, 1, "deleting", 0, 3}) {
		t.Errorf("page 1 = %q %v, want %q at offset 0 fetching 3", calls[0].SQL, calls[0].Args, wantPage)
	}
	if calls[1].SQL != wantPage || !slices.Equal(calls[1].Args, []any{parentID, 1, "deleting", 2, 3}) {
		t.Errorf("page 2 = %q %v, want %q at offset 2 fetching 3", calls[1].SQL, calls[1].Args, wantPage)
	}
	wantUntotalled := directoryBase + " WHERE " + hideDeleting(2) + " ORDER BY q.name OFFSET $3 ROWS FETCH NEXT $4 ROWS ONLY"
	if !strings.HasSuffix(calls[2].SQL, wantUntotalled) || strings.Contains(calls[2].SQL, "COUNT") || !slices.Equal(calls[2].Args, []any{parentID, "deleting", 0, 3}) {
		t.Errorf("TotalNone page = %q %v, want the plain page %q with no count", calls[2].SQL, calls[2].Args, wantUntotalled)
	}
}

// TestListDirectoriesEmptyPage proves the total an empty page carries: an
// empty first page, no row under the filters at all, reports a total of
// 0, while an empty later page, one whose offset ran past the end,
// carries no count column to read and reports query.NoTotal.
func TestListDirectoriesEmptyPage(t *testing.T) {
	ctx := context.Background()
	s, db, _ := openStore(t, fallback, sqltest.WithTotal(children(parentID), 0), listed())
	empty, err := s.Directories.List(ctx, db, parentID, query.Directives{}, query.Page{Number: 1, Size: 2})
	if err != nil {
		t.Fatalf("List page 1: %v", err)
	}
	if len(empty.Items) != 0 || empty.More || empty.Total != 0 {
		t.Errorf("empty first page = %+v, want no items, no more, and a total of 0", empty)
	}

	s, db, _ = openStore(t, fallback, sqltest.WithTotal(children(parentID), 0), listed())
	past, err := s.Directories.List(ctx, db, parentID, query.Directives{}, query.Page{Number: 2, Size: 2})
	if err != nil {
		t.Fatalf("List page 2: %v", err)
	}
	if len(past.Items) != 0 || past.More || past.Total != query.NoTotal {
		t.Errorf("page past the end = %+v, want no items, no more, and NoTotal", past)
	}
}

// TestListRoot proves the listing of the root is the listing anchored on
// blobfs.RootID: the depth-one directories, whose parent is the root. The
// root itself has no parent, so the base never returns it.
func TestListRoot(t *testing.T) {
	s, db, rec := openStore(t, fallback, sqltest.WithTotal(children(blobfs.RootID, "docs"), 1), directoryResponse(blobfs.RootID, "", "/", 1))
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
		sqltest.WithTotal(children(parentID, "a", "b", "c"), 5), listed(),
		sqltest.WithTotal(children(parentID, "c", "d", "e"), 5), listed(),
		children(parentID, "e", "d", "c"), listed(),
		children(parentID, "c", "b"), listed(),
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
	calls := pages(t, rec)
	want := directoryCounted + " WHERE " + hideDeleting(2) + ") q WHERE (q.name > CAST($3 AS text)) ORDER BY q.name OFFSET $4 ROWS FETCH NEXT $5 ROWS ONLY"
	if calls[1].SQL != want || !slices.Equal(calls[1].Args, []any{parentID, "deleting", "b", 0, 3}) {
		t.Errorf("continued page = %q %v, want %q past b", calls[1].SQL, calls[1].Args, want)
	}

	desc := query.Directives{Sort: []query.Sort{{Field: "name", Descending: true}}, Total: query.TotalNone}
	down, err := s.Directories.List(ctx, db, parentID, desc, query.Page{Number: 1, Size: 2})
	if err != nil {
		t.Fatalf("List descending: %v", err)
	}
	if _, err := s.Directories.Continue(ctx, db, parentID, desc, down.Next, 2); err != nil {
		t.Fatalf("Continue descending: %v", err)
	}
	calls = pages(t, rec)
	want = " WHERE " + hideDeleting(2) + " AND (q.name < CAST($3 AS text)) ORDER BY q.name DESC OFFSET $4 ROWS FETCH NEXT $5 ROWS ONLY"
	if !strings.HasSuffix(calls[3].SQL, want) || calls[3].Args[2] != "d" {
		t.Errorf("descending continuation = %q %v, want the suffix %q past d", calls[3].SQL, calls[3].Args, want)
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
			s, db, rec := openStore(t, fallback, children(parentID, "a", "b", "c"), listed(), children(parentID, "a", "b", "c"), listed())
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
// when its position was edited, when it comes from the file listing, and
// when it is relayed under other filters or another sort than the page that
// issued it.
func TestDirectoriesCursorRefusals(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback,
		children(parentID, "a", "b", "c"), listed(),
		filesIn(parentID, "a", "b", "c"), listed(),
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
	if n := len(pages(t, rec)); n != 2 {
		t.Errorf("ran %d pages, want only the two issuing pages", n)
	}
}

// TestListDirectoriesDeleting proves what the listing does with deleting
// rows. Under a parent that is deleting, List and Continue read the page
// and then the parent, and report blobfs.ErrDeleting with no rows; under
// a parent that does not exist the page is the listing, empty, with no
// error. With IncludeDeleting the page composes the caller's filters
// alone, and no read follows it. The hiding filter is part of what a
// cursor is bound to, so a cursor continues only a listing called the
// same way, and the caller's filters are never appended to in place.
func TestListDirectoriesDeleting(t *testing.T) {
	ctx := context.Background()
	none := query.Directives{Total: query.TotalNone}
	deleting := directoryIn(parentID, blobfs.RootID, "parent", blobfs.DirectoryStatusDeleting, 2)
	s, db, rec := openStore(t, fallback,
		children(parentID, "a", "b", "c"), listed(),
		children(parentID, "c"), deleting,
		children(parentID), deleting,
		children(parentID), noDirectory(),
	)
	first, err := s.Directories.List(ctx, db, parentID, none, query.Page{Number: 1, Size: 2})
	if err != nil || first.Next == "" {
		t.Fatalf("List = %+v, %v, want a cursor", first, err)
	}
	c, err := s.Directories.Continue(ctx, db, parentID, none, first.Next, 2)
	if !errors.Is(err, blobfs.ErrDeleting) || len(c.Items) != 0 {
		t.Errorf("Continue under a parent marked since = %+v, %v, want no rows and ErrDeleting", c, err)
	}
	c, err = s.Directories.List(ctx, db, parentID, none, query.Page{Number: 1, Size: 2})
	if !errors.Is(err, blobfs.ErrDeleting) || len(c.Items) != 0 {
		t.Errorf("List under a deleting parent = %+v, %v, want no rows and ErrDeleting", c, err)
	}
	c, err = s.Directories.List(ctx, db, parentID, none, query.Page{Number: 1, Size: 2})
	if err != nil || len(c.Items) != 0 || c.More {
		t.Errorf("List under a missing parent = %+v, %v, want an empty page", c, err)
	}
	if n := len(pages(t, rec)); n != 4 {
		t.Errorf("ran %d pages, want 4", n)
	}

	s, db, rec = openStore(t, fallback,
		sqltest.WithTotal(children(parentID, "a", "b", "c"), 3),
		sqltest.WithTotal(children(parentID, "c"), 3),
	)
	all, err := s.Directories.List(ctx, db, parentID, query.Directives{}, query.Page{Number: 1, Size: 2}, data.IncludeDeleting())
	if err != nil || all.Next == "" {
		t.Fatalf("List with IncludeDeleting = %+v, %v, want a cursor", all, err)
	}
	if _, err := s.Directories.Continue(ctx, db, parentID, query.Directives{}, all.Next, 2, data.IncludeDeleting()); err != nil {
		t.Fatalf("Continue with IncludeDeleting: %v", err)
	}
	calls := queries(rec)
	want := directoryCounted + ") q ORDER BY q.name OFFSET $2 ROWS FETCH NEXT $3 ROWS ONLY"
	if len(calls) != 2 || calls[0].SQL != want || !slices.Equal(calls[0].Args, []any{parentID, 0, 3}) {
		t.Fatalf("with IncludeDeleting ran %v, want the page %q alone, then its continuation", calls, want)
	}
	for _, c := range []struct {
		name  string
		after query.Cursor
		opts  []data.ListOption
	}{
		{"a cursor issued with IncludeDeleting, continued without it", all.Next, nil},
		{"a cursor issued without IncludeDeleting, continued with it", first.Next, []data.ListOption{data.IncludeDeleting()}},
	} {
		_, err := s.Directories.Continue(ctx, db, parentID, none, c.after, 2, c.opts...)
		var cur *query.CursorError
		if !errors.As(err, &cur) || cur.Reason != query.CursorMismatch {
			t.Errorf("%s = %v, want CursorMismatch", c.name, err)
		}
	}
	if n := len(rec.Calls()); n != 2 {
		t.Errorf("the refused cursors ran SQL: %d calls", n)
	}

	s, db, _ = openStore(t, fallback, children(parentID, "a"), listed())
	filters := make([]query.Filter, 1, 2)
	filters[0] = query.Filter{Field: "version", Op: query.OpGe, Value: 1}
	if _, err := s.Directories.List(ctx, db, parentID, query.Directives{Filters: filters, Total: query.TotalNone}, query.Page{Number: 1, Size: 2}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if spare := filters[:2][1]; spare != (query.Filter{}) {
		t.Errorf("the listing appended %+v to the caller's filters in place", spare)
	}
}
