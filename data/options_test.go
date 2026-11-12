package data_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"
	"github.com/standards-lab/sqlate/sqltest"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data"
)

// TestCreateOptionsMints checks an empty CreateOptions.ID mints a fresh
// canonical id for each insert, for directories and files alike.
func TestCreateOptionsMints(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single, childResponse("A", "a"), childResponse("B", "b"))
	for _, n := range []string{"a", "b"} {
		if _, err := s.Directories.Create(ctx, db, blobfs.RootID, n, data.CreateOptions{}); err != nil {
			t.Fatalf("Create(%q) with no id: %v", n, err)
		}
	}
	s, db, frec := openStore(t, single, protocolRow(blobfs.StatusPending, 1))
	if _, err := s.Files.Create(ctx, db, accepting{}, protocolDir, "report.txt", "text/plain", data.CreateOptions{}); err != nil {
		t.Fatalf("Files.Create with no id: %v", err)
	}
	calls := append(rec.Calls(), frec.Calls()...)
	var minted []any
	for _, call := range calls {
		minted = append(minted, call.Args[0])
	}
	for _, id := range minted {
		s, ok := id.(string)
		if parsed, err := blobfs.ParseID(s); !ok || err != nil || parsed != s {
			t.Errorf("the insert bound %v, want a minted id in canonical form", id)
		}
	}
	if len(minted) != 3 || minted[0] == minted[1] {
		t.Errorf("the inserts bound %v, want a fresh id each", minted)
	}
}

// TestCreateOptionsEnsureFoundKeepsItsID checks a row an Ensure finds is
// returned under its own id, not the one CreateOptions supplied.
func TestCreateOptionsEnsureFoundKeepsItsID(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single, childResponse("D", "docs"))
	d, created, err := s.Directories.Ensure(ctx, db, blobfs.RootID, "docs", data.CreateOptions{ID: blobfs.NewID()})
	if err != nil || created || d.ID != "D" {
		t.Errorf("Ensure of a held name = %+v, %v, %v; want the row found under its own id", d, created, err)
	}
	if got := ops(rec); got != "query" {
		t.Errorf("ops = %q, want the lookup alone", got)
	}

	s, db, rec = openStore(t, single, protocolRow(blobfs.StatusAvailable, 2))
	f, outcome, err := s.Files.Ensure(ctx, db, accepting{}, protocolDir, "report.txt", "text/plain", data.CreateOptions{ID: blobfs.NewID()})
	if err != nil || outcome != data.WritePresent || f.ID != protocolFile {
		t.Errorf("Files.Ensure of a held name = %+v, %v, %v; want the row found under its own id", f, outcome, err)
	}
	if got := ops(rec); got != "query" {
		t.Errorf("ops = %q, want the lookup alone", got)
	}
}

// TestVersionOptions checks each guarded call binds no version for the
// zero VersionOptions, which guards nothing, and binds a set Version.
func TestVersionOptions(t *testing.T) {
	ctx := context.Background()
	objects := newObjectStore()
	for _, c := range []struct {
		name string
		run  func(*data.Store, *sqlate.DB, data.VersionOptions)
	}{
		{"Files.Hold", func(s *data.Store, db *sqlate.DB, o data.VersionOptions) {
			_ = s.Files.Hold(ctx, begin(t, db), "F", o)
		}},
		{"Files.Delete", func(s *data.Store, db *sqlate.DB, o data.VersionOptions) {
			_, _ = s.Files.Delete(ctx, begin(t, db), "F", o)
		}},
		{"Directories.Delete", func(s *data.Store, db *sqlate.DB, o data.VersionOptions) {
			_ = s.Directories.Delete(ctx, db, "D", o)
		}},
		{"Directories.MarkDeleting", func(s *data.Store, db *sqlate.DB, o data.VersionOptions) {
			_, _ = s.Directories.MarkDeleting(ctx, begin(t, db), "D", o)
		}},
		{"Store.RemoveFileID", func(s *data.Store, db *sqlate.DB, o data.VersionOptions) {
			_ = s.RemoveFileID(ctx, db, objects, "F", o)
		}},
		{"Store.RemoveFile", func(s *data.Store, db *sqlate.DB, o data.VersionOptions) {
			_ = s.RemoveFile(ctx, db, objects, func(*sqlate.Tx) (string, error) { return "F", nil }, data.RemoveOptions{Version: o.Version})
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, db, rec := openStore(t, single)
			c.run(s, db, data.VersionOptions{Version: 7})
			guarded := slices.IndexFunc(rec.Calls(), func(call sqltest.Call) bool { return slices.Contains(call.Args, any(int64(7))) })
			if guarded < 0 {
				t.Fatalf("the call at version 7 ran %v, want the version bound", rec.Calls())
			}

			s, db, rec = openStore(t, single)
			c.run(s, db, data.VersionOptions{})
			calls := rec.Calls()
			if len(calls) <= guarded {
				t.Fatalf("the unguarded call ran %v, want the guarded statement", calls)
			}
			if got := calls[guarded].Args; !slices.Contains(got, nil) || slices.ContainsFunc(got, func(a any) bool { _, ok := a.(int64); return ok }) {
				t.Errorf("the unguarded call bound %v, want no version", got)
			}
		})
	}
}

