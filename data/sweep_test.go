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

// TestSweepRefusesBeforeSQL proves a Batch below 1 and a StaleOlderThan
// age that is not positive are refused before any SQL.
func TestSweepRefusesBeforeSQL(t *testing.T) {
	ctx := context.Background()
	for _, opt := range []data.SweepOption{data.Batch(0), data.Batch(-1), data.StaleOlderThan(0), data.StaleOlderThan(-time.Hour)} {
		s, db, rec := openStore(t, fallback)
		if _, err := s.Sweep(ctx, db, &objectLog{}, opt); err == nil || !strings.HasPrefix(err.Error(), "data: sweep: ") {
			t.Errorf("Sweep with a refused option = %v, want a refusal", err)
		}
		if n := len(rec.Calls()); n != 0 {
			t.Errorf("the refused option ran %d calls", n)
		}
	}
}

// TestSweepNothingToDo checks a pass with no work runs only its reads and
// returns a zero result.
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
	got, err = s.Sweep(ctx, db, objects, data.StaleOlderThan(time.Hour), data.Batch(7))
	if err != nil || got != (data.SweepResult{}) {
		t.Errorf("Sweep with StaleOlderThan = %+v, %v, want a zero result", got, err)
	}
	calls := queries(rec)
	if len(calls) != 2 || !slices.Equal(calls[0].Args, []any{0, 7}) {
		t.Fatalf("the pass ran %v, want the roots' read to the batch and the stale read", calls)
	}
	stale := calls[1]
	if !strings.Contains(stale.SQL, "WHERE f.status IN ('pending', 'deleting') AND f.updated_at < CAST($1 AS timestamp with time zone)\nORDER BY f.updated_at, f.id") {
		t.Errorf("the stale read is not the statement over the pending and deleting rows by age:\n%s", stale.SQL)
	}
	if len(stale.Args) != 3 || stale.Args[1] != 0 || stale.Args[2] != 7 {
		t.Fatalf("the stale read bound %v, want the instant, offset 0, and the batch", stale.Args)
	}
	before, ok := stale.Args[0].(time.Time)
	if want := start.Add(-time.Hour); !ok || before.Before(want.Add(-time.Minute)) || before.After(want.Add(time.Minute)) {
		t.Errorf("the stale read bound the instant %v, want about %v", stale.Args[0], want)
	}
}

// TestSweepABranch checks a pass over a branch of one directory and one
// file, step by step.
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

// TestSweepBatch checks a pass stops at its bound and reports More.
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

// TestSweepStops checks a pass stops a branch at an object delete's error,
// a hook's error, and a straggler.
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

// TestSweepStale checks the reclaim of pending and deleting stale rows,
// and the skip of rows gone or moved on.
func TestSweepStale(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	row := func(id, status string, version int64) []driver.Value {
		return []driver.Value{id, "D", strings.ToLower(id) + ".txt", status, id + "/" + strings.ToLower(id) + ".txt", nil, "text/plain", nil, version, now, now}
	}
	stale := sqltest.Response{Columns: fileColumns, Rows: [][]driver.Value{
		row("P", "pending", 1), row("L", "deleting", 2), row("M", "deleting", 2), row("Q", "pending", 1),
	}}
	s, db, rec := openStore(t, fallback,
		noDirectory(),
		stale,
		sqltest.Response{Affected: 1}, fileIn("P", "D", "p.txt", blobfs.StatusDeleting, 2),
		sqltest.Response{Affected: 1},
		sqltest.Response{Affected: 1},
		sqltest.Response{Affected: 0}, noFile(),
		sqltest.Response{Affected: 0}, fileIn("Q", "D", "q.txt", blobfs.StatusAvailable, 2),
	)
	objects := &objectLog{}
	got, err := s.Sweep(ctx, db, objects, data.StaleOlderThan(time.Hour))
	if err != nil || got != (data.SweepResult{Stale: 3}) {
		t.Fatalf("Sweep = %+v, %v, want three stale rows reclaimed", got, err)
	}
	if !slices.Equal(objects.keys, []string{"P/p.txt", "L/l.txt", "M/m.txt"}) {
		t.Errorf("the pass deleted %v, want P's, L's, and M's keys", objects.keys)
	}
	marks := callsTo(rec, "UPDATE blobfs_file")
	if len(marks) != 2 || !slices.Equal(marks[0].Args, []any{"P", int64(1)}) || !slices.Equal(marks[1].Args, []any{"Q", int64(1)}) || !strings.Contains(marks[0].SQL, "version = CAST($2 AS bigint)") {
		t.Errorf("the reclaim ran %v, want the pending rows' deletes at the version read and none for the deleting rows", marks)
	}
	if purges := callsTo(rec, "DELETE FROM blobfs_file"); len(purges) != 3 {
		t.Errorf("the reclaim ran %d purges, want P's, L's, and M's", len(purges))
	}
	if want := "query query begin exec query commit exec exec exec query begin exec query rollback"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
}

// TestSweepRefusedOnce checks a file refused in a branch's walk is not
// tried again by the stale read of the same pass.
func TestSweepRefusedOnce(t *testing.T) {
	ctx := context.Background()
	errStore := errors.New("the store is down")
	responses := append([]sqltest.Response{deletingRoot()}, remark()...)
	responses = append(responses, files("D", "F"), files("D", "F"))
	s, db, _ := openStore(t, fallback, responses...)
	objects := &objectLog{err: errStore}
	got, err := s.Sweep(ctx, db, objects, data.StaleOlderThan(time.Hour))
	if !errors.Is(err, errStore) || got != (data.SweepResult{}) {
		t.Errorf("Sweep = %+v, %v, want the store's error and nothing done", got, err)
	}
	if !slices.Equal(objects.keys, []string{"F/f.txt"}) || strings.Count(err.Error(), errStore.Error()) != 1 {
		t.Errorf("the pass deleted %v and reported %v, want F tried once and refused once", objects.keys, err)
	}
}
