package datatest

import (
	"errors"
	"sync"
	"testing"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data"
)

// writes checks the write protocol: Create or Ensure, then Complete.
func (s *suite) writes(t *testing.T) {
	t.Run("CreateInsertsThePendingRow", s.createFile)
	t.Run("CreateRefusals", s.createFileRefusals)
	t.Run("Ensure", s.ensureFile)
	t.Run("EnsureConcurrent", s.ensureFileConcurrent)
	t.Run("CompleteMakesTheRowAvailable", s.completeFile)
	t.Run("CompleteRefusals", s.completeFileRefusals)
	t.Run("InsideTheCallersTransaction", s.writeInTransaction)
	t.Run("RetryCompletesAPendingRow", s.retryWrite)
	t.Run("SuppliedIDs", s.suppliedFileIDs)
}

// createFile checks Create against the baseline: the pending row with the
// key built from the id and the name, the declared content type, no size
// or entity tag, and the table's defaults; the row the database holds;
// the name stored composed; and the same shape through both stores.
func (s *suite) createFile(t *testing.T) {
	dir := s.mkdir(t, "create-file-"+t.Name())
	id := blobfs.NewID()
	got, err := s.store.Files.Create(s.ctx, s.db, acceptAll{}, dir.ID, decomposed+".txt", "text/plain", data.WithID(id))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.ID != id || got.DirectoryID != dir.ID || got.Name != composed+".txt" || got.Status != blobfs.StatusPending ||
		got.Key != id+"/"+composed+".txt" || got.Size != nil || got.ContentType != "text/plain" || got.ETag != nil || got.Version != 1 ||
		got.CreatedAt.IsZero() || !got.UpdatedAt.Equal(got.CreatedAt) {
		t.Errorf("Create returned %+v, want the pending row at version 1 with the table's defaults", got)
	}
	if stored := s.file(t, id); !equalFile(got, stored) {
		t.Errorf("Create returned\n%+v\nbut the database holds\n%+v", got, stored)
	}
	base, err := s.baseline.Files.Create(s.ctx, s.db, acceptAll{}, dir.ID, "base.txt", "text/plain")
	if err != nil {
		t.Fatalf("the baseline's Create: %v", err)
	}
	if !sameFileShape(got, base) {
		t.Errorf("the row\n%+v\ndiffers in shape from the baseline's\n%+v", got, base)
	}
}

