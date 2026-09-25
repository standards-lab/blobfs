package datatest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/standards-lab/sqlate"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data"
)

// sweeps checks Store.Sweep. It first drains the branches the earlier
// groups marked; every later case starts from nothing to sweep and runs
// over the store under test and then the baseline.
func (s *suite) sweeps(t *testing.T) {
	s.createDirectoryReferences(t)
	t.Run("DrainsTheSuite", s.sweepDrains)
	t.Run("NothingToDo", s.sweepNothing)
	t.Run("ABranch", s.sweepBranch)
	t.Run("CrashMidway", s.sweepCrash)
	t.Run("Stragglers", s.sweepStragglers)
	t.Run("Batch", s.sweepBatch)
	t.Run("HookAborts", s.sweepHookAborts)
	t.Run("RefusalDoesNotBlock", s.sweepRefusalDoesNotBlock)
	t.Run("Stale", s.sweepStale)
}

// errObjectStore is what a failing objectStore reports.
var errObjectStore = errors.New("datatest: the object store failed")

// errHook is what a failing hook reports.
var errHook = errors.New("datatest: the hook failed")

// objectStore is the ObjectDeleter the group passes: it records how often
// each key was deleted, and fails the call numbered failAt, counting from
// 1, having deleted the object first when deleteFirst is set, as a crash
// between the object's delete and the row's purge leaves it.
type objectStore struct {
	deleted     map[string]int
	calls       int
	failAt      int
	deleteFirst bool
}

func newObjectStore() *objectStore { return &objectStore{deleted: map[string]int{}} }

func (o *objectStore) DeleteObject(_ context.Context, key string) error {
	o.calls++
	fail := o.calls == o.failAt
	if fail && !o.deleteFirst {
		return errObjectStore
	}
	o.deleted[key]++
	if fail {
		return errObjectStore
	}
	return nil
}

// hooks records the directories OnRemoveDirectory was called with, in
// order, and unbinds each one's owner row from the suite's stand-in for a
// consumer's owner table inside the removal's transaction; it fails for
// the directory failOn.
type hooks struct {
	s      *suite
	ids    []string
	failOn string
}

func (h *hooks) remove(ctx context.Context, tx *sqlate.Tx, dir blobfs.Directory) error {
	h.ids = append(h.ids, dir.ID)
	if dir.ID == h.failOn {
		return errHook
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM datatest_owner WHERE directory_id = "+h.s.db.Dialect().Placeholder(1), dir.ID)
	return err
}

// own inserts an owner row for the directory with id into the suite's
// stand-in for a consumer's owner table, whose foreign key refuses the
// directory's removal while the row remains.
func (s *suite) own(t *testing.T, id string) {
	t.Helper()
	s.exec(t, "INSERT INTO datatest_owner (directory_id) VALUES ("+s.db.Dialect().Placeholder(1)+")", id)
}

// sweep runs one pass through store with objects and opts.
func (s *suite) sweep(store *data.Store, objects data.ObjectDeleter, opts ...data.SweepOption) (data.SweepResult, error) {
	return store.Sweep(s.ctx, s.db, objects, opts...)
}

// keys reads the keys of the files with ids, which a sweep deletes.
func (s *suite) keys(t *testing.T, ids ...string) []string {
	t.Helper()
	var out []string
	for _, id := range ids {
		out = append(out, s.file(t, id).Key)
	}
	return out
}

// branchFiles is every file of b, those a mark counts and the one already
// deleting.
func branchFiles(b branch) []string {
	return append(slices.Clone(b.files), b.deleting)
}

// wantDirectoriesGone checks each directory with ids is gone.
func (s *suite) wantDirectoriesGone(t *testing.T, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if d, err := s.store.Directories.Find(s.ctx, s.db, id); !errors.Is(err, blobfs.ErrNotFound) {
			t.Errorf("the directory %s is %+v, %v after the sweep, want it gone", id, d, err)
		}
	}
}

// wantFilesGone checks each file with ids is gone.
func (s *suite) wantFilesGone(t *testing.T, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if f, err := s.store.Files.Find(s.ctx, s.db, id); !errors.Is(err, blobfs.ErrNotFound) {
			t.Errorf("the file %s is %+v, %v after the sweep, want it gone", id, f, err)
		}
	}
}

