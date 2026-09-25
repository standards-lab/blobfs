package datatest

import (
	"errors"
	"fmt"
	"testing"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data"
)

// branches checks the mark of a branch for deletion and the refusals a
// deleting directory makes.
func (s *suite) branches(t *testing.T) {
	t.Run("MarkCounts", s.markCounts)
	t.Run("MarkRefusesTheRoot", s.markRoot)
	t.Run("MarkMissingIsNotFound", s.markMissing)
	t.Run("MarkAgainConverges", s.markAgain)
	t.Run("MarkAtVersion", s.markAtVersion)
	t.Run("DeletingRefuses", s.deletingRefuses)
	t.Run("ListingsHideTheBranch", s.listingsHideTheBranch)
	t.Run("ListingADeletingDirectory", s.listingADeletingDirectory)
	t.Run("CursorAcrossAMark", s.cursorAcrossAMark)
	t.Run("DeletingFindsTheRoots", s.deletingFindsTheRoots)
	t.Run("DeletingFindsNone", s.deletingFindsNone)
}

// branch is a three-level branch the group marks: top under the root, mid
// under top, and leaf under mid, with files at every level.
type branch struct {
	top, mid, leaf blobfs.Directory
	// files are the branch's files that are not deleting yet, which a mark
	// counts.
	files []string
	// deleting is a file of the branch already deleting, which a mark
	// leaves as it is and does not count.
	deleting string
}

// newBranch creates a branch under the root through the store under test,
// named from n: a file in top, a pending and an available file in mid, a
// file in leaf, and a file in top already deleting.
func (s *suite) newBranch(t *testing.T, n string) branch {
	t.Helper()
	return s.newBranchUnder(t, blobfs.RootID, n)
}

// newBranchUnder creates the branch newBranch creates under the directory
// with parentID.
func (s *suite) newBranchUnder(t *testing.T, parentID, n string) branch {
	t.Helper()
	var b branch
	b.top = s.mkdirUnder(t, parentID, n)
	b.mid = s.mkdirUnder(t, b.top.ID, "mid")
	b.leaf = s.mkdirUnder(t, b.mid.ID, "leaf")
	b.files = []string{
		s.insertFile(t, b.top.ID, "top.txt", blobfs.StatusAvailable),
		s.insertFile(t, b.mid.ID, "pending.txt", blobfs.StatusPending),
		s.insertFile(t, b.mid.ID, "mid.txt", blobfs.StatusAvailable),
		s.insertFile(t, b.leaf.ID, "leaf.txt", blobfs.StatusAvailable),
	}
	b.deleting = s.insertFile(t, b.top.ID, "gone.txt", blobfs.StatusDeleting)
	return b
}

// mark runs MarkDeleting of id through store in a transaction of its own
// and commits it.
func (s *suite) mark(store *data.Store, id string, opts ...data.VersionOption) (data.Marked, error) {
	return s.db.Transact(s.ctx, func(tx *sqlate.Tx) (data.Marked, error) {
		return store.Directories.MarkDeleting(s.ctx, tx, id, opts...)
	})
}

