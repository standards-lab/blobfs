package data_test

import (
	"context"
	"database/sql/driver"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/sqltest"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data"
)

// objectLog is an ObjectDeleter that records every key it is asked to
// delete, and fails with err when err is set.
type objectLog struct {
	keys []string
	err  error
}

func (o *objectLog) DeleteObject(_ context.Context, key string) error {
	o.keys = append(o.keys, key)
	return o.err
}

// files scripts a page of file rows, each deleting at version 2, in dir.
func files(dir string, ids ...string) sqltest.Response {
	r := sqltest.Response{Columns: fileColumns}
	for _, id := range ids {
		r.Rows = append(r.Rows, fileIn(id, dir, strings.ToLower(id)+".txt", blobfs.StatusDeleting, 2).Rows...)
	}
	return r
}

// deletingRoot scripts the roots' read returning D, deleting at version 2
// under the root.
func deletingRoot() sqltest.Response {
	return directoryIn("D", blobfs.RootID, "d", blobfs.DirectoryStatusDeleting, 2)
}

// remark scripts the mark a pass repeats on a branch marked already: no
// directory changed, the read of the root, and no file changed.
func remark() []sqltest.Response {
	return []sqltest.Response{{Affected: 0}, deletingRoot(), {Affected: 0}}
}

// TestSweepRefusesBeforeSQL proves a Batch below 1 and a PendingOlderThan
// age that is not positive are refused before any SQL.
func TestSweepRefusesBeforeSQL(t *testing.T) {
	ctx := context.Background()
	for _, opt := range []data.SweepOption{data.Batch(0), data.Batch(-1), data.PendingOlderThan(0), data.PendingOlderThan(-time.Hour)} {
		s, db, rec := openStore(t, fallback)
		if _, err := s.Sweep(ctx, db, &objectLog{}, opt); err == nil || !strings.HasPrefix(err.Error(), "data: sweep: ") {
			t.Errorf("Sweep with a refused option = %v, want a refusal", err)
		}
		if n := len(rec.Calls()); n != 0 {
			t.Errorf("the refused option ran %d calls", n)
		}
	}
}

// TestSweepNothingToDo proves a pass with no branch being deleted is the
// roots' read alone and a zero result; with PendingOlderThan it is that
// read and the read of the pending rows, oldest first, before an instant
// the age before now, from offset 0 to the batch.
func TestSweepNothingToDo(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback, noDirectory())
	objects := &objectLog{}
	got, err := s.Sweep(ctx, db, objects)
	if err != nil || got != (data.SweepResult{}) {
		t.Errorf("Sweep = %+v, %v, want a zero result", got, err)
	}
	if ops := ops(rec); ops != "query" || len(objects.keys) != 0 {
		t.Errorf("the empty pass ran %q and deleted %v, want the one read and nothing", ops, objects.keys)
	}

	s, db, rec = openStore(t, fallback, noDirectory(), noFile())
	start := time.Now()
	got, err = s.Sweep(ctx, db, objects, data.PendingOlderThan(time.Hour), data.Batch(7))
	if err != nil || got != (data.SweepResult{}) {
		t.Errorf("Sweep with PendingOlderThan = %+v, %v, want a zero result", got, err)
	}
	calls := queries(rec)
	if len(calls) != 2 || !slices.Equal(calls[0].Args, []any{0, 7}) {
		t.Fatalf("the pass ran %v, want the roots' read to the batch and the pending read", calls)
	}
	pending := calls[1]
	if !strings.Contains(pending.SQL, "WHERE f.status = 'pending' AND f.updated_at < CAST($1 AS timestamp with time zone)\nORDER BY f.updated_at, f.id") {
		t.Errorf("the pending read is not the statement over the pending rows by age:\n%s", pending.SQL)
	}
	if len(pending.Args) != 3 || pending.Args[1] != 0 || pending.Args[2] != 7 {
		t.Fatalf("the pending read bound %v, want the instant, offset 0, and the batch", pending.Args)
	}
	before, ok := pending.Args[0].(time.Time)
	if want := start.Add(-time.Hour); !ok || before.Before(want.Add(-time.Minute)) || before.After(want.Add(time.Minute)) {
		t.Errorf("the pending read bound the instant %v, want about %v", pending.Args[0], want)
	}
}

// TestSweepABranch proves a pass over a branch of one directory holding
// one file: the mark repeated in its own transaction; the file listed
// with IncludeDeleting, its object deleted and its row purged; the child
// directories listed; and the directory removed in a transaction of its
// own, the hook first and then the removal at the version the pass read.
func TestSweepABranch(t *testing.T) {
	ctx := context.Background()
	responses := append([]sqltest.Response{deletingRoot()}, remark()...)
	responses = append(responses,
		files("D", "F"),
		sqltest.Response{Affected: 1},
		noDirectory(),
		sqltest.Response{Affected: 1},
	)
	s, db, rec := openStore(t, fallback, responses...)
	objects := &objectLog{}
	var hooked []string
	hook := func(_ context.Context, tx *sqlate.Tx, dir blobfs.Directory) error {
		if tx == nil {
			t.Error("the hook ran outside a transaction")
		}
		hooked = append(hooked, dir.ID)
		return nil
	}
	got, err := s.Sweep(ctx, db, objects, data.OnRemoveDirectory(hook))
	if err != nil || got != (data.SweepResult{Files: 1, Directories: 1}) {
		t.Fatalf("Sweep = %+v, %v, want one file and one directory", got, err)
	}
	if want := "query begin exec query exec commit query exec query begin exec commit"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
	if !slices.Equal(objects.keys, []string{"F/f.txt"}) || !slices.Equal(hooked, []string{"D"}) {
		t.Errorf("the pass deleted %v and hooked %v, want F's key and D", objects.keys, hooked)
	}
	purge := callsTo(rec, "DELETE FROM blobfs_file")
	remove := callsTo(rec, "DELETE FROM blobfs_directory")
	if len(purge) != 1 || !slices.Equal(purge[0].Args, []any{"F"}) {
		t.Errorf("the purge ran %v, want F's", purge)
	}
	if len(remove) != 1 || !slices.Equal(remove[0].Args, []any{"D", int64(2)}) || !strings.Contains(remove[0].SQL, "version = CAST($2 AS bigint)") {
		t.Errorf("the removal ran %v, want D's at the version read", remove)
	}
}