// wantDeletedOnce checks every key was deleted exactly once and nothing
// else was deleted.
func wantDeletedOnce(t *testing.T, objects *objectStore, keys []string) {
	t.Helper()
	for _, key := range keys {
		if n := objects.deleted[key]; n != 1 {
			t.Errorf("the object %s was deleted %d times, want once", key, n)
		}
	}
	if len(objects.deleted) != len(keys) {
		t.Errorf("the sweep deleted %d objects, want %d: %v", len(objects.deleted), len(keys), objects.deleted)
	}
}

// sweepDrains sweeps what the earlier groups left, pass after pass while
// More: every branch being deleted is removed, the directories and files
// the passes report are the rows that left the tables, each object is
// deleted once, and no directory is left deleting.
func (s *suite) sweepDrains(t *testing.T) {
	dirs := s.count(t, s.db, "SELECT COUNT(*) FROM blobfs_directory")
	files := s.count(t, s.db, "SELECT COUNT(*) FROM blobfs_file")
	objects := newObjectStore()
	var total data.SweepResult
	for pass := 0; ; pass++ {
		if pass == 1000 {
			t.Fatalf("the sweep still reports More after %d passes: %+v", pass, total)
		}
		r, err := s.sweep(s.store, objects, data.StaleOlderThan(time.Hour))
		if err != nil {
			t.Fatalf("Sweep: %v", err)
		}
		total.Files, total.Directories, total.Stale = total.Files+r.Files, total.Directories+r.Directories, total.Stale+r.Stale
		if !r.More {
			break
		}
	}
	if total.Directories == 0 || total.Files == 0 {
		t.Errorf("the sweep of the suite's branches did %+v, want directories and files removed", total)
	}
	if gone := dirs - s.count(t, s.db, "SELECT COUNT(*) FROM blobfs_directory"); gone != total.Directories {
		t.Errorf("%d directories left the table, the passes report %d", gone, total.Directories)
	}
	if gone := files - s.count(t, s.db, "SELECT COUNT(*) FROM blobfs_file"); gone != total.Files+total.Stale {
		t.Errorf("%d files left the table, the passes report %d and %d stale", gone, total.Files, total.Stale)
	}
	if n := s.count(t, s.db, "SELECT COUNT(*) FROM blobfs_directory WHERE status = 'deleting'"); n != 0 {
		t.Errorf("%d directories are still deleting after the sweep", n)
	}
	for key, n := range objects.deleted {
		if n != 1 {
			t.Errorf("the object %s was deleted %d times, want once", key, n)
		}
	}
	if len(objects.deleted) != total.Files+total.Stale {
		t.Errorf("the passes deleted %d objects for %d files", len(objects.deleted), total.Files+total.Stale)
	}
}

// sweepNothing checks a pass over a tree with nothing to sweep, on both
// stores and with the stale reclaim or without: a zero result, no
// object deleted, and no hook called.
func (s *suite) sweepNothing(t *testing.T) {
	for _, store := range []*data.Store{s.store, s.baseline} {
		for _, opts := range [][]data.SweepOption{nil, {data.StaleOlderThan(time.Hour)}} {
			objects := newObjectStore()
			h := &hooks{s: s}
			r, err := s.sweep(store, objects, append(opts, data.OnRemoveDirectory(h.remove))...)
			if err != nil || r != (data.SweepResult{}) || objects.calls != 0 || len(h.ids) != 0 {
				t.Errorf("Sweep with nothing to do = %+v, %v, with %d object deletes and hooks %v, want a zero result and nothing called", r, err, objects.calls, h.ids)
			}
		}
	}
}

