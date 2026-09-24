package datatest

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/standards-lab/blobfs"
)

// hostileNames are directory names that would break a path spliced into
// SQL text or into an array literal, and that ValidateName accepts:
// quotes, a backslash, braces, a comma, an SQL comment and statement
// terminator, a non-ASCII name, leading and trailing spaces, and the word
// NULL.
var hostileNames = []string{
	`a"b`,
	`a\b`,
	`{x}`,
	`a,b`,
	`'; DROP TABLE blobfs_directory; --`,
	`caf` + string(rune(0x00E9)) + ` ` + string(rune(0x4E2D)) + string(rune(0x6587)),
	` leading`,
	`trailing `,
	`NULL`,
	`"quoted"`,
	`back\\slash\n`,
}

// paths checks path resolution through the variant under test against the
// baseline, and Path.
func (s *suite) paths(t *testing.T) {
	top := s.mkdir(t, "resolve-"+t.Name())
	// A chain of depth six below top: top/d1/d2/d3/d4/d5/d6, where chain[i]
	// is at depth i below top and names[i] is its name.
	chain := []blobfs.Directory{top}
	names := []string{top.Name}
	for i := 1; i <= 6; i++ {
		n := "d" + strconv.Itoa(i)
		chain = append(chain, s.mkdirUnder(t, chain[i-1].ID, n))
		names = append(names, n)
	}
	// rel is the path from chain[from] down to chain[to].
	rel := func(from, to int) string { return strings.Join(names[from+1:to+1], "/") }
	// fromRoot is the path from the root down to chain[to].
	fromRoot := func(to int) string { return strings.Join(names[:to+1], "/") }

	t.Run("Depths", func(t *testing.T) {
		for _, depth := range []int{0, 1, 3, 6} {
			for _, c := range []struct {
				start string
				path  string
				want  blobfs.Directory
			}{
				{top.ID, rel(0, depth), chain[depth]},
				{blobfs.RootID, fromRoot(depth), chain[depth]},
				{chain[1].ID, rel(1, max(depth, 1)), chain[max(depth, 1)]},
			} {
				got, err := s.store.Directories.FindByPath(s.ctx, s.db, c.start, c.path)
				if err != nil || !equalDirectory(got, c.want) {
					t.Errorf("FindByPath(%s, %q) = %+v, %v, want %+v", c.start, c.path, got, err, c.want)
				}
				base, err := s.baseline.Directories.FindByPath(s.ctx, s.db, c.start, c.path)
				if err != nil || !equalDirectory(base, got) {
					t.Errorf("the baseline's FindByPath(%s, %q) = %+v, %v, want the same row", c.start, c.path, base, err)
				}
			}
		}
		// Either spelling of a segment resolves to the row stored composed.
		cafe := s.mkdirUnder(t, chain[2].ID, composed)
		for _, spelling := range []string{composed, decomposed} {
			path := rel(0, 2) + "/" + spelling
			if got, err := s.store.Directories.FindByPath(s.ctx, s.db, top.ID, path); err != nil || !equalDirectory(got, cafe) {
				t.Errorf("FindByPath(top, %q) = %+v, %v, want %+v", path, got, err, cafe)
			}
		}
	})
	t.Run("Path", func(t *testing.T) {
		for depth := range chain {
			s.wantPath(t, chain[depth].ID, "/"+fromRoot(depth))
			// The path after its leading slash resolves from the root back to
			// the directory.
			if got, err := s.store.Directories.FindByPath(s.ctx, s.db, blobfs.RootID, fromRoot(depth)); err != nil || got.ID != chain[depth].ID {
				t.Errorf("FindByPath(root, %q) = %+v, %v, want %s", fromRoot(depth), got, err, chain[depth].ID)
			}
		}
	})
	t.Run("MissingSegment", func(t *testing.T) {
		for _, c := range []struct {
			name  string
			start string
			path  string
			at    string
		}{
			{"First", top.ID, "missing/" + rel(1, 4), "missing"},
			{"Middle", top.ID, rel(0, 3) + "/missing/" + rel(3, 6), rel(0, 3) + "/missing"},
			{"Last", top.ID, rel(0, 6) + "/missing", rel(0, 6) + "/missing"},
			{"FromRoot", blobfs.RootID, fromRoot(2) + "/missing/x", fromRoot(2) + "/missing"},
			{"Deep", chain[3].ID, "missing/b/c/d/e/f/g/h/i/j", "missing"},
		} {
			t.Run(c.name, func(t *testing.T) {
				_, err := s.store.Directories.FindByPath(s.ctx, s.db, c.start, c.path)
				if !errors.Is(err, blobfs.ErrNotFound) || !strings.Contains(err.Error(), " at "+c.at+": ") {
					t.Errorf("FindByPath(%s, %q) = %v, want ErrNotFound at %s", c.start, c.path, err, c.at)
				}
				_, base := s.baseline.Directories.FindByPath(s.ctx, s.db, c.start, c.path)
				wantSameError(t, err, base)
			})
		}
	})
	t.Run("MissingStart", func(t *testing.T) {
		file := s.insertFile(t, top.ID, "not-a-directory.txt", blobfs.StatusAvailable)
		for _, start := range []string{blobfs.NewID(), file} {
			for _, path := range []string{"", names[1]} {
				_, err := s.store.Directories.FindByPath(s.ctx, s.db, start, path)
				if !errors.Is(err, blobfs.ErrNotFound) || strings.Contains(err.Error(), " at ") {
					t.Errorf("FindByPath(%s, %q) = %v, want ErrNotFound for the start and no failing prefix", start, path, err)
				}
				_, base := s.baseline.Directories.FindByPath(s.ctx, s.db, start, path)
				wantSameError(t, err, base)
			}
		}
	})
	t.Run("Refused", func(t *testing.T) {
		// A path is relative and every segment a valid name, so upward
		// navigation and the absolute spelling are refused before any SQL.
		for _, path := range []string{"/", "/" + names[1], names[1] + "/", names[1] + "//" + names[2], ".", "..", names[1] + "/..", "../a", "./" + names[1]} {
			_, err := s.store.Directories.FindByPath(s.ctx, s.db, top.ID, path)
			if !errors.Is(err, blobfs.ErrInvalidPath) {
				t.Errorf("FindByPath(top, %q) = %v, want ErrInvalidPath", path, err)
			}
			_, base := s.baseline.Directories.FindByPath(s.ctx, s.db, top.ID, path)
			wantSameError(t, err, base)
		}
		if _, err := s.store.Directories.FindByPath(s.ctx, s.db, top.ID, names[1]+"/.."); !errors.Is(err, blobfs.ErrInvalidName) {
			t.Errorf("a refused segment = %v, want ErrInvalidName as well", err)
		}
	})
	t.Run("HostileNames", func(t *testing.T) {
		hostile := s.mkdir(t, "hostile-"+t.Name())
		for _, n := range hostileNames {
			// Created through the store directly: the suite's naming helper
			// rewrites backslashes.
			d, err := s.store.Directories.Create(s.ctx, s.db, hostile.ID, n)
			if err != nil {
				t.Fatalf("Create(%q): %v", n, err)
			}
			child := s.mkdirUnder(t, d.ID, "child")
			for _, c := range []struct{ start, path string }{
				{blobfs.RootID, hostile.Name + "/" + n + "/child"},
				{hostile.ID, n + "/child"},
			} {
				got, err := s.store.Directories.FindByPath(s.ctx, s.db, c.start, c.path)
				if err != nil || !equalDirectory(got, child) {
					t.Errorf("FindByPath(%s, %q) = %+v, %v, want the child under the hostile name", c.start, c.path, got, err)
				}
				base, err := s.baseline.Directories.FindByPath(s.ctx, s.db, c.start, c.path)
				if err != nil || !equalDirectory(base, got) {
					t.Errorf("the baseline's FindByPath(%s, %q) = %+v, %v, want the same row", c.start, c.path, base, err)
				}
			}
			s.wantPath(t, child.ID, "/"+hostile.Name+"/"+n+"/child")
			// A name that does not exist under the hostile one is not found,
			// so no character of it reached the engine as syntax.
			if _, err := s.store.Directories.FindByPath(s.ctx, s.db, hostile.ID, n+"/absent"); !errors.Is(err, blobfs.ErrNotFound) {
				t.Errorf("FindByPath below %q of an absent name = %v, want ErrNotFound", n, err)
			}
		}
		// Each of a, b, and a,b is its own directory: the comma is a
		// character of a name, not a separator.
		a := s.mkdirUnder(t, hostile.ID, "a")
		b := s.mkdirUnder(t, hostile.ID, "b")
		for _, c := range []struct {
			name string
			want blobfs.Directory
		}{{"a", a}, {"b", b}} {
			if got, err := s.store.Directories.FindByPath(s.ctx, s.db, hostile.ID, c.name); err != nil || !equalDirectory(got, c.want) {
				t.Errorf("FindByPath(hostile, %q) = %+v, %v, want %+v", c.name, got, err, c.want)
			}
		}
		if n := s.count(t, s.db, "SELECT COUNT(*) FROM blobfs_directory WHERE parent_id = "+s.db.Dialect().Placeholder(1), hostile.ID); n != len(hostileNames)+2 {
			t.Errorf("%d directories under the hostile parent, want %d", n, len(hostileNames)+2)
		}
	})
}
