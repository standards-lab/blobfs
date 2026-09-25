package datatest

import (
	"errors"
	"slices"
	"testing"

	"github.com/standards-lab/sqlate"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data"
)

// directoryIDs lists the directories under parentID through store, called
// with opts, in one page of up to 100, and returns their ids in name order
// and the total.
func (s *suite) directoryIDs(t *testing.T, store *data.Store, parentID string, opts ...data.ListOption) ([]string, int) {
	t.Helper()
	c, err := store.Directories.List(s.ctx, s.db, parentID, listAll(), firstPage(100), opts...)
	if err != nil {
		t.Fatalf("Directories.List(%s): %v", parentID, err)
	}
	var ids []string
	for _, d := range c.Items {
		ids = append(ids, d.ID)
	}
	return ids, c.Total
}

// fileIDs lists the files in dir through store, called with opts, in one
// page of up to 100, and returns their ids in name order and the total.
func (s *suite) fileIDs(t *testing.T, store *data.Store, dir string, opts ...data.ListOption) ([]string, int) {
	t.Helper()
	c, err := store.Files.List(s.ctx, s.db, dir, listAll(), firstPage(100), opts...)
	if err != nil {
		t.Fatalf("Files.List(%s): %v", dir, err)
	}
	var ids []string
	for _, f := range c.Items {
		ids = append(ids, f.ID)
	}
	return ids, c.Total
}

// deleteFile runs the first step of a file's delete through the store
// under test in a transaction of its own and commits it.
func (s *suite) deleteFile(t *testing.T, id string) {
	t.Helper()
	if _, err := s.db.Transact(s.ctx, func(tx *sqlate.Tx) (blobfs.File, error) {
		return s.store.Files.Delete(s.ctx, tx, id)
	}); err != nil {
		t.Fatalf("Files.Delete: %v", err)
	}
}

// listingsHideTheBranch checks, on both stores alike, that a marked
// branch vanishes from its parent's listing, which counts it no more,
// while an active sibling stays; that a file whose own delete began
// vanishes from its directory's listing; and that IncludeDeleting shows
// both, and lists the marked branch's directories and files, every one
// deleting.
func (s *suite) listingsHideTheBranch(t *testing.T) {
	holder := s.mkdir(t, "hide-"+t.Name())
	keep := s.mkdirUnder(t, holder.ID, "keep")
	b := s.newBranchUnder(t, holder.ID, "marked")
	loose := s.insertFile(t, holder.ID, "loose.txt", blobfs.StatusAvailable)
	deleted := s.insertFile(t, holder.ID, "deleted.txt", blobfs.StatusAvailable)
	if ids, total := s.directoryIDs(t, s.store, holder.ID); !slices.Equal(ids, []string{keep.ID, b.top.ID}) || total != 2 {
		t.Fatalf("before the mark the listing is %v of %d, want keep and marked", ids, total)
	}
	if _, err := s.mark(s.store, b.top.ID); err != nil {
		t.Fatalf("MarkDeleting: %v", err)
	}
	s.deleteFile(t, deleted)

	for _, store := range []*data.Store{s.store, s.baseline} {
		if ids, total := s.directoryIDs(t, store, holder.ID); !slices.Equal(ids, []string{keep.ID}) || total != 1 {
			t.Errorf("after the mark the listing is %v of %d, want keep alone", ids, total)
		}
		if ids, total := s.directoryIDs(t, store, holder.ID, data.IncludeDeleting()); !slices.Equal(ids, []string{keep.ID, b.top.ID}) || total != 2 {
			t.Errorf("with IncludeDeleting the listing is %v of %d, want keep and marked", ids, total)
		}
		if ids, total := s.fileIDs(t, store, holder.ID); !slices.Equal(ids, []string{loose}) || total != 1 {
			t.Errorf("after the file's delete began the listing is %v of %d, want loose.txt alone", ids, total)
		}
		if ids, total := s.fileIDs(t, store, holder.ID, data.IncludeDeleting()); !slices.Equal(ids, []string{deleted, loose}) || total != 2 {
			t.Errorf("with IncludeDeleting the file listing is %v of %d, want deleted.txt and loose.txt", ids, total)
		}
		dirs, err := store.Directories.List(s.ctx, s.db, b.top.ID, listAll(), firstPage(10), data.IncludeDeleting())
		if err != nil || len(dirs.Items) != 1 || dirs.Items[0].ID != b.mid.ID || dirs.Items[0].Status != blobfs.DirectoryStatusDeleting {
			t.Errorf("the marked top's children with IncludeDeleting = %+v, %v, want mid, deleting", dirs, err)
		}
		files, err := store.Files.List(s.ctx, s.db, b.mid.ID, listAll(), firstPage(10), data.IncludeDeleting())
		if err != nil || len(files.Items) != 2 || files.Total != 2 {
			t.Errorf("the marked mid's files with IncludeDeleting = %+v, %v, want its two files", files, err)
		}
		for _, f := range files.Items {
			if f.Status != blobfs.StatusDeleting {
				t.Errorf("the marked mid's file %s is %s, want deleting", f.Name, f.Status)
			}
		}
	}
}