// sweepBranch checks a full sweep of a three-level branch a consumer owns,
// against the baseline: every row gone in one pass, each object deleted
// once, the hook called deepest first, the counts, and a sibling untouched.
func (s *suite) sweepBranch(t *testing.T) {
	for i, store := range []*data.Store{s.store, s.baseline} {
		b := s.newBranch(t, fmt.Sprintf("sweep-%d-%s", i, t.Name()))
		sibling := s.mkdir(t, fmt.Sprintf("sweep-sibling-%d-%s", i, t.Name()))
		kept := s.insertFile(t, sibling.ID, "kept.txt", blobfs.StatusAvailable)
		s.own(t, b.top.ID)
		keys := s.keys(t, branchFiles(b)...)
		if _, err := s.mark(store, b.top.ID); err != nil {
			t.Fatalf("MarkDeleting: %v", err)
		}
		objects := newObjectStore()
		h := &hooks{s: s}
		r, err := s.sweep(store, objects, data.OnRemoveDirectory(h.remove))
		if want := (data.SweepResult{Files: 5, Directories: 3}); err != nil || r != want {
			t.Errorf("Sweep = %+v, %v, want %+v", r, err, want)
		}
		wantDeletedOnce(t, objects, keys)
		if want := []string{b.leaf.ID, b.mid.ID, b.top.ID}; !slices.Equal(h.ids, want) {
			t.Errorf("the hook was called with %v, want the leaf, the middle, and the top once each", h.ids)
		}
		s.wantDirectoriesGone(t, b.top.ID, b.mid.ID, b.leaf.ID)
		s.wantFilesGone(t, branchFiles(b)...)
		if n := s.count(t, s.db, "SELECT COUNT(*) FROM datatest_owner WHERE directory_id = "+s.db.Dialect().Placeholder(1), b.top.ID); n != 0 {
			t.Errorf("the owner row survived its directory's removal")
		}
		if d := s.directory(t, sibling.ID); !equalDirectory(d, sibling) || s.file(t, kept).Status != blobfs.StatusAvailable {
			t.Errorf("the sweep changed the active sibling to %+v", d)
		}
	}
}

// sweepCrash checks a pass stopped between an object's delete and its
// row's purge, and the next pass finishing the branch.
func (s *suite) sweepCrash(t *testing.T) {
	for i, store := range []*data.Store{s.store, s.baseline} {
		b := s.newBranch(t, fmt.Sprintf("crash-%d-%s", i, t.Name()))
		ids := branchFiles(b)
		keys := s.keys(t, ids...)
		if _, err := s.mark(store, b.top.ID); err != nil {
			t.Fatalf("MarkDeleting: %v", err)
		}
		objects := newObjectStore()
		objects.failAt, objects.deleteFirst = 3, true
		r, err := s.sweep(store, objects)
		if !errors.Is(err, errObjectStore) || r != (data.SweepResult{Files: 2}) {
			t.Errorf("the crashed Sweep = %+v, %v, want two files and the store's error", r, err)
		}
		var stopped []string
		for _, id := range ids {
			if f, err := s.store.Files.Find(s.ctx, s.db, id); err == nil {
				if f.Status != blobfs.StatusDeleting {
					t.Errorf("the file %s is %s after the crash, want deleting", f.Name, f.Status)
				}
				if objects.deleted[f.Key] == 1 {
					stopped = append(stopped, f.Key)
				}
			}
		}
		if len(stopped) != 1 {
			t.Errorf("the crash left %v with the object deleted and the row not purged, want one", stopped)
		}
		objects.failAt = 0
		r, err = s.sweep(store, objects)
		if want := (data.SweepResult{Files: 3, Directories: 3}); err != nil || r != want {
			t.Errorf("the finishing Sweep = %+v, %v, want %+v", r, err, want)
		}
		for _, key := range keys {
			want := 1
			if slices.Contains(stopped, key) {
				want = 2
			}
			if n := objects.deleted[key]; n != want {
				t.Errorf("the object %s was deleted %d times, want %d", key, n, want)
			}
		}
		s.wantDirectoriesGone(t, b.top.ID, b.mid.ID, b.leaf.ID)
		s.wantFilesGone(t, ids...)
	}
}

// sweepStragglers checks, against the baseline, that the rows a create
// that raced the mark left active in the branch, a directory under the
// leaf with a pending file and an available file in the middle, are
// marked by the pass and swept with the branch.
func (s *suite) sweepStragglers(t *testing.T) {
	for i, store := range []*data.Store{s.store, s.baseline} {
		b := s.newBranch(t, fmt.Sprintf("stragglers-%d-%s", i, t.Name()))
		if _, err := s.mark(store, b.top.ID); err != nil {
			t.Fatalf("MarkDeleting: %v", err)
		}
		dir := s.insertDirectory(t, b.leaf.ID, "straggler")
		files := append(branchFiles(b),
			s.insertFile(t, dir, "straggler.txt", blobfs.StatusPending),
			s.insertFile(t, b.mid.ID, "straggler.txt", blobfs.StatusAvailable))
		keys := s.keys(t, files...)
		objects := newObjectStore()
		r, err := s.sweep(store, objects)
		if want := (data.SweepResult{Files: 7, Directories: 4}); err != nil || r != want {
			t.Errorf("Sweep = %+v, %v, want %+v", r, err, want)
		}
		wantDeletedOnce(t, objects, keys)
		s.wantDirectoriesGone(t, b.top.ID, b.mid.ID, b.leaf.ID, dir)
		s.wantFilesGone(t, files...)
	}
}