// createFileRefusals checks Create's and Ensure's refusals against the
// baseline: a name taken by a row of any status, a missing directory, and
// a taken id, each by its sentinel over its constraint in the same text
// on both stores, Ensure finding the taken name instead of refusing it;
// and the refusals before any SQL, a refused name, a refused id, and a
// key the store refuses, which leave no row.
func (s *suite) createFileRefusals(t *testing.T) {
	dir := s.mkdir(t, "create-file-refusals-"+t.Name())
	taken := s.insertFile(t, dir.ID, "taken.txt", blobfs.StatusAvailable)
	s.insertFile(t, dir.ID, "deleting.txt", blobfs.StatusDeleting)
	for _, c := range []struct {
		name       string
		dir, file  string
		opts       []data.CreateOption
		want       error
		constraint string
	}{
		{"NameTaken", dir.ID, "taken.txt", nil, blobfs.ErrNameTaken, blobfs.ConstraintUniqueFileDirectoryName},
		{"NameTakenByADeletingRow", dir.ID, "deleting.txt", nil, blobfs.ErrNameTaken, blobfs.ConstraintUniqueFileDirectoryName},
		{"MissingDirectory", blobfs.NewID(), "orphan.txt", nil, blobfs.ErrNotFound, blobfs.ConstraintForeignKeyFileDirectory},
		{"IDTaken", dir.ID, "twin.txt", []data.CreateOption{data.WithID(taken)}, blobfs.ErrIDTaken, blobfs.ConstraintPrimaryKeyFile},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := s.store.Files.Create(s.ctx, s.db, acceptAll{}, c.dir, c.file, "text/plain", c.opts...)
			wantViolation(t, err, c.want, c.constraint)
			_, base := s.baseline.Files.Create(s.ctx, s.db, acceptAll{}, c.dir, c.file, "text/plain", c.opts...)
			wantSameError(t, err, base)
			found, outcome, err := s.store.Files.Ensure(s.ctx, s.db, acceptAll{}, c.dir, c.file, "text/plain", c.opts...)
			_, _, base = s.baseline.Files.Ensure(s.ctx, s.db, acceptAll{}, c.dir, c.file, "text/plain", c.opts...)
			if c.want == blobfs.ErrNameTaken {
				// The lookup runs first, so the taken name is found.
				if err != nil || base != nil || outcome != data.WritePresent || found.Name != c.file {
					t.Errorf("Ensure of a taken name = %+v, %s, %v and %v on the baseline, want the row present", found, outcome, err, base)
				}
				return
			}
			wantViolation(t, err, c.want, c.constraint)
			wantSameError(t, err, base)
		})
	}
	t.Run("BeforeSQL", func(t *testing.T) {
		for _, c := range []struct {
			file string
			keys blobfs.KeyValidator
			opts []data.CreateOption
			want error
		}{
			{"a/b.txt", acceptAll{}, nil, blobfs.ErrInvalidName},
			{"", acceptAll{}, nil, blobfs.ErrInvalidName},
			{"bad-id.txt", acceptAll{}, []data.CreateOption{data.WithID(blobfs.RootID)}, blobfs.ErrInvalidID},
			{"bad-id.txt", acceptAll{}, []data.CreateOption{data.WithID("not-a-uuid")}, blobfs.ErrInvalidID},
			{"refused-key.txt", refuseAll{}, nil, blobfs.ErrInvalidKey},
		} {
			_, err := s.store.Files.Create(s.ctx, s.db, c.keys, dir.ID, c.file, "text/plain", c.opts...)
			if !errors.Is(err, c.want) {
				t.Errorf("Create(%q) = %v, want %v", c.file, err, c.want)
			}
			_, _, err = s.store.Files.Ensure(s.ctx, s.db, c.keys, dir.ID, c.file, "text/plain", c.opts...)
			if !errors.Is(err, c.want) {
				t.Errorf("Ensure(%q) = %v, want %v", c.file, err, c.want)
			}
		}
		if _, err := s.store.Files.Create(s.ctx, s.db, refuseAll{}, dir.ID, "refused-key.txt", "text/plain"); !errors.Is(err, errRefusedKey) {
			t.Errorf("a refused key = %v, want the store's own reason reachable", err)
		}
		if n := s.count(t, s.db, "SELECT COUNT(*) FROM blobfs_file WHERE directory_id = "+s.db.Dialect().Placeholder(1), dir.ID); n != 2 {
			t.Errorf("%d files in the directory after the refusals, want the two seeded", n)
		}
	})
}

