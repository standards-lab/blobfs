package data_test

import (
	"context"
	"database/sql/driver"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/standards-lab/sqlate/query"
	"github.com/standards-lab/sqlate/sqltest"

	"github.com/standards-lab/blobfs"
)

// filesIn scripts one page of available file rows in directory, one per
// name, with ids derived from the names. A test under query.TotalExact
// wraps it with sqltest.WithTotal; a TotalNone page scripts it unwrapped.
func filesIn(directory string, names ...string) sqltest.Response {
	now := time.Now()
	r := sqltest.Response{Columns: fileColumns}
	for _, n := range names {
		id := "id-" + n
		r.Rows = append(r.Rows, []driver.Value{id, directory, n, string(blobfs.StatusAvailable), id + "/" + n, int64(4), "text/plain", "etag", int64(2), now, now})
	}
	return r
}

// fileNames returns the names of a page's files, in order.
func fileNames(c query.Collection[blobfs.File]) []string {
	var out []string
	for _, f := range c.Items {
		out = append(out, f.Name)
	}
	return out
}

// fileBase is the file listing's base as the plain, uncounted page wraps
// it, anchored on its directory by the first placeholder.
const fileBase = "FROM blobfs_file f\nWHERE f.directory_id = CAST($1 AS uuid)) q"

// fileCounted is the file listing's base as a counted page wraps it: the
// plain base inside the window that counts the rows under the listing's
// filters, itself re-aliased as q for the keyset predicate, the order, and
// the paging outside it. A listing's own filters, if any, close the inner
// layer before the count's closing parenthesis.
const fileCounted = "SELECT * FROM (SELECT q.*, COUNT(*) OVER () AS sqlate_total FROM (SELECT f.id, f.directory_id, f.name, f.status, f.key, f.size, f.content_type, f.etag, f.version, f.created_at, f.updated_at\n" + fileBase

// TestListFiles proves List and Continue over the files of one directory:
// a status filter bound as text, the total counted in the page's own
// statement over the base anchored on the directory, the name tie-breaker
// after a sort by updated_at, and the continuation past the cursor's two
// keyed values under the same filter.
func TestListFiles(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback,
		sqltest.WithTotal(filesIn(parentID, "a", "b", "c"), 3),
		sqltest.WithTotal(filesIn(parentID, "c"), 3),
	)
	req := query.Directives{
		Filters: []query.Filter{{Field: "status", Op: query.OpEq, Value: string(blobfs.StatusAvailable)}},
		Sort:    []query.Sort{{Field: "updated_at"}},
	}
	first, err := s.Files.List(ctx, db, parentID, req, query.Page{Number: 1, Size: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := fileNames(first); !slices.Equal(got, []string{"a", "b"}) || first.Total != 3 || !first.More || first.Next == "" {
		t.Errorf("page 1 = %v total %d more %v, want [a b] of 3 with a cursor", got, first.Total, first.More)
	}
	next, err := s.Files.Continue(ctx, db, parentID, req, first.Next, 2)
	if err != nil {
		t.Fatalf("Continue: %v", err)
	}
	if got := fileNames(next); !slices.Equal(got, []string{"c"}) || next.Total != 3 || next.More || next.Next != "" {
		t.Errorf("continued page = %v total %d more %v, want [c] of 3 and nothing more", got, next.Total, next.More)
	}

	calls := queries(rec)
	if len(calls) != 2 {
		t.Fatalf("ran %d queries, want one per page", len(calls))
	}
	filter := " WHERE q.status = CAST($2 AS text)"
	want := fileCounted + filter + ") q ORDER BY q.updated_at, q.name OFFSET $3 ROWS FETCH NEXT $4 ROWS ONLY"
	if calls[0].SQL != want || !slices.Equal(calls[0].Args, []any{parentID, "available", 0, 3}) {
		t.Errorf("page = %q %v, want %q", calls[0].SQL, calls[0].Args, want)
	}
	want = fileCounted + filter + ") q WHERE (q.updated_at > CAST($3 AS timestamp with time zone) OR (q.updated_at = CAST($3 AS timestamp with time zone) AND q.name > CAST($4 AS text)))" +
		" ORDER BY q.updated_at, q.name OFFSET $5 ROWS FETCH NEXT $6 ROWS ONLY"
	if calls[1].SQL != want {
		t.Errorf("continued page = %q, want %q", calls[1].SQL, want)
	}
	if args := calls[1].Args; len(args) != 6 || args[0] != parentID || args[3] != "b" {
		t.Errorf("continued page args = %v, want the directory, the filter, the keyed values past b, and the paging", args)
	}
}

// TestListFilesEmptyPage proves the total an empty page carries: an empty
// first page, no row under the filters at all, reports a total of 0,
// while an empty later page, one whose offset ran past the end, carries
// no count column to read and reports query.NoTotal.
func TestListFilesEmptyPage(t *testing.T) {
	ctx := context.Background()
	s, db, _ := openStore(t, fallback, sqltest.WithTotal(filesIn(parentID), 0))
	empty, err := s.Files.List(ctx, db, parentID, query.Directives{}, query.Page{Number: 1, Size: 2})
	if err != nil {
		t.Fatalf("List page 1: %v", err)
	}
	if len(empty.Items) != 0 || empty.More || empty.Total != 0 {
		t.Errorf("empty first page = %+v, want no items, no more, and a total of 0", empty)
	}

	s, db, _ = openStore(t, fallback, sqltest.WithTotal(filesIn(parentID), 0))
	past, err := s.Files.List(ctx, db, parentID, query.Directives{}, query.Page{Number: 2, Size: 2})
	if err != nil {
		t.Fatalf("List page 2: %v", err)
	}
	if len(past.Items) != 0 || past.More || past.Total != query.NoTotal {
		t.Errorf("page past the end = %+v, want no items, no more, and NoTotal", past)
	}
}

// TestFilesNullableSort proves a sort by size or etag, null until a write
// completes, pages by number with More but carries no cursor.
func TestFilesNullableSort(t *testing.T) {
	for _, field := range []string{"size", "etag"} {
		t.Run(field, func(t *testing.T) {
			s, db, _ := openStore(t, fallback, filesIn(parentID, "a", "b", "c"))
			req := query.Directives{Sort: []query.Sort{{Field: field}}, Total: query.TotalNone}
			page, err := s.Files.List(context.Background(), db, parentID, req, query.Page{Number: 1, Size: 2})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if !page.More || page.Next != "" || page.Total != query.NoTotal {
				t.Errorf("page = more %v next %q total %d, want More, no cursor, NoTotal", page.More, page.Next, page.Total)
			}
		})
	}
}

// TestListFilesRefusesUndeclaredFields proves the file listing's contract:
// the object key, which is not declared, is refused as a filter and as a
// sort before any SQL.
func TestListFilesRefusesUndeclaredFields(t *testing.T) {
	s, db, rec := openStore(t, fallback)
	for _, req := range []query.Directives{
		{Sort: []query.Sort{{Field: "key"}}},
		{Filters: []query.Filter{{Field: "key", Op: query.OpLike, Value: "x%"}}},
	} {
		_, err := s.Files.List(context.Background(), db, parentID, req, query.Page{Number: 1, Size: 10})
		var unknown *query.UnknownFieldError
		if !errors.As(err, &unknown) || unknown.Field != "key" {
			t.Errorf("List(%+v) = %v, want an UnknownFieldError for key", req, err)
		}
	}
	if len(rec.Calls()) != 0 {
		t.Error("refused requests ran SQL")
	}
}