// listingCase is one of the two listings, reached through the store's
// exported handle as a data.Listing, with the scripted page of its rows
// and the scripted page of the other listing's rows.
type listingCase[T any] struct {
	name  string
	of    func(*data.Store) data.Listing[T]
	rows  func(parent string, names ...string) sqltest.Response
	other func(parent string, names ...string) sqltest.Response
	// foreign is the other listing's List, whose cursor this one refuses.
	foreign func(ctx context.Context, s *data.Store, sess sqlate.Session) (query.Cursor, error)
}

// directoriesCase is the directory listing; files lists the cursors it
// refuses.
var directoriesCase = listingCase[blobfs.Directory]{
	name: "directories",
	of:   func(s *data.Store) data.Listing[blobfs.Directory] { return s.Directories },
	rows: children, other: filesIn,
	foreign: func(ctx context.Context, s *data.Store, sess sqlate.Session) (query.Cursor, error) {
		c, err := s.Files.List(ctx, sess, parentID, query.Directives{Total: query.TotalNone}, query.Page{Number: 1, Size: 2}, data.ListOptions{})
		return c.Next, err
	},
}

// filesCase is the file listing; directories lists the cursors it
// refuses.
var filesCase = listingCase[blobfs.File]{
	name: "files",
	of:   func(s *data.Store) data.Listing[blobfs.File] { return s.Files },
	rows: filesIn, other: children,
	foreign: func(ctx context.Context, s *data.Store, sess sqlate.Session) (query.Cursor, error) {
		c, err := s.Directories.List(ctx, sess, parentID, query.Directives{Total: query.TotalNone}, query.Page{Number: 1, Size: 2}, data.ListOptions{})
		return c.Next, err
	},
}

// TestListOptions checks the zero ListOptions hides deleting rows and
// reads the listed directory on List and Continue of both listings, and
// IncludeDeleting set does neither.
func TestListOptions(t *testing.T) {
	checkListOptions(t, directoriesCase)
	checkListOptions(t, filesCase)
}

// checkListOptions runs TestListOptions over one listing.
func checkListOptions[T any](t *testing.T, c listingCase[T]) {
	ctx := context.Background()
	none := query.Directives{Total: query.TotalNone}
	for _, mode := range []struct {
		name string
		opts data.ListOptions
		// hides is whether each page hides the deleting status and is
		// followed by the listed directory's read.
		hides bool
	}{
		{"zero", data.ListOptions{}, true},
		{"IncludeDeleting", data.ListOptions{IncludeDeleting: true}, false},
	} {
		t.Run(c.name+"/"+mode.name, func(t *testing.T) {
			script := []sqltest.Response{c.rows(parentID, "a", "b", "c"), c.rows(parentID, "c")}
			var bound []any
			if mode.hides {
				script = []sqltest.Response{c.rows(parentID, "a", "b", "c"), listed(), c.rows(parentID, "c"), listed()}
				bound = []any{"deleting"}
			}
			s, db, rec := openStore(t, fallback, script...)
			first, err := c.of(s).List(ctx, db, parentID, none, query.Page{Number: 1, Size: 2}, mode.opts)
			if err != nil || first.Next == "" {
				t.Fatalf("List = %+v, %v, want a cursor", first, err)
			}
			if _, err := c.of(s).Continue(ctx, db, parentID, none, data.CursorPage{After: first.Next, Size: 2}, mode.opts); err != nil {
				t.Fatalf("Continue: %v", err)
			}
			calls := queries(rec)
			if mode.hides {
				calls = pages(t, rec)
			}
			if len(calls) != 2 {
				t.Fatalf("ran %d pages, want List's and Continue's alone", len(calls))
			}
			for i, want := range [][]any{
				slices.Concat([]any{parentID}, bound, []any{0, 3}),
				slices.Concat([]any{parentID}, bound, []any{"b", 0, 3}),
			} {
				if hides := strings.Contains(calls[i].SQL, "q.status <>"); hides != mode.hides || !slices.Equal(calls[i].Args, want) {
					t.Errorf("page %d = %q %v, want the status hidden %v and %v bound", i+1, calls[i].SQL, calls[i].Args, mode.hides, want)
				}
			}
		})
	}
}

