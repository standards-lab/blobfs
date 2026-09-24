package datatest

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data"
)

// keysetFiles is the number of files the keyset group seeds, and
// keysetDirectories the number of directories: neither a multiple of the
// page size, so the last page is partial.
const (
	keysetFiles       = 23
	keysetDirectories = 9
)

// keysetPage is the page size the keyset group walks with.
const keysetPage = 5

// sortIndex is the index a consumer's migration adds for a sort by
// created_at, which the keyset group creates for its second pass and
// drops again.
const sortIndex = "datatest_ix_file_directory_created"

// keysetSorts are the cursorable sorts the keyset group walks: one term,
// two terms through the appended key, and three, in both directions.
var keysetSorts = []struct {
	name string
	sort []query.Sort
}{
	{"name", []query.Sort{{Field: "name"}}},
	{"name desc", []query.Sort{{Field: "name", Descending: true}}},
	{"created_at", []query.Sort{{Field: "created_at"}}},
	{"created_at desc", []query.Sort{{Field: "created_at", Descending: true}}},
	{"created_at, version", []query.Sort{{Field: "created_at"}, {Field: "version"}}},
	{"created_at desc, version desc", []query.Sort{{Field: "created_at", Descending: true}, {Field: "version", Descending: true}}},
	{"version, created_at", []query.Sort{{Field: "version"}, {Field: "created_at"}}},
}

// keyset checks the cursor walk through the store under test against the
// baseline: every sort walked by cursor to the end returns the same rows
// in the same order on both stores, the same rows an offset walk returns,
// each exactly once, with every page but the last carrying a cursor and a
// continued page the whole listing's total; once without an index on
// created_at, as the library ships, and once with the index a consumer
// adds. The timestamps round-trip exactly, or a walk over the ties would
// skip or repeat rows.
func (s *suite) keyset(t *testing.T) {
	dir := s.mkdir(t, "keyset-"+t.Name())
	// Files whose created_at ties in groups of three, whose version
	// alternates, and whose name order differs from their creation order,
	// so every sort term decides some pairs.
	base := time.Date(2026, 1, 1, 0, 0, 0, 123456000, time.UTC)
	for i := range keysetFiles {
		s.insertFileAt(t, dir.ID, fmt.Sprintf("f%02d.txt", (i*7)%keysetFiles), base.Add(time.Duration(i/3)*time.Minute), int64(1+i%2))
	}
	for i := range keysetDirectories {
		s.mkdirUnder(t, dir.ID, fmt.Sprintf("d%d", (i*4)%keysetDirectories))
	}
	t.Run("WithoutIndex", func(t *testing.T) { s.walks(t, dir.ID) })
	t.Run("WithIndex", func(t *testing.T) {
		s.exec(t, "CREATE INDEX "+sortIndex+" ON blobfs_file (directory_id, created_at)")
		defer s.exec(t, "DROP INDEX "+sortIndex)
		s.walks(t, dir.ID)
	})
}