// ensureFile checks Ensure's outcomes on both stores: a free name created
// pending under a supplied id; the same name, in the other spelling and
// under another supplied id, resumed as the same row; once the write
// completes the name present as available; once a delete begins present
// as deleting; and the directory holding one row throughout.
func (s *suite) ensureFile(t *testing.T) {
	dir := s.mkdir(t, "ensure-file-"+t.Name())
	for _, tier := range []struct {
		name  string
		store *data.Store
	}{{"UnderTest", s.store}, {"Baseline", s.baseline}} {
		t.Run(tier.name, func(t *testing.T) {
			id := blobfs.NewID()
			n := decomposed + "-" + tier.name + ".txt"
			created, outcome, err := tier.store.Files.Ensure(s.ctx, s.db, acceptAll{}, dir.ID, n, "text/plain", data.WithID(id))
			if err != nil || outcome != data.WriteCreated || created.ID != id || created.Status != blobfs.StatusPending || created.Key != id+"/"+blobfs.NormalizeName(n) {
				t.Fatalf("the first Ensure = %+v, %s, %v; want the pending row created under the id", created, outcome, err)
			}
			resumed, outcome, err := tier.store.Files.Ensure(s.ctx, s.db, acceptAll{}, dir.ID, blobfs.NormalizeName(n), "text/plain", data.WithID(blobfs.NewID()))
			if err != nil || outcome != data.WriteResumed || !equalFile(created, resumed) {
				t.Fatalf("the second Ensure = %+v, %s, %v; want the same pending row resumed", resumed, outcome, err)
			}
			completed, err := tier.store.Files.Complete(s.ctx, s.db, resumed.ID, resumed.Version, object)
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			present, outcome, err := tier.store.Files.Ensure(s.ctx, s.db, acceptAll{}, dir.ID, n, "text/plain")
			if err != nil || outcome != data.WritePresent || !equalFile(completed, present) {
				t.Fatalf("Ensure over the available row = %+v, %s, %v; want the row as it is", present, outcome, err)
			}
			deleting := s.beginDelete(t, id)
			present, outcome, err = tier.store.Files.Ensure(s.ctx, s.db, acceptAll{}, dir.ID, n, "text/plain")
			if err != nil || outcome != data.WritePresent || !equalFile(deleting, present) {
				t.Errorf("Ensure over the deleting row = %+v, %s, %v; want the row as it is", present, outcome, err)
			}
			if n := s.count(t, s.db, "SELECT COUNT(*) FROM blobfs_file WHERE id = "+s.db.Dialect().Placeholder(1), id); n != 1 {
				t.Errorf("%d rows under the id, want one", n)
			}
		})
	}
}

// ensureFileConcurrent checks the race on the pool, forced as the
// directory's is: two callers write the same free name at once through a
// pool that holds each caller's lookup until both have looked, so both
// insert and the second insert is refused; the second caller recovers by
// looking the row up. Exactly one creates the pending row, the other
// resumes it, both return the same row, and the lookup runs a third time.
func (s *suite) ensureFileConcurrent(t *testing.T) {
	dir := s.mkdir(t, "ensure-file-concurrent-"+t.Name())
	pool := s.racingPool(t, "file_by_name")
	var (
		start    sync.WaitGroup
		done     sync.WaitGroup
		mu       sync.Mutex
		results  []blobfs.File
		outcomes []data.WriteOutcome
	)
	start.Add(1)
	for range 2 {
		done.Go(func() {
			start.Wait()
			f, outcome, err := s.store.Files.Ensure(s.ctx, pool, acceptAll{}, dir.ID, "shared.txt", "text/plain")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Errorf("Ensure: %v", err)
				return
			}
			results = append(results, f)
			outcomes = append(outcomes, outcome)
		})
	}
	start.Done()
	done.Wait()
	if len(results) != 2 || !equalFile(results[0], results[1]) {
		t.Fatalf("the two callers got %+v; want the same row", results)
	}
	created := 0
	for _, o := range outcomes {
		switch o {
		case data.WriteCreated:
			created++
		case data.WriteResumed:
		default:
			t.Errorf("an outcome is %s; want created or resumed", o)
		}
	}
	if created != 1 {
		t.Errorf("%d callers created the row, want one", created)
	}
	pool.wantRecovered(t)
}