// TestCursorPage checks Continue of both listings binds CursorPage.After
// and Size, and refuses an empty After, a Size below 1, and the other
// listing's cursor before any SQL, with sqlate's errors.
func TestCursorPage(t *testing.T) {
	checkCursorPage(t, directoriesCase)
	checkCursorPage(t, filesCase)
}

// checkCursorPage runs TestCursorPage over one listing.
func checkCursorPage[T any](t *testing.T, c listingCase[T]) {
	t.Run(c.name, func(t *testing.T) {
		ctx := context.Background()
		none := query.Directives{Total: query.TotalNone}
		s, db, rec := openStore(t, fallback,
			c.rows(parentID, "a", "b", "c"), listed(),
			c.rows(parentID, "c", "d"), listed(),
			c.other(parentID, "a", "b", "c"), listed(),
		)
		first, err := c.of(s).List(ctx, db, parentID, none, query.Page{Number: 1, Size: 2}, data.ListOptions{})
		if err != nil || first.Next == "" {
			t.Fatalf("List = %+v, %v, want a cursor", first, err)
		}
		// A Size other than the issuing page's, so the bound limit is
		// the CursorPage's: one row past Size.
		next, err := c.of(s).Continue(ctx, db, parentID, none, data.CursorPage{After: first.Next, Size: 1}, data.ListOptions{})
		if err != nil || len(next.Items) != 1 || !next.More {
			t.Fatalf("Continue = %+v, %v, want one row and more", next, err)
		}
		if got, want := pages(t, rec)[1].Args, []any{parentID, "deleting", "b", 0, 2}; !slices.Equal(got, want) {
			t.Errorf("Continue bound %v, want the page past b of one row, %v", got, want)
		}
		foreign, err := c.foreign(ctx, s, db)
		if err != nil || foreign == "" {
			t.Fatalf("the other listing's List = %q, %v, want a cursor", foreign, err)
		}

		ran := len(rec.Calls())
		for _, r := range []struct {
			name string
			page data.CursorPage
			want string
		}{
			{"an empty After", data.CursorPage{Size: 2}, "Continue requires a cursor from a previous page"},
			{"a zero Size", data.CursorPage{After: first.Next}, "page size must be at least 1"},
			{"a negative Size", data.CursorPage{After: first.Next, Size: -1}, "page size must be at least 1"},
			{"the other listing's cursor", data.CursorPage{After: foreign, Size: 2}, ""},
		} {
			_, err := c.of(s).Continue(ctx, db, parentID, none, r.page, data.ListOptions{})
			if !errors.Is(err, query.ErrDirectives) || !strings.Contains(fmt.Sprint(err), r.want) {
				t.Errorf("Continue with %s = %v, want ErrDirectives saying %q", r.name, err, r.want)
			}
			var cur *query.CursorError
			if mismatch := errors.As(err, &cur) && cur.Reason == query.CursorMismatch; mismatch != (r.want == "") {
				t.Errorf("Continue with %s = %v, want a CursorMismatch only for a foreign cursor", r.name, err)
			}
		}
		if n := len(rec.Calls()); n != ran {
			t.Errorf("the refused pages ran %d calls, want none", n-ran)
		}
	})
}