// listingADeletingDirectory checks, against the baseline in the same text,
// that a listing of a deleting directory, the root of a marked branch and
// a directory inside one, is blobfs.ErrDeleting, by List and by Continue
// with a cursor issued before the mark, for directories and files alike;
// and that IncludeDeleting lists it.
func (s *suite) listingADeletingDirectory(t *testing.T) {
	b := s.newBranch(t, "list-deleting-"+t.Name())
	s.mkdirUnder(t, b.top.ID, "second")
	s.insertFile(t, b.top.ID, "second.txt", blobfs.StatusAvailable)
	dirs, err := s.store.Directories.List(s.ctx, s.db, b.top.ID, listAll(), firstPage(1))
	if err != nil || dirs.Next == "" {
		t.Fatalf("Directories.List before the mark = %+v, %v, want a cursor", dirs, err)
	}
	files, err := s.store.Files.List(s.ctx, s.db, b.top.ID, listAll(), firstPage(1))
	if err != nil || files.Next == "" {
		t.Fatalf("Files.List before the mark = %+v, %v, want a cursor", files, err)
	}
	if _, err := s.mark(s.store, b.top.ID); err != nil {
		t.Fatalf("MarkDeleting: %v", err)
	}
	type listing struct {
		name string
		run  func(store *data.Store) error
	}
	var cases []listing
	for _, dir := range []blobfs.Directory{b.top, b.leaf} {
		cases = append(cases,
			listing{"Directories.List of " + dir.Name, func(store *data.Store) error {
				_, err := store.Directories.List(s.ctx, s.db, dir.ID, listAll(), firstPage(10))
				return err
			}},
			listing{"Files.List of " + dir.Name, func(store *data.Store) error {
				_, err := store.Files.List(s.ctx, s.db, dir.ID, listAll(), firstPage(10))
				return err
			}})
	}
	cases = append(cases,
		listing{"Directories.Continue", func(store *data.Store) error {
			_, err := store.Directories.Continue(s.ctx, s.db, b.top.ID, listAll(), dirs.Next, 10)
			return err
		}},
		listing{"Files.Continue", func(store *data.Store) error {
			_, err := store.Files.Continue(s.ctx, s.db, b.top.ID, listAll(), files.Next, 10)
			return err
		}})
	for _, c := range cases {
		err := c.run(s.store)
		if !errors.Is(err, blobfs.ErrDeleting) {
			t.Errorf("%s = %v, want ErrDeleting", c.name, err)
		}
		wantSameError(t, err, c.run(s.baseline))
	}
	for _, store := range []*data.Store{s.store, s.baseline} {
		if ids, total := s.directoryIDs(t, store, b.top.ID, data.IncludeDeleting()); len(ids) != 2 || total != 2 {
			t.Errorf("the deleting top's children with IncludeDeleting = %v of %d, want mid and second", ids, total)
		}
		if ids, total := s.fileIDs(t, store, b.top.ID, data.IncludeDeleting()); len(ids) != 3 || total != 3 {
			t.Errorf("the deleting top's files with IncludeDeleting = %v of %d, want its three", ids, total)
		}
	}
}

// cursorAcrossAMark checks that a cursor taken before a mark still
// continues its listing, on both stores alike: the directory marked and
// the file whose delete began after the cursor was issued are hidden from
// the page past it, which counts the listing without them; and once the
// listed directory is itself marked, the same cursor is ErrDeleting.
func (s *suite) cursorAcrossAMark(t *testing.T) {
	holder := s.mkdir(t, "across-"+t.Name())
	dirs := map[string]string{}
	files := map[string]string{}
	for _, n := range []string{"a", "b", "c", "d"} {
		dirs[n] = s.mkdirUnder(t, holder.ID, n).ID
		files[n] = s.insertFile(t, holder.ID, n, blobfs.StatusAvailable)
	}
	firstDirs, err := s.store.Directories.List(s.ctx, s.db, holder.ID, listAll(), firstPage(2))
	if err != nil || firstDirs.Next == "" {
		t.Fatalf("Directories.List = %+v, %v, want a cursor", firstDirs, err)
	}
	firstFiles, err := s.store.Files.List(s.ctx, s.db, holder.ID, listAll(), firstPage(2))
	if err != nil || firstFiles.Next == "" {
		t.Fatalf("Files.List = %+v, %v, want a cursor", firstFiles, err)
	}
	if _, err := s.mark(s.store, dirs["c"]); err != nil {
		t.Fatalf("MarkDeleting: %v", err)
	}
	s.deleteFile(t, files["c"])
	for _, store := range []*data.Store{s.store, s.baseline} {
		d, err := store.Directories.Continue(s.ctx, s.db, holder.ID, listAll(), firstDirs.Next, 10)
		if err != nil || len(d.Items) != 1 || d.Items[0].ID != dirs["d"] || d.Total != 3 || d.More {
			t.Errorf("Directories.Continue past a mark = %+v, %v, want d alone of 3", d, err)
		}
		f, err := store.Files.Continue(s.ctx, s.db, holder.ID, listAll(), firstFiles.Next, 10)
		if err != nil || len(f.Items) != 1 || f.Items[0].ID != files["d"] || f.Total != 3 || f.More {
			t.Errorf("Files.Continue past a delete = %+v, %v, want d alone of 3", f, err)
		}
	}
	if _, err := s.mark(s.store, holder.ID); err != nil {
		t.Fatalf("MarkDeleting of the listed directory: %v", err)
	}
	for _, store := range []*data.Store{s.store, s.baseline} {
		if _, err := store.Directories.Continue(s.ctx, s.db, holder.ID, listAll(), firstDirs.Next, 10); !errors.Is(err, blobfs.ErrDeleting) {
			t.Errorf("Directories.Continue once the listed directory is marked = %v, want ErrDeleting", err)
		}
		if _, err := store.Files.Continue(s.ctx, s.db, holder.ID, listAll(), firstFiles.Next, 10); !errors.Is(err, blobfs.ErrDeleting) {
			t.Errorf("Files.Continue once the listed directory is marked = %v, want ErrDeleting", err)
		}
	}
}

