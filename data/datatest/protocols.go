package datatest

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/standards-lab/sqlate"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data"
)

// protocols checks the protocols end to end over the database: Store.Write,
// Store.Ensure, Store.Remove, Store.Purge, and SweepUntilDone over the
// store's passes. Each case runs over the store under test and then the
// baseline. It runs before any group marks a branch, and it sweeps every
// branch it marks, so SweepUntilDone drains only its own backlog.
func (s *suite) protocols(t *testing.T) {
	t.Run("Write", s.protocolWrite)
	t.Run("WriteFailedPut", s.protocolWriteFailedPut)
	t.Run("WriteRefusedCompletion", s.protocolWriteRefusedCompletion)
	t.Run("WriteRefusedBegin", s.protocolWriteRefusedBegin)
	t.Run("EnsureCreates", s.protocolEnsureCreates)
	t.Run("EnsureResumes", s.protocolEnsureResumes)
	t.Run("EnsureNameTaken", s.protocolEnsureNameTaken)
	t.Run("EnsureDeleting", s.protocolEnsureDeleting)
	t.Run("Remove", s.protocolRemove)
	t.Run("RemoveFailedObjectDelete", s.protocolRemoveFailedDelete)
	t.Run("RemoveRefusedPick", s.protocolRemoveRefusedPick)
	t.Run("Purge", s.protocolPurge)
	t.Run("SweepUntilDone", s.protocolSweepUntilDone)
	t.Run("SweepUntilDoneStopped", s.protocolSweepUntilDoneStopped)
}

// errBegin is what a refusing begin or pick reports.
var errBegin = errors.New("datatest: the caller's scope refuses")

// storesUnder names the store under test and the baseline, for the cases
// that run over each in a subtest of its own.
func (s *suite) storesUnder() []struct {
	name  string
	store *data.Store
} {
	return []struct {
		name  string
		store *data.Store
	}{{"UnderTest", s.store}, {"Baseline", s.baseline}}
}

// writeFile runs Store.Write through store of body as the file n in dir,
// its begin creating the pending row with Files.Create.
func (s *suite) writeFile(store *data.Store, objects data.ObjectStore, dir, n, body string) (blobfs.File, error) {
	return store.Write(s.ctx, s.db, objects, strings.NewReader(body), int64(len(body)), func(tx *sqlate.Tx) (blobfs.File, error) {
		return store.Files.Create(s.ctx, tx, acceptAll{}, dir, n, "text/plain")
	})
}

// ensureWrite runs Store.Ensure through store of body as the file n in dir
// under id, its begin running Files.Ensure with WithID(id).
func (s *suite) ensureWrite(store *data.Store, objects data.ObjectStore, dir, n, id, body string) (blobfs.File, bool, error) {
	return store.Ensure(s.ctx, s.db, objects, id, strings.NewReader(body), int64(len(body)), func(tx *sqlate.Tx) (blobfs.File, data.WriteOutcome, error) {
		return store.Files.Ensure(s.ctx, tx, acceptAll{}, dir, n, "text/plain", data.WithID(id))
	})
}

// removeFile runs Store.Remove through store of the file with id, its
// pick naming the file.
func (s *suite) removeFile(store *data.Store, objects data.ObjectDeleter, id string) error {
	return store.Remove(s.ctx, s.db, objects, func(*sqlate.Tx) (string, error) { return id, nil })
}

// wantWritten checks that got is the available row the database holds,
// with the put's size and entity tag, and body stored under its key in the
// row's content type.
func (s *suite) wantWritten(t *testing.T, objects *objectStore, got blobfs.File, body string) {
	t.Helper()
	if got.Status != blobfs.StatusAvailable || got.Size == nil || *got.Size != int64(len(body)) || got.ETag == nil ||
		*got.ETag != objects.put.ETag || got.ContentType != objects.put.ContentType || got.ContentType != "text/plain" {
		t.Errorf("the write returned %+v, want the available row with the put's %+v", got, objects.put)
	}
	if stored := s.file(t, got.ID); !equalFile(got, stored) {
		t.Errorf("the write returned\n%+v\nbut the database holds\n%+v", got, stored)
	}
	if b, ok := objects.objects[got.Key]; !ok || b != body {
		t.Errorf("the store holds %q under the row's key %s, want %q", b, got.Key, body)
	}
}