// markCounts checks a mark of a three-level branch against the baseline:
// the counts, every row read back deleting at one version past its own,
// the row already deleting unchanged, and a sibling and the root untouched.
func (s *suite) markCounts(t *testing.T) {
	b := s.newBranch(t, "mark-"+t.Name())
	sibling := s.newBranch(t, "sibling-"+t.Name())
	root := s.directory(t, blobfs.RootID)
	already := s.file(t, b.deleting)
	got, err := s.mark(s.store, b.top.ID)
	if err != nil {
		t.Fatalf("MarkDeleting: %v", err)
	}
	if want := (data.Marked{Directories: 3, Files: 4}); got != want {
		t.Errorf("MarkDeleting = %+v, want %+v", got, want)
	}
	base, err := s.mark(s.baseline, sibling.top.ID)
	if err != nil || base != got {
		t.Errorf("the baseline's MarkDeleting of the same shape = %+v, %v, want %+v", base, err, got)
	}
	for _, d := range []blobfs.Directory{b.top, b.mid, b.leaf} {
		after := s.directory(t, d.ID)
		if after.Status != blobfs.DirectoryStatusDeleting || after.Version != d.Version+1 || after.Name != d.Name || !equalString(after.ParentID, d.ParentID) {
			t.Errorf("the marked directory reads %+v, want %+v deleting at version %d", after, d, d.Version+1)
		}
		byName, err := s.store.Directories.FindByName(s.ctx, s.db, *d.ParentID, d.Name)
		if err != nil || !equalDirectory(byName, after) {
			t.Errorf("FindByName of the marked directory = %+v, %v, want %+v", byName, err, after)
		}
	}
	for _, store := range []*data.Store{s.store, s.baseline} {
		byPath, err := store.Directories.FindByPath(s.ctx, s.db, blobfs.RootID, b.top.Name+"/mid/leaf")
		if err != nil || byPath.ID != b.leaf.ID || byPath.Status != blobfs.DirectoryStatusDeleting {
			t.Errorf("FindByPath of the marked leaf = %+v, %v, want it deleting", byPath, err)
		}
	}
	for _, id := range b.files {
		if f := s.file(t, id); f.Status != blobfs.StatusDeleting || f.Version != 2 {
			t.Errorf("the marked file reads %+v, want it deleting at version 2", f)
		}
	}
	if after := s.file(t, b.deleting); !equalFile(after, already) {
		t.Errorf("the file already deleting changed to\n%+v\nfrom\n%+v", after, already)
	}
	if after := s.directory(t, blobfs.RootID); !equalDirectory(after, root) || after.Status != blobfs.DirectoryStatusActive {
		t.Errorf("the root changed to %+v from %+v", after, root)
	}
	outside := s.newBranch(t, "outside-"+t.Name())
	if _, err := s.mark(s.store, outside.mid.ID); err != nil {
		t.Fatalf("MarkDeleting of a branch's middle: %v", err)
	}
	if top := s.directory(t, outside.top.ID); top.Status != blobfs.DirectoryStatusActive || top.Version != outside.top.Version {
		t.Errorf("the mark of a middle directory changed its parent to %+v", top)
	}
	if f := s.file(t, outside.files[0]); f.Status != blobfs.StatusAvailable {
		t.Errorf("the mark of a middle directory reached its parent's file: %+v", f)
	}
}

// markRoot checks the root is refused with ErrRootDirectory on both
// stores, in the same text, and left active.
func (s *suite) markRoot(t *testing.T) {
	_, err := s.mark(s.store, blobfs.RootID)
	if !errors.Is(err, blobfs.ErrRootDirectory) {
		t.Errorf("MarkDeleting(root) = %v, want ErrRootDirectory", err)
	}
	_, base := s.mark(s.baseline, blobfs.RootID)
	wantSameError(t, err, base)
	if root := s.directory(t, blobfs.RootID); root.Status != blobfs.DirectoryStatusActive {
		t.Errorf("the refused mark left the root %s", root.Status)
	}
}

// markMissing checks a directory that does not exist is ErrNotFound on
// both stores, in the same text.
func (s *suite) markMissing(t *testing.T) {
	id := blobfs.NewID()
	_, err := s.mark(s.store, id)
	if !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("MarkDeleting(missing) = %v, want ErrNotFound", err)
	}
	_, base := s.mark(s.baseline, id)
	wantSameError(t, err, base)
}

// markAgain checks a repeated mark counts and changes nothing, and that a
// later mark reaches stragglers, counting them alone.
func (s *suite) markAgain(t *testing.T) {
	b := s.newBranch(t, "again-"+t.Name())
	if _, err := s.mark(s.store, b.top.ID); err != nil {
		t.Fatalf("MarkDeleting: %v", err)
	}
	leaf := s.directory(t, b.leaf.ID)
	file := s.file(t, b.files[3])
	for _, store := range []*data.Store{s.store, s.baseline} {
		got, err := s.mark(store, b.top.ID)
		if err != nil || got != (data.Marked{}) {
			t.Errorf("MarkDeleting of a marked branch = %+v, %v, want nothing marked", got, err)
		}
	}
	if after := s.directory(t, b.leaf.ID); !equalDirectory(after, leaf) {
		t.Errorf("the repeated mark changed the leaf to\n%+v\nfrom\n%+v", after, leaf)
	}
	if after := s.file(t, b.files[3]); !equalFile(after, file) {
		t.Errorf("the repeated mark changed a file to\n%+v\nfrom\n%+v", after, file)
	}
	straggler := s.insertDirectory(t, b.leaf.ID, "straggler")
	s.insertFile(t, straggler, "straggler.txt", blobfs.StatusPending)
	got, err := s.mark(s.store, b.top.ID)
	if err != nil || got != (data.Marked{Directories: 1, Files: 1}) {
		t.Errorf("MarkDeleting after the stragglers = %+v, %v, want the straggling directory and file", got, err)
	}
	if d := s.directory(t, straggler); d.Status != blobfs.DirectoryStatusDeleting {
		t.Errorf("the straggling directory is %s after the mark, want deleting", d.Status)
	}
}