// TestSweepBatch proves the bound: a pass of one record deletes the first
// file of a page that holds more, stops, and reads whether any branch is
// still being deleted, which it reports as More.
func TestSweepBatch(t *testing.T) {
	ctx := context.Background()
	responses := append([]sqltest.Response{deletingRoot()}, remark()...)
	responses = append(responses, files("D", "F", "G"), sqltest.Response{Affected: 1}, deletingRoot())
	s, db, rec := openStore(t, fallback, responses...)
	objects := &objectLog{}
	got, err := s.Sweep(ctx, db, objects, data.Batch(1))
	if err != nil || got != (data.SweepResult{Files: 1, More: true}) {
		t.Fatalf("Sweep = %+v, %v, want one file and More", got, err)
	}
	if !slices.Equal(objects.keys, []string{"F/f.txt"}) {
		t.Errorf("the pass deleted %v, want F's key alone", objects.keys)
	}
	calls := queries(rec)
	if last := calls[len(calls)-1]; !slices.Equal(last.Args, []any{0, 1}) || !strings.Contains(last.SQL, "d.status = 'deleting' AND p.status = 'active'") {
		t.Errorf("the pass ended with %v, want the read of one root", last)
	}
}

// TestSweepStops proves the three ways a pass stops in a branch: an
// object delete's error stops it before the purge, with the error
// returned; a hook's error rolls the removal back, with the error
// returned; and a file that is not deleting, a straggler that landed
// after the mark, stops the branch with More and no error.
func TestSweepStops(t *testing.T) {
	ctx := context.Background()
	errStore := errors.New("the store is down")
	responses := append([]sqltest.Response{deletingRoot()}, remark()...)
	s, db, rec := openStore(t, fallback, append(responses, files("D", "F"))...)
	got, err := s.Sweep(ctx, db, &objectLog{err: errStore})
	if !errors.Is(err, errStore) || got != (data.SweepResult{}) {
		t.Errorf("Sweep over a failing store = %+v, %v, want the store's error", got, err)
	}
	if n := len(callsTo(rec, "DELETE")); n != 0 {
		t.Errorf("the failed object delete was followed by %d deletes", n)
	}

	errHook := errors.New("the owner row is locked")
	s, db, rec = openStore(t, fallback, append(responses, files("D"), noDirectory())...)
	got, err = s.Sweep(ctx, db, &objectLog{}, data.OnRemoveDirectory(func(context.Context, *sqlate.Tx, blobfs.Directory) error { return errHook }))
	if !errors.Is(err, errHook) || got != (data.SweepResult{}) {
		t.Errorf("Sweep under a failing hook = %+v, %v, want the hook's error", got, err)
	}
	if want := "query begin exec query exec commit query query begin rollback"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}

	straggler := fileIn("S", "D", "s.txt", blobfs.StatusAvailable, 1)
	s, db, rec = openStore(t, fallback, append(responses, straggler)...)
	objects := &objectLog{}
	got, err = s.Sweep(ctx, db, objects)
	if err != nil || got != (data.SweepResult{More: true}) || len(objects.keys) != 0 {
		t.Errorf("Sweep over a straggler = %+v, %v, deleted %v, want More and nothing done", got, err, objects.keys)
	}
	if n := len(callsTo(rec, "DELETE")); n != 0 {
		t.Errorf("the straggler was followed by %d deletes", n)
	}
}

// TestSweepPending proves the reclaim of a pending row: moved to deleting
// in a transaction of its own at the version read, its object deleted,
// its row purged; a row that moved on since the read is skipped.
func TestSweepPending(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	pending := sqltest.Response{Columns: fileColumns, Rows: [][]driver.Value{
		{"P", "D", "p.txt", "pending", "P/p.txt", nil, "text/plain", nil, int64(1), now, now},
		{"Q", "D", "q.txt", "pending", "Q/q.txt", nil, "text/plain", nil, int64(1), now, now},
	}}
	s, db, rec := openStore(t, fallback,
		noDirectory(),
		pending,
		sqltest.Response{Affected: 1}, fileIn("P", "D", "p.txt", blobfs.StatusDeleting, 2),
		sqltest.Response{Affected: 1},
		sqltest.Response{Affected: 0}, fileIn("Q", "D", "q.txt", blobfs.StatusAvailable, 2),
	)
	objects := &objectLog{}
	got, err := s.Sweep(ctx, db, objects, data.PendingOlderThan(time.Hour))
	if err != nil || got != (data.SweepResult{Pending: 1}) {
		t.Fatalf("Sweep = %+v, %v, want one pending row reclaimed", got, err)
	}
	if !slices.Equal(objects.keys, []string{"P/p.txt"}) {
		t.Errorf("the pass deleted %v, want P's key alone", objects.keys)
	}
	marks := callsTo(rec, "UPDATE blobfs_file")
	if len(marks) != 2 || !slices.Equal(marks[0].Args, []any{"P", int64(1)}) || !strings.Contains(marks[0].SQL, "version = CAST($2 AS bigint)") {
		t.Errorf("the reclaim ran %v, want each row's delete at the version read", marks)
	}
	if want := "query query begin exec query commit exec begin exec query rollback"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
}