// protocolWrite checks a write that succeeds: the row available at the
// version past the pending one, with the put's size and entity tag, and
// the body stored under the row's key, the one put.
func (s *suite) protocolWrite(t *testing.T) {
	for _, tier := range s.storesUnder() {
		t.Run(tier.name, func(t *testing.T) {
			dir := s.mkdir(t, "write-"+t.Name())
			objects := newObjectStore()
			got, err := s.writeFile(tier.store, objects, dir.ID, "report.txt", "report")
			if err != nil {
				t.Fatalf("Write: %v", err)
			}
			if got.Version != 2 || got.DirectoryID != dir.ID || got.Name != "report.txt" || got.Key != got.ID+"/report.txt" {
				t.Errorf("Write returned %+v, want the row at version 2 under its key", got)
			}
			s.wantWritten(t, objects, got, "report")
			if objects.puts != 1 || objects.calls != 0 {
				t.Errorf("the write put %d times and deleted %d, want one put and no delete", objects.puts, objects.calls)
			}
		})
	}
}

// protocolWriteFailedPut checks that a put that fails abandons the write:
// the store's error returned, the object's delete run under the row's key,
// no row left, and the name free for a second write that succeeds.
func (s *suite) protocolWriteFailedPut(t *testing.T) {
	for _, tier := range s.storesUnder() {
		t.Run(tier.name, func(t *testing.T) {
			dir := s.mkdir(t, "write-failed-put-"+t.Name())
			objects := newObjectStore()
			objects.failPut = true
			var key string
			objects.beforePut = func(k string) { key = k }
			if _, err := s.writeFile(tier.store, objects, dir.ID, "report.txt", "report"); !errors.Is(err, errObjectStore) {
				t.Fatalf("Write under a failing put = %v, want the store's error", err)
			}
			if key == "" || objects.deleted[key] != 1 || len(objects.deleted) != 1 {
				t.Errorf("the abandon deleted %v, want the row's key %q once", objects.deleted, key)
			}
			if f, err := tier.store.Files.FindByName(s.ctx, s.db, dir.ID, "report.txt"); !errors.Is(err, blobfs.ErrNotFound) {
				t.Errorf("the abandoned write left %+v, %v, want no row", f, err)
			}
			objects.failPut, objects.beforePut = false, nil
			got, err := s.writeFile(tier.store, objects, dir.ID, "report.txt", "again")
			if err != nil {
				t.Fatalf("the second Write of the freed name: %v", err)
			}
			s.wantWritten(t, objects, got, "again")
		})
	}
}

// protocolWriteRefusedCompletion checks a completion refused because the
// file's branch was marked between the write's first transaction and its
// completion: the directory's DeletingError, the object the write put
// deleted again, and the row left deleting, which a sweep then finishes.
func (s *suite) protocolWriteRefusedCompletion(t *testing.T) {
	for _, tier := range s.storesUnder() {
		t.Run(tier.name, func(t *testing.T) {
			dir := s.mkdir(t, "write-refused-completion-"+t.Name())
			objects := newObjectStore()
			var key string
			objects.beforePut = func(k string) {
				key = k
				if _, err := s.mark(tier.store, dir.ID); err != nil {
					t.Errorf("MarkDeleting inside the put: %v", err)
				}
			}
			_, err := s.writeFile(tier.store, objects, dir.ID, "report.txt", "report")
			if !errors.Is(err, blobfs.ErrDeleting) || errors.Is(err, errObjectStore) {
				t.Fatalf("Write across a mark = %v, want ErrDeleting", err)
			}
			wantDeletingKind(t, err, true, dir.ID)
			if _, ok := objects.objects[key]; ok || objects.deleted[key] != 1 || len(objects.deleted) != 1 {
				t.Errorf("after the refusal the store holds the object %v and deleted %v, want the put object deleted once", ok, objects.deleted)
			}
			ids := s.column(t, "SELECT f.id FROM blobfs_file f WHERE f.directory_id = "+s.db.Dialect().Placeholder(1), dir.ID)
			if len(ids) != 1 {
				t.Fatalf("the directory holds the files %v, want the one written", ids)
			}
			if f := s.file(t, ids[0]); f.Status != blobfs.StatusDeleting || f.Key != key {
				t.Errorf("the refused write left %+v, want its row deleting for the sweep", f)
			}
			sweeper := newObjectStore()
			if r, err := s.sweep(tier.store, sweeper); err != nil || r != (data.SweepResult{Files: 1, Directories: 1}) {
				t.Errorf("the Sweep after the refusal = %+v, %v, want the row and its directory finished", r, err)
			}
			s.wantFilesGone(t, ids...)
			s.wantDirectoriesGone(t, dir.ID)
		})
	}
}

