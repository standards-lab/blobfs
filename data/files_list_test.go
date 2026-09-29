package data_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/standards-lab/sqlate/query"
	"github.com/standards-lab/sqlate/sqltest"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data"
	"github.com/standards-lab/blobfs/data/datatest"
)

// filesIn scripts one page of available file rows in directory, one per
// name, with ids derived from the names. A test under query.TotalExact
// wraps it with sqltest.WithTotal; a TotalNone page scripts it unwrapped.
func filesIn(directory string, names ...string) sqltest.Response {
	size, etag := int64(4), "etag"
	var rows []blobfs.File
	for _, n := range names {
		f := fileRow("id-"+n, directory, n, blobfs.StatusAvailable, 2)
		f.Size, f.ETag = &size, &etag
		rows = append(rows, f)
	}
	return datatest.FileRows(rows...)
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

// TestListFiles checks List and Continue over one directory's files: the
// filters, the count, the order, the cursor, and the directory's read.
func TestListFiles(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback,
		sqltest.WithTotal(filesIn(parentID, "a", "b", "c"), 3), listed(),
		sqltest.WithTotal(filesIn(parentID, "c"), 3), listed(),
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

	calls := pages(t, rec)
	if len(calls) != 2 {
		t.Fatalf("ran %d pages, want one per call", len(calls))
	}
	filter := " WHERE q.status = CAST($2 AS text) AND " + hideDeleting(3)
	want := fileCounted + filter + ") q ORDER BY q.updated_at, q.name OFFSET $4 ROWS FETCH NEXT $5 ROWS ONLY"
	if calls[0].SQL != want || !slices.Equal(calls[0].Args, []any{parentID, "available", "deleting", 0, 3}) {
		t.Errorf("page = %q %v, want %q", calls[0].SQL, calls[0].Args, want)
	}
	want = fileCounted + filter + ") q WHERE (q.updated_at > CAST($4 AS timestamp with time zone) OR (q.updated_at = CAST($4 AS timestamp with time zone) AND q.name > CAST($5 AS text)))" +
		" ORDER BY q.updated_at, q.name OFFSET $6 ROWS FETCH NEXT $7 ROWS ONLY"
	if calls[1].SQL != want {
		t.Errorf("continued page = %q, want %q", calls[1].SQL, want)
	}
	if args := calls[1].Args; len(args) != 7 || args[0] != parentID || args[4] != "b" {
		t.Errorf("continued page args = %v, want the directory, the filter, the keyed values past b, and the paging", args)
	}
}

// TestListFilesEmptyPage checks an empty first page reports 0 and an empty
// later page reports query.NoTotal.
func TestListFilesEmptyPage(t *testing.T) {
	ctx := context.Background()
	s, db, _ := openStore(t, fallback, sqltest.WithTotal(filesIn(parentID), 0), listed())
	empty, err := s.Files.List(ctx, db, parentID, query.Directives{}, query.Page{Number: 1, Size: 2})
	if err != nil {
		t.Fatalf("List page 1: %v", err)
	}
	if len(empty.Items) != 0 || empty.More || empty.Total != 0 {
		t.Errorf("empty first page = %+v, want no items, no more, and a total of 0", empty)
	}

	s, db, _ = openStore(t, fallback, sqltest.WithTotal(filesIn(parentID), 0), listed())
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
			s, db, _ := openStore(t, fallback, filesIn(parentID, "a", "b", "c"), listed())
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

// TestListFilesRefusesUndeclaredFields checks the object key is refused as
// a filter and a sort before any SQL.
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

// TestListFilesDeleting checks the hiding filter, the refusal of a
// deleting directory, and IncludeDeleting.
func TestListFilesDeleting(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback,
		filesIn(parentID), directoryIn(parentID, blobfs.RootID, "parent", blobfs.DirectoryStatusDeleting, 2),
		filesIn(parentID, "a"),
	)
	none := query.Directives{Total: query.TotalNone}
	c, err := s.Files.List(ctx, db, parentID, none, query.Page{Number: 1, Size: 2})
	if !errors.Is(err, blobfs.ErrDeleting) || len(c.Items) != 0 {
		t.Errorf("List in a deleting directory = %+v, %v, want no rows and ErrDeleting", c, err)
	}
	req := query.Directives{Total: query.TotalNone, Filters: []query.Filter{{Field: "status", Op: query.OpEq, Value: string(blobfs.StatusDeleting)}}}
	if _, err := s.Files.List(ctx, db, parentID, req, query.Page{Number: 1, Size: 2}, data.IncludeDeleting()); err != nil {
		t.Fatalf("List with IncludeDeleting: %v", err)
	}
	calls := queries(rec)
	want := fileBase + " WHERE q.status = CAST($2 AS text) ORDER BY q.name OFFSET $3 ROWS FETCH NEXT $4 ROWS ONLY"
	if len(calls) != 3 || !strings.HasSuffix(calls[2].SQL, want) || !slices.Equal(calls[2].Args, []any{parentID, "deleting", 0, 3}) {
		t.Errorf("with IncludeDeleting ran %v, want the page %q alone", calls[2:], want)
	}
}