// sweepBatch checks the bound over a branch of eight records: a batch of
// three takes three passes, and a batch of four two, the last with no More.
func (s *suite) sweepBatch(t *testing.T) {
	for i, store := range []*data.Store{s.store, s.baseline} {
		for _, c := range []struct {
			batch  int
			passes []data.SweepResult
		}{
			{3, []data.SweepResult{{Files: 3, More: true}, {Files: 2, Directories: 1, More: true}, {Directories: 2}}},
			{4, []data.SweepResult{{Files: 4, More: true}, {Files: 1, Directories: 3}}},
		} {
			b := s.newBranch(t, fmt.Sprintf("batch-%d-%d-%s", c.batch, i, t.Name()))
			keys := s.keys(t, branchFiles(b)...)
			if _, err := s.mark(store, b.top.ID); err != nil {
				t.Fatalf("MarkDeleting: %v", err)
			}
			objects := newObjectStore()
			for n, want := range c.passes {
				if r, err := s.sweep(store, objects, data.Batch(c.batch)); err != nil || r != want {
					t.Errorf("pass %d of Batch(%d) = %+v, %v, want %+v", n+1, c.batch, r, err, want)
				}
			}
			wantDeletedOnce(t, objects, keys)
			s.wantDirectoriesGone(t, b.top.ID, b.mid.ID, b.leaf.ID)
		}
	}
}

// sweepHookAborts checks a failing hook aborts its removal, a pass without
// the hook is refused by the owner row, and a pass with it finishes.
func (s *suite) sweepHookAborts(t *testing.T) {
	for i, store := range []*data.Store{s.store, s.baseline} {
		b := s.newBranch(t, fmt.Sprintf("hook-%d-%s", i, t.Name()))
		s.own(t, b.top.ID)
		if _, err := s.mark(store, b.top.ID); err != nil {
			t.Fatalf("MarkDeleting: %v", err)
		}
		h := &hooks{s: s, failOn: b.mid.ID}
		r, err := s.sweep(store, newObjectStore(), data.OnRemoveDirectory(h.remove))
		if !errors.Is(err, errHook) || r != (data.SweepResult{Files: 5, Directories: 1}) {
			t.Errorf("Sweep under a failing hook = %+v, %v, want the leaf removed and the hook's error", r, err)
		}
		if d := s.directory(t, b.mid.ID); d.Status != blobfs.DirectoryStatusDeleting {
			t.Errorf("the aborted directory is %s, want deleting", d.Status)
		}
		roots, err := store.Directories.Deleting(s.ctx, s.db, 10)
		if err != nil || len(roots) != 1 || roots[0].ID != b.top.ID {
			t.Errorf("Deleting after the abort = %+v, %v, want the branch's top", roots, err)
		}

		r, err = s.sweep(store, newObjectStore())
		if !errors.Is(err, blobfs.ErrReferenced) || r != (data.SweepResult{Directories: 1}) {
			t.Errorf("Sweep without the hook = %+v, %v, want the middle removed and the top refused as ErrReferenced", r, err)
		}
		s.wantDirectoriesGone(t, b.mid.ID)

		h = &hooks{s: s}
		r, err = s.sweep(store, newObjectStore(), data.OnRemoveDirectory(h.remove))
		if err != nil || r != (data.SweepResult{Directories: 1}) || !slices.Equal(h.ids, []string{b.top.ID}) {
			t.Errorf("the finishing Sweep = %+v, %v with hooks %v, want the top removed and hooked once", r, err, h.ids)
		}
		s.wantDirectoriesGone(t, b.top.ID)
	}
}