// protocolWriteRefusedBegin checks a begin that fails after its create:
// its error returned as it came, nothing put, and no row committed.
func (s *suite) protocolWriteRefusedBegin(t *testing.T) {
	for _, tier := range s.storesUnder() {
		t.Run(tier.name, func(t *testing.T) {
			dir := s.mkdir(t, "write-refused-begin-"+t.Name())
			objects := newObjectStore()
			_, err := tier.store.Write(s.ctx, s.db, objects, strings.NewReader("report"), 6, func(tx *sqlate.Tx) (blobfs.File, error) {
				if _, err := tier.store.Files.Create(s.ctx, tx, acceptAll{}, dir.ID, "report.txt", "text/plain"); err != nil {
					return blobfs.File{}, err
				}
				return blobfs.File{}, errBegin
			})
			if !errors.Is(err, errBegin) || strings.Contains(err.Error(), "data: ") {
				t.Errorf("Write under a refusing begin = %v, want its error as it came", err)
			}
			if objects.puts != 0 || objects.calls != 0 {
				t.Errorf("the refused write put %d times and deleted %d, want nothing", objects.puts, objects.calls)
			}
			if f, err := tier.store.Files.FindByName(s.ctx, s.db, dir.ID, "report.txt"); !errors.Is(err, blobfs.ErrNotFound) {
				t.Errorf("the refused begin left %+v, %v, want no row", f, err)
			}
		})
	}
}

// protocolEnsureCreates checks Ensure under a fixed id: the first call
// creates, stores, and reports stored; a second finds the row available
// and returns it as it stands, nothing put and stored false.
func (s *suite) protocolEnsureCreates(t *testing.T) {
	for _, tier := range s.storesUnder() {
		t.Run(tier.name, func(t *testing.T) {
			dir := s.mkdir(t, "ensure-write-"+t.Name())
			id := blobfs.NewID()
			objects := newObjectStore()
			got, stored, err := s.ensureWrite(tier.store, objects, dir.ID, "seed.txt", id, "seed")
			if err != nil || !stored || got.ID != id || got.Key != id+"/seed.txt" {
				t.Fatalf("the first Ensure = %+v, %v, %v, want the row created under the id and stored", got, stored, err)
			}
			s.wantWritten(t, objects, got, "seed")
			again, stored, err := s.ensureWrite(tier.store, objects, dir.ID, "seed.txt", id, "seed")
			if err != nil || stored || !equalFile(got, again) || objects.puts != 1 {
				t.Errorf("the second Ensure = %+v, %v, %v after %d puts, want the row as it stands, stored false, and one put", again, stored, err, objects.puts)
			}
		})
	}
}

// protocolEnsureResumes checks Ensure over the pending row an earlier
// write under the id left when it stopped before its put: the row resumed
// at its version and completed, stored true, and one row under the id.
func (s *suite) protocolEnsureResumes(t *testing.T) {
	for _, tier := range s.storesUnder() {
		t.Run(tier.name, func(t *testing.T) {
			dir := s.mkdir(t, "ensure-resume-"+t.Name())
			id := blobfs.NewID()
			pending, err := tier.store.Files.Create(s.ctx, s.db, acceptAll{}, dir.ID, "seed.txt", "text/plain", data.WithID(id))
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			objects := newObjectStore()
			got, stored, err := s.ensureWrite(tier.store, objects, dir.ID, "seed.txt", id, "seed")
			if err != nil || !stored || got.ID != id || got.Version != pending.Version+1 || !got.CreatedAt.Equal(pending.CreatedAt) {
				t.Fatalf("Ensure over the pending row = %+v, %v, %v, want it resumed and completed", got, stored, err)
			}
			s.wantWritten(t, objects, got, "seed")
			if n := s.count(t, s.db, "SELECT COUNT(*) FROM blobfs_file WHERE id = "+s.db.Dialect().Placeholder(1), id); n != 1 {
				t.Errorf("%d rows under the id, want one", n)
			}
		})
	}
}