// completeFile checks Complete against the baseline: the row moved to
// available at the next version with the object's size, content type, and
// entity tag, updated_at stamped, and the key, the name, and created_at
// unchanged; the row the database holds; and the same shape through both
// stores.
func (s *suite) completeFile(t *testing.T) {
	dir := s.mkdir(t, "complete-"+t.Name())
	pending, err := s.store.Files.Create(s.ctx, s.db, acceptAll{}, dir.ID, "complete.txt", "application/octet-stream")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.store.Files.Complete(s.ctx, s.db, pending.ID, pending.Version, object)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got.Status != blobfs.StatusAvailable || got.Version != pending.Version+1 || got.Size == nil || *got.Size != object.Size ||
		got.ContentType != object.ContentType || got.ETag == nil || *got.ETag != object.ETag || !got.UpdatedAt.After(pending.UpdatedAt) ||
		!got.CreatedAt.Equal(pending.CreatedAt) || got.Key != pending.Key || got.Name != pending.Name {
		t.Errorf("Complete returned %+v, want the available row at the next version carrying the object", got)
	}
	if stored := s.file(t, pending.ID); !equalFile(got, stored) {
		t.Errorf("Complete returned\n%+v\nbut the database holds\n%+v", got, stored)
	}
	basePending, err := s.baseline.Files.Create(s.ctx, s.db, acceptAll{}, dir.ID, "complete-base.txt", "application/octet-stream")
	if err != nil {
		t.Fatalf("the baseline's Create: %v", err)
	}
	base, err := s.baseline.Files.Complete(s.ctx, s.db, basePending.ID, basePending.Version, object)
	if err != nil {
		t.Fatalf("the baseline's Complete: %v", err)
	}
	if !sameFileShape(got, base) {
		t.Errorf("the completed row\n%+v\ndiffers in shape from the baseline's\n%+v", got, base)
	}
}

// completeFileRefusals checks Complete's refusals against the baseline,
// each leaving the row unchanged: a missing row, a stale version, a row
// already available (an invalid transition), and a deleting row, at its
// own version and at the pending version the writer read before a
// concurrent Delete advanced it, which is ErrDeleting and never a version
// mismatch.
func (s *suite) completeFileRefusals(t *testing.T) {
	dir := s.mkdir(t, "complete-refusals-"+t.Name())
	available, err := s.store.Files.Create(s.ctx, s.db, acceptAll{}, dir.ID, "refused.txt", "text/plain")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if available, err = s.store.Files.Complete(s.ctx, s.db, available.ID, available.Version, object); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	pending, err := s.store.Files.Create(s.ctx, s.db, acceptAll{}, dir.ID, "deleting.txt", "text/plain")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	deleting := s.beginDelete(t, pending.ID)
	missing := blobfs.NewID()
	for _, c := range []struct {
		name    string
		id      string
		version int64
		want    error
		not     error
	}{
		{"NotFound", missing, 1, blobfs.ErrNotFound, query.ErrVersionMismatch},
		{"VersionMismatch", available.ID, available.Version - 1, query.ErrVersionMismatch, blobfs.ErrInvalidTransition},
		{"AlreadyAvailable", available.ID, available.Version, blobfs.ErrInvalidTransition, blobfs.ErrDeleting},
		{"Deleting", deleting.ID, deleting.Version, blobfs.ErrDeleting, query.ErrVersionMismatch},
		{"DeletingAtThePendingVersion", deleting.ID, pending.Version, blobfs.ErrDeleting, query.ErrVersionMismatch},
	} {
		t.Run(c.name, func(t *testing.T) {
			var before blobfs.File
			if c.id != missing {
				before = s.file(t, c.id)
			}
			_, err := s.store.Files.Complete(s.ctx, s.db, c.id, c.version, object)
			if !errors.Is(err, c.want) || errors.Is(err, c.not) {
				t.Errorf("Complete = %v, want %v and not %v", err, c.want, c.not)
			}
			_, base := s.baseline.Files.Complete(s.ctx, s.db, c.id, c.version, object)
			wantSameError(t, err, base)
			if c.id != missing {
				if after := s.file(t, c.id); !equalFile(before, after) {
					t.Errorf("the refused complete changed the row to\n%+v\nfrom\n%+v", after, before)
				}
			}
		})
	}
}