// deletingRoots is the plain query of the roots of the branches being
// deleted, which Directories.Deleting must match.
const deletingRoots = "SELECT d.id FROM blobfs_directory d JOIN blobfs_directory p ON p.id = d.parent_id WHERE d.status = 'deleting' AND p.status = 'active' ORDER BY d.id"

// deletingFindsTheRoots checks Directories.Deleting against the baseline
// and a plain query: after a branch marked at its top, one marked at its
// middle, and one marked at its middle and then at its top, the roots are
// the three directories a mark named last, each deleting under an active
// parent, and no directory beneath one; the limit keeps the first roots
// in id order; and a limit below 1 is refused in the same text.
func (s *suite) deletingFindsTheRoots(t *testing.T) {
	top := s.newBranch(t, "roots-top-"+t.Name())
	middle := s.newBranch(t, "roots-middle-"+t.Name())
	twice := s.newBranch(t, "roots-twice-"+t.Name())
	for _, id := range []string{top.top.ID, middle.mid.ID, twice.mid.ID, twice.top.ID} {
		if _, err := s.mark(s.store, id); err != nil {
			t.Fatalf("MarkDeleting: %v", err)
		}
	}
	want := s.column(t, deletingRoots)
	for _, store := range []*data.Store{s.store, s.baseline} {
		roots, err := store.Directories.Deleting(s.ctx, s.db, len(want)+10)
		if err != nil {
			t.Fatalf("Deleting: %v", err)
		}
		var ids []string
		for _, d := range roots {
			ids = append(ids, d.ID)
			if d.Status != blobfs.DirectoryStatusDeleting || d.ParentID == nil || s.directory(t, *d.ParentID).Status != blobfs.DirectoryStatusActive {
				t.Errorf("Deleting returned %+v, want a deleting directory under an active parent", d)
			}
		}
		if !slices.Equal(ids, want) {
			t.Errorf("Deleting = %v, the plain query %v", ids, want)
		}
		for _, id := range []string{top.top.ID, middle.mid.ID, twice.top.ID} {
			if !slices.Contains(ids, id) {
				t.Errorf("Deleting = %v, want it to hold the root %s", ids, id)
			}
		}
		for _, id := range []string{top.mid.ID, top.leaf.ID, middle.top.ID, middle.leaf.ID, twice.mid.ID} {
			if slices.Contains(ids, id) {
				t.Errorf("Deleting = %v, want no directory %s that is not a root", ids, id)
			}
		}
		for _, limit := range []int{1, 2} {
			first, err := store.Directories.Deleting(s.ctx, s.db, limit)
			if err != nil || len(first) != limit || first[limit-1].ID != want[limit-1] || !equalDirectory(first[0], roots[0]) {
				t.Errorf("Deleting(%d) = %+v, %v, want the first %d of %v", limit, first, err, limit, want)
			}
		}
	}
	_, err := s.store.Directories.Deleting(s.ctx, s.db, 0)
	if err == nil {
		t.Errorf("Deleting(0) = nil, want a refusal")
	}
	_, base := s.baseline.Directories.Deleting(s.ctx, s.db, 0)
	wantSameError(t, err, base)
}

// deletingFindsNone checks that Directories.Deleting of a tree with no
// branch being deleted returns no rows and no error, on both stores. The
// suite's database holds marked branches by now, so the check runs in a
// transaction that makes every directory active again and rolls back.
func (s *suite) deletingFindsNone(t *testing.T) {
	tx := s.beginTx(t)
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(s.ctx, "UPDATE blobfs_directory SET status = 'active' WHERE status = 'deleting'"); err != nil {
		t.Fatalf("reactivate the marked directories: %v", err)
	}
	for _, store := range []*data.Store{s.store, s.baseline} {
		if roots, err := store.Directories.Deleting(s.ctx, tx, 10); err != nil || len(roots) != 0 {
			t.Errorf("Deleting with no branch being deleted = %+v, %v, want none", roots, err)
		}
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if n := s.count(t, s.db, "SELECT COUNT(*) FROM blobfs_directory WHERE status = 'deleting'"); n == 0 {
		t.Errorf("the rollback left no directory deleting")
	}
}