// protocolEnsureNameTaken checks Ensure where a row under another id holds
// the name, pending or available: ErrNameTaken, the row untouched, and
// nothing put or deleted.
func (s *suite) protocolEnsureNameTaken(t *testing.T) {
	for _, tier := range s.storesUnder() {
		t.Run(tier.name, func(t *testing.T) {
			dir := s.mkdir(t, "ensure-taken-"+t.Name())
			for _, status := range []blobfs.Status{blobfs.StatusPending, blobfs.StatusAvailable} {
				n := "taken-" + status.String() + ".txt"
				before := s.file(t, s.insertFile(t, dir.ID, n, status))
				objects := newObjectStore()
				_, stored, err := s.ensureWrite(tier.store, objects, dir.ID, n, blobfs.NewID(), "seed")
				if !errors.Is(err, blobfs.ErrNameTaken) || stored {
					t.Errorf("Ensure over a %s row under another id = %v, %v, want ErrNameTaken", status, stored, err)
				}
				if objects.puts != 0 || objects.calls != 0 {
					t.Errorf("the refused Ensure put %d times and deleted %d, want nothing", objects.puts, objects.calls)
				}
				if after := s.file(t, before.ID); !equalFile(before, after) {
					t.Errorf("the refused Ensure changed the %s row to\n%+v\nfrom\n%+v", status, after, before)
				}
			}
		})
	}
}

// protocolEnsureDeleting checks Ensure over a row under the id whose own
// delete began: the file's DeletingError, the row untouched, and nothing
// put. The row is purged after.
func (s *suite) protocolEnsureDeleting(t *testing.T) {
	for _, tier := range s.storesUnder() {
		t.Run(tier.name, func(t *testing.T) {
			dir := s.mkdir(t, "ensure-deleting-"+t.Name())
			id := blobfs.NewID()
			if _, err := tier.store.Files.Create(s.ctx, s.db, acceptAll{}, dir.ID, "seed.txt", "text/plain", data.WithID(id)); err != nil {
				t.Fatalf("Create: %v", err)
			}
			deleting := s.beginDelete(t, id)
			objects := newObjectStore()
			_, stored, err := s.ensureWrite(tier.store, objects, dir.ID, "seed.txt", id, "seed")
			if !errors.Is(err, blobfs.ErrDeleting) || stored {
				t.Errorf("Ensure over a deleting row = %v, %v, want ErrDeleting", stored, err)
			}
			wantDeletingKind(t, err, false, id)
			if objects.puts != 0 || objects.calls != 0 {
				t.Errorf("the refused Ensure put %d times and deleted %d, want nothing", objects.puts, objects.calls)
			}
			if after := s.file(t, id); !equalFile(deleting, after) {
				t.Errorf("the refused Ensure changed the row to\n%+v\nfrom\n%+v", after, deleting)
			}
			s.purge(t, id)
		})
	}
}

// protocolRemove checks Remove of an available file: the object deleted
// under the row's key, then the row purged.
func (s *suite) protocolRemove(t *testing.T) {
	for _, tier := range s.storesUnder() {
		t.Run(tier.name, func(t *testing.T) {
			dir := s.mkdir(t, "remove-"+t.Name())
			objects := newObjectStore()
			f, err := s.writeFile(tier.store, objects, dir.ID, "report.txt", "report")
			if err != nil {
				t.Fatalf("Write: %v", err)
			}
			if err := s.removeFile(tier.store, objects, f.ID); err != nil {
				t.Fatalf("Remove: %v", err)
			}
			if _, ok := objects.objects[f.Key]; ok || objects.deleted[f.Key] != 1 || len(objects.deleted) != 1 {
				t.Errorf("Remove deleted %v, want the row's key %s once", objects.deleted, f.Key)
			}
			s.wantFilesGone(t, f.ID)
		})
	}
}