// markAtVersion checks a mark under AtVersion against the baseline: a
// stale version, the current one, a retry after the mark, and a missing
// directory.
func (s *suite) markAtVersion(t *testing.T) {
	for i, store := range []*data.Store{s.store, s.baseline} {
		b := s.newBranch(t, fmt.Sprintf("at-version-%d-%s", i, t.Name()))
		_, err := s.mark(store, b.top.ID, data.AtVersion(b.top.Version+1))
		if !errors.Is(err, query.ErrVersionMismatch) {
			t.Errorf("MarkDeleting at a stale version = %v, want ErrVersionMismatch", err)
		}
		if d := s.directory(t, b.leaf.ID); !equalDirectory(d, b.leaf) {
			t.Errorf("the refused mark changed the leaf to\n%+v\nfrom\n%+v", d, b.leaf)
		}
		got, err := s.mark(store, b.top.ID, data.AtVersion(b.top.Version))
		if err != nil || got != (data.Marked{Directories: 3, Files: 4}) {
			t.Errorf("MarkDeleting at the current version = %+v, %v, want the branch marked", got, err)
		}
		got, err = s.mark(store, b.top.ID, data.AtVersion(b.top.Version))
		if err != nil || got != (data.Marked{}) {
			t.Errorf("MarkDeleting's retry at the version read before the mark = %+v, %v, want nothing marked", got, err)
		}
		if _, err := s.mark(store, blobfs.NewID(), data.AtVersion(1)); !errors.Is(err, blobfs.ErrNotFound) {
			t.Errorf("MarkDeleting(missing) at a version = %v, want ErrNotFound", err)
		}
	}
}