// walks runs every sort over the files and the children of dir.
func (s *suite) walks(t *testing.T, dir string) {
	for _, c := range keysetSorts {
		t.Run("Files/"+c.name, func(t *testing.T) {
			list := func(store *data.Store) lister[blobfs.File] {
				return lister[blobfs.File]{
					list: func(req query.Directives, page query.Page) (query.Collection[blobfs.File], error) {
						return store.Files.List(s.ctx, s.db, dir, req, page)
					},
					cont: func(req query.Directives, after query.Cursor, size int) (query.Collection[blobfs.File], error) {
						return store.Files.Continue(s.ctx, s.db, dir, req, after, size)
					},
				}
			}
			got := cursorWalk(t, list(s.store), c.sort, keysetFiles, fileKey)
			base := cursorWalk(t, list(s.baseline), c.sort, keysetFiles, fileKey)
			if !slices.Equal(got, base) {
				t.Errorf("the cursor walk returned\n%v\nand the baseline's\n%v", got, base)
			}
			if offset := offsetWalk(t, list(s.store), c.sort, fileKey); !slices.Equal(got, offset) {
				t.Errorf("the cursor walk returned\n%v\nand the offset walk\n%v", got, offset)
			}
			if len(got) != keysetFiles || len(slices.Compact(slices.Sorted(slices.Values(got)))) != keysetFiles {
				t.Errorf("the walk returned %d rows with duplicates, want %d distinct", len(got), keysetFiles)
			}
		})
	}
	for _, c := range keysetSorts[:4] {
		t.Run("Directories/"+c.name, func(t *testing.T) {
			list := func(store *data.Store) lister[blobfs.Directory] {
				return lister[blobfs.Directory]{
					list: func(req query.Directives, page query.Page) (query.Collection[blobfs.Directory], error) {
						return store.Directories.List(s.ctx, s.db, dir, req, page)
					},
					cont: func(req query.Directives, after query.Cursor, size int) (query.Collection[blobfs.Directory], error) {
						return store.Directories.Continue(s.ctx, s.db, dir, req, after, size)
					},
				}
			}
			got := cursorWalk(t, list(s.store), c.sort, keysetDirectories, directoryKey)
			base := cursorWalk(t, list(s.baseline), c.sort, keysetDirectories, directoryKey)
			if !slices.Equal(got, base) {
				t.Errorf("the cursor walk returned\n%v\nand the baseline's\n%v", got, base)
			}
			if offset := offsetWalk(t, list(s.store), c.sort, directoryKey); !slices.Equal(got, offset) {
				t.Errorf("the cursor walk returned\n%v\nand the offset walk\n%v", got, offset)
			}
			if len(got) != keysetDirectories || len(slices.Compact(slices.Sorted(slices.Values(got)))) != keysetDirectories {
				t.Errorf("the walk returned %d rows with duplicates, want %d distinct", len(got), keysetDirectories)
			}
		})
	}
}

func fileKey(f blobfs.File) string           { return f.Name + "@" + f.ID }
func directoryKey(d blobfs.Directory) string { return d.Name + "@" + d.ID }

// lister is one listing's two reads, List and Continue, bound to a store
// and a directory.
type lister[T any] struct {
	list func(query.Directives, query.Page) (query.Collection[T], error)
	cont func(query.Directives, query.Cursor, int) (query.Collection[T], error)
}

// cursorWalk reads page 1 with its total and then follows the cursor to
// the end, the second page with the total and the rest without, and
// returns the keys of every row in order. Every page but the last reports
// More with a cursor, and the last neither; a counted continued page
// reports the whole listing's total, rows.
func cursorWalk[T any](t *testing.T, l lister[T], sort []query.Sort, rows int, key func(T) string) []string {
	t.Helper()
	req := query.Directives{Sort: sort}
	c, err := l.list(req, query.Page{Number: 1, Size: keysetPage})
	var out []string
	for pages := 1; ; pages++ {
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		if len(c.Items) > keysetPage {
			t.Fatalf("page %d holds %d rows, more than the size", pages, len(c.Items))
		}
		switch {
		case req.Total == query.TotalExact && c.Total != rows:
			t.Errorf("page %d reports the total %d, want the whole listing's %d", pages, c.Total, rows)
		case req.Total == query.TotalNone && c.Total != query.NoTotal:
			t.Errorf("page %d without the total reports %d", pages, c.Total)
		}
		for _, row := range c.Items {
			out = append(out, key(row))
		}
		if !c.More {
			if c.Next != "" {
				t.Errorf("the last page carries a cursor")
			}
			return out
		}
		if c.Next == "" {
			t.Fatalf("page %d reports more rows and no cursor under %v", pages, sort)
		}
		if pages > rows {
			t.Fatalf("the walk did not end after %d pages", pages)
		}
		if pages >= 2 {
			req.Total = query.TotalNone
		}
		c, err = l.cont(req, c.Next, keysetPage)
	}
}

// offsetWalk reads every page by its number without the total and
// returns the keys of every row in order.
func offsetWalk[T any](t *testing.T, l lister[T], sort []query.Sort, key func(T) string) []string {
	t.Helper()
	var out []string
	for n := 1; n <= 100; n++ {
		c, err := l.list(query.Directives{Sort: sort, Total: query.TotalNone}, query.Page{Number: n, Size: keysetPage})
		if err != nil {
			t.Fatalf("page %d by number: %v", n, err)
		}
		for _, row := range c.Items {
			out = append(out, key(row))
		}
		if !c.More {
			return out
		}
	}
	t.Fatal("the offset walk did not end")
	return nil
}