// sweepRefusalDoesNotBlock checks a branch refused on every pass holds back
// no branch behind it.
func (s *suite) sweepRefusalDoesNotBlock(t *testing.T) {
	for i, store := range []*data.Store{s.store, s.baseline} {
		stuck := s.newBranch(t, fmt.Sprintf("stuck-%d-%s", i, t.Name()))
		free := s.newBranch(t, fmt.Sprintf("free-%d-%s", i, t.Name()))
		s.own(t, stuck.top.ID)
		for _, b := range []branch{stuck, free} {
			if _, err := s.mark(store, b.top.ID); err != nil {
				t.Fatalf("MarkDeleting: %v", err)
			}
		}
		roots, err := store.Directories.Deleting(s.ctx, s.db, 10)
		if err != nil || len(roots) != 2 || roots[0].ID != stuck.top.ID {
			t.Fatalf("Deleting = %+v, %v, want the stuck branch first", roots, err)
		}
		r, err := s.sweep(store, newObjectStore())
		if !errors.Is(err, blobfs.ErrReferenced) || r.Directories != 5 || r.Files != 10 {
			t.Errorf("Sweep past a refused branch = %+v, %v, want both branches emptied, the free one removed, and ErrReferenced", r, err)
		}
		s.wantDirectoriesGone(t, free.top.ID, free.mid.ID, free.leaf.ID)
		if d := s.directory(t, stuck.top.ID); d.Status != blobfs.DirectoryStatusDeleting {
			t.Errorf("the refused top is %s, want deleting", d.Status)
		}
		h := &hooks{s: s}
		if r, err := s.sweep(store, newObjectStore(), data.OnRemoveDirectory(h.remove)); err != nil || r != (data.SweepResult{Directories: 1}) {
			t.Errorf("the finishing Sweep = %+v, %v, want the refused top removed", r, err)
		}
		s.wantDirectoriesGone(t, stuck.top.ID)
	}
}

// sweepStale checks the reclaim with and without StaleOlderThan: old
// pending and deleting rows finished, their names freed, and younger rows
// and available rows left as they were.
func (s *suite) sweepStale(t *testing.T) {
	old := time.Now().Add(-2 * time.Hour)
	for i, store := range []*data.Store{s.store, s.baseline} {
		dir := s.mkdir(t, fmt.Sprintf("stale-%d-%s", i, t.Name()))
		abandoned := s.insertFile(t, dir.ID, "abandoned.txt", blobfs.StatusPending)
		young := s.insertFile(t, dir.ID, "young.txt", blobfs.StatusPending)
		available := s.insertFile(t, dir.ID, "available.txt", blobfs.StatusAvailable)
		stopped := s.insertFile(t, dir.ID, "stopped.txt", blobfs.StatusAvailable)
		starting := s.insertFile(t, dir.ID, "starting.txt", blobfs.StatusAvailable)
		s.deleteFile(t, stopped)
		s.deleteFile(t, starting)
		p := s.db.Dialect().Placeholder
		for _, id := range []string{abandoned, available, stopped} {
			s.exec(t, "UPDATE blobfs_file SET updated_at = "+p(1)+" WHERE id = "+p(2), old, id)
		}
		before := map[string]blobfs.File{}
		for _, id := range []string{abandoned, young, available, stopped, starting} {
			before[id] = s.file(t, id)
		}
		if _, err := store.Files.Create(s.ctx, s.db, acceptAll{}, dir.ID, "stopped.txt", "text/plain"); !errors.Is(err, blobfs.ErrNameTaken) {
			t.Errorf("Create over the stopped delete's name = %v, want ErrNameTaken while its row remains", err)
		}

		objects := newObjectStore()
		if r, err := s.sweep(store, objects); err != nil || r != (data.SweepResult{}) || objects.calls != 0 {
			t.Errorf("Sweep without StaleOlderThan = %+v, %v with %d object deletes, want nothing done", r, err, objects.calls)
		}
		for _, id := range []string{abandoned, stopped} {
			if f := s.file(t, id); !equalFile(f, before[id]) {
				t.Errorf("the pass without StaleOlderThan changed %s to %+v", f.Name, f)
			}
		}

		r, err := s.sweep(store, objects, data.StaleOlderThan(time.Hour))
		if err != nil || r != (data.SweepResult{Stale: 2}) {
			t.Errorf("Sweep with StaleOlderThan = %+v, %v, want the abandoned write and the stopped delete", r, err)
		}
		wantDeletedOnce(t, objects, []string{before[abandoned].Key, before[stopped].Key})
		s.wantFilesGone(t, abandoned, stopped)
		for _, id := range []string{young, available, starting} {
			if f := s.file(t, id); !equalFile(f, before[id]) {
				t.Errorf("the reclaim changed %s to\n%+v\nfrom\n%+v", f.Name, f, before[id])
			}
		}
		if f, err := store.Files.Create(s.ctx, s.db, acceptAll{}, dir.ID, "stopped.txt", "text/plain"); err != nil || f.Status != blobfs.StatusPending {
			t.Errorf("Create over the finished delete's name = %+v, %v, want a new pending row", f, err)
		}
	}
}