// protocolRemoveFailedDelete checks Remove whose object delete fails: the
// store's error, the row left deleting, and a retry that converges.
func (s *suite) protocolRemoveFailedDelete(t *testing.T) {
	for _, tier := range s.storesUnder() {
		t.Run(tier.name, func(t *testing.T) {
			dir := s.mkdir(t, "remove-failed-"+t.Name())
			id := s.insertFile(t, dir.ID, "report.txt", blobfs.StatusAvailable)
			objects := newObjectStore()
			objects.failAt = 1
			if err := s.removeFile(tier.store, objects, id); !errors.Is(err, errObjectStore) {
				t.Fatalf("Remove under a failing delete = %v, want the store's error", err)
			}
			f := s.file(t, id)
			if f.Status != blobfs.StatusDeleting {
				t.Errorf("the failed Remove left the row %s, want deleting", f.Status)
			}
			if err := s.removeFile(tier.store, objects, id); err != nil {
				t.Fatalf("the retried Remove: %v", err)
			}
			if objects.deleted[f.Key] != 1 || objects.calls != 2 {
				t.Errorf("the object was deleted %d times in %d calls, want once in two", objects.deleted[f.Key], objects.calls)
			}
			s.wantFilesGone(t, id)
		})
	}
}

// protocolRemoveRefusedPick checks a pick that refuses: its error returned
// as it came, nothing deleted, and the row as it was.
func (s *suite) protocolRemoveRefusedPick(t *testing.T) {
	for _, tier := range s.storesUnder() {
		t.Run(tier.name, func(t *testing.T) {
			dir := s.mkdir(t, "remove-refused-"+t.Name())
			before := s.file(t, s.insertFile(t, dir.ID, "report.txt", blobfs.StatusAvailable))
			objects := newObjectStore()
			err := tier.store.Remove(s.ctx, s.db, objects, func(*sqlate.Tx) (string, error) { return before.ID, errBegin })
			if !errors.Is(err, errBegin) || strings.Contains(err.Error(), "data: ") {
				t.Errorf("Remove under a refusing pick = %v, want its error as it came", err)
			}
			if objects.calls != 0 {
				t.Errorf("the refused Remove deleted %d objects, want none", objects.calls)
			}
			if after := s.file(t, before.ID); !equalFile(before, after) {
				t.Errorf("the refused Remove changed the row to\n%+v\nfrom\n%+v", after, before)
			}
		})
	}
}

// protocolPurge checks Purge after a Files.Delete the caller ran in its
// own transaction: the object deleted and the row purged, and a retry of
// the purge that converges.
func (s *suite) protocolPurge(t *testing.T) {
	for _, tier := range s.storesUnder() {
		t.Run(tier.name, func(t *testing.T) {
			dir := s.mkdir(t, "purge-"+t.Name())
			objects := newObjectStore()
			f, err := s.writeFile(tier.store, objects, dir.ID, "report.txt", "report")
			if err != nil {
				t.Fatalf("Write: %v", err)
			}
			deleting, err := s.db.Transact(s.ctx, func(tx *sqlate.Tx) (blobfs.File, error) {
				return tier.store.Files.Delete(s.ctx, tx, f.ID)
			})
			if err != nil {
				t.Fatalf("Files.Delete: %v", err)
			}
			if err := tier.store.Purge(s.ctx, s.db, objects, deleting); err != nil {
				t.Fatalf("Purge: %v", err)
			}
			if _, ok := objects.objects[f.Key]; ok || objects.deleted[f.Key] != 1 {
				t.Errorf("Purge deleted %v, want the row's key %s", objects.deleted, f.Key)
			}
			s.wantFilesGone(t, f.ID)
			if err := tier.store.Purge(s.ctx, s.db, objects, deleting); err != nil {
				t.Errorf("the retried Purge = %v, want the same success", err)
			}
		})
	}
}

