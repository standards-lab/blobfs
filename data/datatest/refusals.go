package datatest

import (
	"errors"
	"slices"
	"testing"

	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
)

// listingRefusals checks the refusals of a request: an undeclared field,
// the file's key included, and a page below 1 unwrap to
// query.ErrDirectives before any SQL; a filter value the engine cannot
// read as the field's type is a query.InvalidValueError under
// query.ErrDirectives; and a cursor refused by Continue is a
// query.CursorError: an edited one, one issued by the other listing, one
// issued under another sort or other filters, and any cursor under a sort
// that cannot be continued.
func (s *suite) listingRefusals(t *testing.T) {
	dir := s.mkdir(t, "refusals-"+t.Name())
	for _, n := range []string{"a", "b", "c"} {
		s.insertFile(t, dir.ID, n, blobfs.StatusAvailable)
		s.mkdirUnder(t, dir.ID, n)
	}
	for _, c := range []struct {
		label string
		req   query.Directives
		page  query.Page
	}{
		{"sort by key", query.Directives{Sort: []query.Sort{{Field: "key"}}}, firstPage(2)},
		{"filter on key", query.Directives{Filters: []query.Filter{{Field: "key", Op: query.OpEq, Value: "x"}}}, firstPage(2)},
		{"page 0", listAll(), query.Page{Number: 0, Size: 2}},
		{"size 0", listAll(), query.Page{Number: 1, Size: 0}},
	} {
		if _, err := s.store.Files.List(s.ctx, s.db, dir.ID, c.req, c.page); !errors.Is(err, query.ErrDirectives) {
			t.Errorf("Files.List with %s = %v, want ErrDirectives", c.label, err)
		}
	}
	if _, err := s.store.Directories.List(s.ctx, s.db, dir.ID, query.Directives{Sort: []query.Sort{{Field: "directory_id"}}}, firstPage(2)); !errors.Is(err, query.ErrDirectives) {
		t.Errorf("Directories.List sorted by a file field = %v, want ErrDirectives", err)
	}
	_, err := s.store.Files.List(s.ctx, s.db, dir.ID, query.Directives{Filters: []query.Filter{{Field: "id", Op: query.OpEq, Value: "not a uuid"}}}, firstPage(1))
	var invalid *query.InvalidValueError
	if !errors.As(err, &invalid) || !errors.Is(err, query.ErrDirectives) {
		t.Errorf("a filter on a malformed id = %v, want an InvalidValueError under ErrDirectives", err)
	}

	files, err := s.store.Files.List(s.ctx, s.db, dir.ID, listAll(), firstPage(1))
	if err != nil || files.Next == "" {
		t.Fatalf("Files.List = %+v, %v; want a cursor", files, err)
	}
	dirs, err := s.store.Directories.List(s.ctx, s.db, dir.ID, listAll(), firstPage(1))
	if err != nil || dirs.Next == "" {
		t.Fatalf("Directories.List = %+v, %v; want a cursor", dirs, err)
	}
	desc := query.Directives{Sort: []query.Sort{{Field: "name", Descending: true}}}
	filtered := query.Directives{Filters: []query.Filter{{Field: "status", Op: query.OpEq, Value: "available"}}}
	bySize := query.Directives{Sort: []query.Sort{{Field: "size"}}}
	for _, c := range []struct {
		label string
		req   query.Directives
		after query.Cursor
		files bool
	}{
		{"an edited cursor", listAll(), files.Next[:len(files.Next)-2] + "xx", true},
		{"garbage", listAll(), "not-a-cursor", true},
		{"the directory listing's cursor", listAll(), dirs.Next, true},
		{"the file listing's cursor", listAll(), files.Next, false},
		{"another sort", desc, files.Next, true},
		{"other filters", filtered, files.Next, true},
		{"a sort that cannot be continued", bySize, files.Next, true},
	} {
		var err error
		if c.files {
			_, err = s.store.Files.Continue(s.ctx, s.db, dir.ID, c.req, c.after, 1)
		} else {
			_, err = s.store.Directories.Continue(s.ctx, s.db, dir.ID, c.req, c.after, 1)
		}
		var ce *query.CursorError
		if !errors.As(err, &ce) || !errors.Is(err, query.ErrDirectives) {
			t.Errorf("Continue with %s = %v, want a CursorError under ErrDirectives", c.label, err)
		}
	}
	if _, err := s.store.Files.Continue(s.ctx, s.db, dir.ID, listAll(), "", 1); !errors.Is(err, query.ErrDirectives) {
		t.Errorf("Continue with an empty cursor = %v, want ErrDirectives", err)
	}
}

// cursorIsAPosition checks that a cursor records a position in the order
// and not the directory: a cursor from one directory's listing continues
// a sibling's from the same name on.
func (s *suite) cursorIsAPosition(t *testing.T, f listingFixture) {
	first, err := s.store.Files.List(s.ctx, s.db, f.dirs["d1"], listAll(), firstPage(2))
	if err != nil || first.Next == "" {
		t.Fatalf("page 1 of d1 = %+v, %v; want a cursor", first, err)
	}
	other, err := s.store.Files.Continue(s.ctx, s.db, f.dirs["d2"], listAll(), first.Next, 10)
	if err != nil {
		t.Fatalf("the d1 cursor on d2: %v", err)
	}
	p := s.db.Dialect().Placeholder
	want := s.column(t, "SELECT f.id FROM blobfs_file f WHERE f.directory_id = "+p(1)+" AND f.name > "+p(2)+" ORDER BY f.name", f.dirs["d2"], first.Items[1].Name)
	var got []string
	for _, r := range other.Items {
		got = append(got, r.ID)
	}
	if !slices.Equal(got, want) {
		t.Errorf("the d1 cursor on d2 = %v, want %v", got, want)
	}
}