// writeInTransaction checks the write composes into the caller's
// transaction beside the caller's own row: the caller inserts a row that
// references the pending file in the same transaction, a rollback leaves
// neither row, and a commit leaves both. The foreign key holds against
// the pending row inside the transaction, so a consumer references a file
// before its object exists.
func (s *suite) writeInTransaction(t *testing.T) {
	s.createFileReferences(t)
	dir := s.mkdir(t, "in-transaction-"+t.Name())
	abort := errors.New("the consumer changed its mind")
	id := blobfs.NewID()
	write := func(fail bool) (blobfs.File, error) {
		return s.db.Transact(s.ctx, func(tx *sqlate.Tx) (blobfs.File, error) {
			f, err := s.store.Files.Create(s.ctx, tx, acceptAll{}, dir.ID, "plan.txt", "text/plain", data.WithID(id))
			if err != nil {
				return blobfs.File{}, err
			}
			s.reference(t, tx, f.ID)
			if fail {
				return blobfs.File{}, abort
			}
			return f, nil
		})
	}
	if _, err := write(true); !errors.Is(err, abort) {
		t.Fatalf("the aborted transaction = %v, want the consumer's error", err)
	}
	if _, err := s.store.Files.Find(s.ctx, s.db, id); !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("after the rollback the file row is there: %v", err)
	}
	if n := s.references(t, id); n != 0 {
		t.Errorf("after the rollback %d references exist, want none", n)
	}
	f, err := write(false)
	if err != nil {
		t.Fatalf("the committed transaction: %v", err)
	}
	if stored := s.file(t, f.ID); stored.Status != blobfs.StatusPending {
		t.Errorf("after the commit the file is %s, want pending", stored.Status)
	}
	if n := s.references(t, id); n != 1 {
		t.Errorf("after the commit %d references exist, want one", n)
	}
	s.unreference(t, id)
}

// retryWrite checks the stop between the steps: a write whose caller
// stops before the object is stored leaves a pending row; a later caller
// finds the row by name and completes it at the version it read, and the
// directory then holds the one row, available, with the retry's facts.
func (s *suite) retryWrite(t *testing.T) {
	dir := s.mkdir(t, "retry-"+t.Name())
	first, err := s.store.Files.Create(s.ctx, s.db, acceptAll{}, dir.ID, "report.pdf", "application/pdf")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// The first caller stops here.
	pending, err := s.store.Files.FindByName(s.ctx, s.db, dir.ID, "report.pdf")
	if err != nil || pending.ID != first.ID || pending.Status != blobfs.StatusPending {
		t.Fatalf("the retry found %+v, %v, want the first caller's pending row", pending, err)
	}
	obj := blobfs.Object{Size: 3, ContentType: "application/pdf", ETag: `"r"`}
	done, err := s.store.Files.Complete(s.ctx, s.db, pending.ID, pending.Version, obj)
	if err != nil {
		t.Fatalf("Complete from the retry: %v", err)
	}
	if done.Status != blobfs.StatusAvailable || done.Version != 2 || done.ID != first.ID || *done.Size != 3 {
		t.Errorf("the retry completed %+v", done)
	}
	if n := s.count(t, s.db, "SELECT COUNT(*) FROM blobfs_file WHERE directory_id = "+s.db.Dialect().Placeholder(1), dir.ID); n != 1 {
		t.Errorf("the directory holds %d rows, want the one", n)
	}
}

// suppliedFileIDs checks a caller-supplied id through the whole protocol:
// the row and its key under the id, a delete that frees the id, and a
// write under the same id and name that reuses the same key.
func (s *suite) suppliedFileIDs(t *testing.T) {
	dir := s.mkdir(t, "supplied-"+t.Name())
	id := blobfs.NewID()
	f, err := s.store.Files.Create(s.ctx, s.db, acceptAll{}, dir.ID, "fixture.txt", "text/plain", data.WithID(id))
	if err != nil || f.ID != id || f.Key != id+"/fixture.txt" {
		t.Fatalf("Create with an id = %+v, %v; want the row and the key under the id", f, err)
	}
	s.beginDelete(t, id)
	s.purge(t, id)
	again, err := s.store.Files.Create(s.ctx, s.db, acceptAll{}, dir.ID, "fixture.txt", "text/plain", data.WithID(id))
	if err != nil || again.ID != id || again.Key != f.Key || again.Version != 1 {
		t.Errorf("Create under the freed id = %+v, %v; want a fresh row under the same id and key", again, err)
	}
}