// passes records what SweepUntilDone reported, and ran counts the passes
// the loop ran.
type passes struct {
	results []data.SweepResult
	errs    []error
	ran     int
}

func (p *passes) report(r data.SweepResult, err error) {
	p.results = append(p.results, r)
	p.errs = append(p.errs, err)
}

// pass is one pass of store's sweep through objects with opts, counted in
// ran, as a consumer hands it to SweepUntilDone.
func (p *passes) pass(s *suite, store *data.Store, objects *objectStore, opts ...data.SweepOption) func(context.Context) (data.SweepResult, error) {
	return func(ctx context.Context) (data.SweepResult, error) {
		p.ran++
		return store.Sweep(ctx, s.db, objects, opts...)
	}
}

// protocolSweepUntilDone checks that SweepUntilDone drains a backlog
// larger than one batch: a branch of eight records at Batch(3) takes the
// three passes sweepBatch counts, the loop runs each once, report sees
// each with no error, and the branch is gone with each object deleted once.
func (s *suite) protocolSweepUntilDone(t *testing.T) {
	want := []data.SweepResult{{Files: 3, More: true}, {Files: 2, Directories: 1, More: true}, {Directories: 2}}
	for _, tier := range s.storesUnder() {
		t.Run(tier.name, func(t *testing.T) {
			b := s.newBranch(t, "until-done-"+t.Name())
			keys := s.keys(t, branchFiles(b)...)
			if _, err := s.mark(tier.store, b.top.ID); err != nil {
				t.Fatalf("MarkDeleting: %v", err)
			}
			objects := newObjectStore()
			var got passes
			if err := data.SweepUntilDone(s.ctx, nil, got.pass(s, tier.store, objects, data.Batch(3)), got.report); err != nil {
				t.Fatalf("SweepUntilDone: %v", err)
			}
			if !slices.Equal(got.results, want) || !slices.Equal(got.errs, make([]error, len(want))) || got.ran != len(want) {
				t.Errorf("SweepUntilDone reported %+v, %v over %d passes, want %+v, no error, and each pass run once", got.results, got.errs, got.ran, want)
			}
			wantDeletedOnce(t, objects, keys)
			s.wantDirectoriesGone(t, b.top.ID, b.mid.ID, b.leaf.ID)
			s.wantFilesGone(t, branchFiles(b)...)
		})
	}
}

// protocolSweepUntilDoneStopped checks that with stop closed before the
// loop starts, the loop runs no pass: nothing run, reported, or deleted,
// and the branch still marked, which an unstopped loop then drains.
func (s *suite) protocolSweepUntilDoneStopped(t *testing.T) {
	for _, tier := range s.storesUnder() {
		t.Run(tier.name, func(t *testing.T) {
			b := s.newBranch(t, "until-stopped-"+t.Name())
			if _, err := s.mark(tier.store, b.top.ID); err != nil {
				t.Fatalf("MarkDeleting: %v", err)
			}
			stop := make(chan struct{})
			close(stop)
			objects := newObjectStore()
			var got passes
			if err := data.SweepUntilDone(s.ctx, stop, got.pass(s, tier.store, objects), got.report); err != nil || len(got.results) != 0 || got.ran != 0 || objects.calls != 0 {
				t.Errorf("SweepUntilDone with a closed stop = %v after %d passes reported and %d run, %d deletes, want nothing run", err, len(got.results), got.ran, objects.calls)
			}
			if d := s.directory(t, b.top.ID); d.Status != blobfs.DirectoryStatusDeleting {
				t.Errorf("the stopped loop left the branch %s, want deleting", d.Status)
			}
			got = passes{}
			if err := data.SweepUntilDone(s.ctx, nil, got.pass(s, tier.store, objects), got.report); err != nil || len(got.results) != 1 ||
				got.results[0] != (data.SweepResult{Files: 5, Directories: 3}) {
				t.Errorf("the unstopped SweepUntilDone = %v after %+v, want the branch in one pass", err, got.results)
			}
			s.wantDirectoriesGone(t, b.top.ID, b.mid.ID, b.leaf.ID)
		})
	}
}

// Check the fake is the object store the protocols take.
var _ data.ObjectStore = (*objectStore)(nil)