// deletingRefuses checks every create, ensure, and move a deleting
// directory refuses, against the baseline in the same text, at the marked
// rows' old and new versions and for stragglers; every refused row is left
// as it was. An ensure that finds a deleting file reports it present.
func (s *suite) deletingRefuses(t *testing.T) {
	b := s.newBranch(t, "refuses-"+t.Name())
	if _, err := s.mark(s.store, b.top.ID); err != nil {
		t.Fatalf("MarkDeleting: %v", err)
	}
	outside := s.mkdir(t, "outside-"+t.Name())
	loose := s.insertFile(t, outside.ID, "loose.txt", blobfs.StatusAvailable)
	strayDir := s.insertDirectory(t, b.mid.ID, "stray")
	strayFile := s.insertFile(t, b.mid.ID, "stray.txt", blobfs.StatusAvailable)
	mid := s.directory(t, b.mid.ID)
	midFile := s.file(t, b.files[2])

	type refusal struct {
		name string
		run  func(store *data.Store) error
	}
	createDir := func(parent, n string) func(*data.Store) error {
		return func(store *data.Store) error {
			_, err := store.Directories.Create(s.ctx, s.db, parent, n)
			return err
		}
	}
	ensureDir := func(parent, n string) func(*data.Store) error {
		return func(store *data.Store) error {
			_, _, err := store.Directories.Ensure(s.ctx, s.db, parent, n)
			return err
		}
	}
	createFile := func(dir, n string) func(*data.Store) error {
		return func(store *data.Store) error {
			_, err := store.Files.Create(s.ctx, s.db, acceptAll{}, dir, n, "text/plain")
			return err
		}
	}
	ensureFile := func(dir, n string) func(*data.Store) error {
		return func(store *data.Store) error {
			_, _, err := store.Files.Ensure(s.ctx, s.db, acceptAll{}, dir, n, "text/plain")
			return err
		}
	}
	moveDir := func(id, parent, n string, version int64) func(*data.Store) error {
		return func(store *data.Store) error {
			_, err := s.db.Transact(s.ctx, func(tx *sqlate.Tx) (blobfs.Directory, error) {
				return store.Directories.Move(s.ctx, tx, id, parent, n, version)
			})
			return err
		}
	}
	moveFile := func(id, dir, n string, version int64) func(*data.Store) error {
		return func(store *data.Store) error {
			_, err := store.Files.Move(s.ctx, s.db, id, dir, n, version)
			return err
		}
	}
	for _, c := range []refusal{
		{"CreateDirectory", createDir(b.leaf.ID, "new")},
		{"EnsureDirectory", ensureDir(b.leaf.ID, "new")},
		{"EnsureADeletingDirectory", ensureDir(b.top.ID, "mid")},
		{"CreateFile", createFile(b.leaf.ID, "new.txt")},
		{"EnsureFile", ensureFile(b.leaf.ID, "new.txt")},
		{"MoveDirectoryInto", moveDir(outside.ID, b.leaf.ID, outside.Name, outside.Version)},
		{"MoveFileInto", moveFile(loose, b.leaf.ID, "loose.txt", 1)},
		{"MoveDirectoryOut", moveDir(b.mid.ID, blobfs.RootID, "escaped-"+name(t.Name()), mid.Version)},
		{"MoveDirectoryOutAtThePreMarkVersion", moveDir(b.mid.ID, blobfs.RootID, "escaped-"+name(t.Name()), b.mid.Version)},
		{"RenameADeletingDirectory", moveDir(b.mid.ID, b.top.ID, "renamed", mid.Version)},
		{"MoveFileOut", moveFile(b.files[2], outside.ID, "mid.txt", midFile.Version)},
		{"MoveFileOutAtThePreMarkVersion", moveFile(b.files[2], outside.ID, "mid.txt", 1)},
		{"MoveStragglingDirectoryOut", moveDir(strayDir, blobfs.RootID, "stray-"+name(t.Name()), 1)},
		{"MoveStragglingFileOut", moveFile(strayFile, outside.ID, "stray.txt", 1)},
		{"RenameStragglingFile", moveFile(strayFile, b.mid.ID, "renamed.txt", 1)},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := c.run(s.store)
			if !errors.Is(err, blobfs.ErrDeleting) || errors.Is(err, query.ErrVersionMismatch) || errors.Is(err, blobfs.ErrNotFound) {
				t.Errorf("%s = %v, want ErrDeleting", c.name, err)
			}
			wantSameError(t, err, c.run(s.baseline))
		})
	}
	if f, outcome, err := s.store.Files.Ensure(s.ctx, s.db, acceptAll{}, b.mid.ID, "mid.txt", "text/plain"); err != nil ||
		outcome != data.WritePresent || f.Status != blobfs.StatusDeleting {
		t.Errorf("Ensure of a marked file = %+v, %s, %v, want it present and deleting", f, outcome, err)
	}

	if _, err := s.store.Directories.FindByName(s.ctx, s.db, b.leaf.ID, "new"); !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("a refused create left the directory new: %v", err)
	}
	if _, err := s.store.Files.FindByName(s.ctx, s.db, b.leaf.ID, "new.txt"); !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("a refused create left the file new.txt: %v", err)
	}
	if after := s.directory(t, outside.ID); !equalDirectory(after, outside) {
		t.Errorf("a refused move changed the directory to\n%+v\nfrom\n%+v", after, outside)
	}
	if after := s.directory(t, b.mid.ID); !equalDirectory(after, mid) {
		t.Errorf("a refused move changed the marked directory to\n%+v\nfrom\n%+v", after, mid)
	}
	if after := s.file(t, b.files[2]); !equalFile(after, midFile) {
		t.Errorf("a refused move changed the marked file to\n%+v\nfrom\n%+v", after, midFile)
	}
	if after := s.file(t, loose); after.DirectoryID != outside.ID || after.Version != 1 {
		t.Errorf("a refused move changed the loose file to %+v", after)
	}
	if after := s.file(t, strayFile); after.DirectoryID != b.mid.ID || after.Name != "stray.txt" || after.Version != 1 {
		t.Errorf("a refused move changed the straggling file to %+v", after)
	}
	if after := s.directory(t, strayDir); after.Name != "stray" || *after.ParentID != b.mid.ID || after.Version != 1 {
		t.Errorf("a refused move changed the straggling directory to %+v", after)
	}
}
