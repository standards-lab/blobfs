package data_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/sqltest"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data"
	"github.com/standards-lab/blobfs/data/datatest"
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
	var rows []blobfs.File
	for _, id := range ids {
		rows = append(rows, fileRow(id, dir, strings.ToLower(id)+".txt", blobfs.StatusDeleting, 2))
	}
	return datatest.FileRows(rows...)
}

// deletingRoot scripts the roots' read returning D, deleting at version 2
// under the root.
func deletingRoot() sqltest.Response {
	return directoryIn("D", blobfs.RootID, "d", blobfs.DirectoryStatusDeleting, 2)
}

// remark scripts the mark a pass repeats on a branch marked already, after
// a straggler: no directory changed, the read of the root, and the
// straggling file changed.
func remark() []sqltest.Response {
	return []sqltest.Response{{Affected: 0}, deletingRoot(), {Affected: 1}}
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
// file, step by step: the walk marks nothing again when it meets no
// straggler.
func TestSweepABranch(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback,
		deletingRoot(),
		files("D", "F"),
		sqltest.Response{Affected: 1},
		noDirectory(),
		sqltest.Response{Affected: 1},
	)
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
	if want := "query query exec query begin exec commit"; ops(rec) != want {
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

// TestSweepBatch checks a pass stops at its bound and reports More without
// another read, and that a page past a refused file is read past it.
func TestSweepBatch(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback, deletingRoot(), files("D", "F", "G"), sqltest.Response{Affected: 1})
	objects := &objectLog{}
	got, err := s.Sweep(ctx, db, objects, data.Batch(1))
	if err != nil || got != (data.SweepResult{Files: 1, More: true}) {
		t.Fatalf("Sweep = %+v, %v, want one file and More", got, err)
	}
	if !slices.Equal(objects.keys, []string{"F/f.txt"}) {
		t.Errorf("the pass deleted %v, want F's key alone", objects.keys)
	}
	if want := "query query exec"; ops(rec) != want {
		t.Errorf("ops = %q, want the roots' read, one page, and one purge", ops(rec))
	}

	// F's object delete is refused, so the budget of one is left for G,
	// on the page read past F.
	errStore := errors.New("the store is down")
	s, db, rec = openStore(t, fallback, deletingRoot(), files("D", "F", "G"), files("D", "G"), sqltest.Response{Affected: 1})
	got, err = s.Sweep(ctx, db, &failingKey{key: "F/f.txt", err: errStore}, data.Batch(1))
	if !errors.Is(err, errStore) || got != (data.SweepResult{Files: 1, More: true}) {
		t.Fatalf("Sweep past a refused file = %+v, %v, want G finished, More, and the store's error", got, err)
	}
	pages := queries(rec)[1:]
	if len(pages) != 2 || !strings.Contains(pages[1].SQL, "name") || len(pages[1].Args) <= len(pages[0].Args) {
		t.Errorf("the pass read %v, want the first page and the page past F", pages)
	}
	if purge := callsTo(rec, "DELETE FROM blobfs_file"); len(purge) != 1 || !slices.Equal(purge[0].Args, []any{"G"}) {
		t.Errorf("the pass purged %v, want G alone", purge)
	}
}

// failingKey is an ObjectDeleter that refuses key with err and deletes
// every other key.
type failingKey struct {
	key string
	err error
}

func (f *failingKey) DeleteObject(_ context.Context, key string) error {
	if key == f.key {
		return f.err
	}
	return nil
}

// TestSweepStraggler checks a straggler has the branch marked again, once,
// and the directory walked again: the marked straggler is swept in the
// same pass, and a straggler met after the mark stops the branch with
// More.
func TestSweepStraggler(t *testing.T) {
	ctx := context.Background()
	straggler := fileIn("S", "D", "s.txt", blobfs.StatusAvailable, 1)
	responses := append([]sqltest.Response{deletingRoot(), straggler}, remark()...)
	s, db, rec := openStore(t, fallback, append(responses,
		files("D", "S"), sqltest.Response{Affected: 1}, noDirectory(), sqltest.Response{Affected: 1})...)
	got, err := s.Sweep(ctx, db, &objectLog{})
	if err != nil || got != (data.SweepResult{Files: 1, Directories: 1}) {
		t.Errorf("Sweep over a straggler = %+v, %v, want it marked and swept with the branch", got, err)
	}
	if want := "query query begin exec query exec commit query exec query begin exec commit"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
	marks := callsTo(rec, "UPDATE blobfs_directory")
	if len(marks) != 1 || !slices.Equal(marks[0].Args, []any{"D", nil}) {
		t.Errorf("the pass marked %v, want D's branch once, at no version", marks)
	}

	s, db, rec = openStore(t, fallback, append(responses, straggler)...)
	objects := &objectLog{}
	got, err = s.Sweep(ctx, db, objects)
	if err != nil || got != (data.SweepResult{More: true}) || len(objects.keys) != 0 {
		t.Errorf("Sweep over a straggler after the mark = %+v, %v, deleted %v, want More and nothing done", got, err, objects.keys)
	}
	if n := len(callsTo(rec, "UPDATE blobfs_directory")); n != 1 {
		t.Errorf("the pass marked the branch %d times, want once", n)
	}
	if n := len(callsTo(rec, "DELETE")); n != 0 {
		t.Errorf("the straggler was followed by %d deletes", n)
	}
}

// TestSweepRefusals checks a refusal keeps its row and the directories
// above it, and the walk goes on: an object delete's error and a hook's
// error.
func TestSweepRefusals(t *testing.T) {
	ctx := context.Background()
	errStore := errors.New("the store is down")
	s, db, rec := openStore(t, fallback, deletingRoot(), files("D", "F"), noDirectory())
	got, err := s.Sweep(ctx, db, &objectLog{err: errStore})
	if !errors.Is(err, errStore) || got != (data.SweepResult{}) {
		t.Errorf("Sweep over a failing store = %+v, %v, want the store's error", got, err)
	}
	if want := "query query query"; ops(rec) != want {
		t.Errorf("ops = %q, want the pages of files and directories and no removal", ops(rec))
	}

	errHook := errors.New("the owner row is locked")
	s, db, rec = openStore(t, fallback, deletingRoot(), files("D"), noDirectory())
	got, err = s.Sweep(ctx, db, &objectLog{}, data.OnRemoveDirectory(func(context.Context, *sqlate.Tx, blobfs.Directory) error { return errHook }))
	if !errors.Is(err, errHook) || got != (data.SweepResult{}) {
		t.Errorf("Sweep under a failing hook = %+v, %v, want the hook's error", got, err)
	}
	if want := "query query query begin rollback"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
}

// TestSweepRemovalOutcomes checks a directory's removal: one gone
// already counts as removed, one refused as not empty is a straggler that
// has the branch marked again and walked again, and one a consumer's
// foreign key refuses is kept, the refusal returned.
func TestSweepRemovalOutcomes(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback, deletingRoot(), files("D"), noDirectory(), sqltest.Response{Affected: 0}, noDirectory())
	got, err := s.Sweep(ctx, db, &objectLog{})
	if err != nil || got != (data.SweepResult{}) {
		t.Errorf("Sweep of a directory gone before its removal = %+v, %v, want nothing counted and no error", got, err)
	}
	if want := "query query query begin exec query rollback"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
	wantDone(t, rec)

	s, db, rec = openStore(t, fallback, deletingRoot(), files("D"), noDirectory(),
		violation(blobfs.ConstraintForeignKeyDirectoryParent, sqlate.ErrForeignKeyViolation),
		sqltest.Response{Affected: 0}, deletingRoot(), sqltest.Response{Affected: 0},
		files("D"), noDirectory(), sqltest.Response{Affected: 1})
	got, err = s.Sweep(ctx, db, &objectLog{})
	if err != nil || got != (data.SweepResult{Directories: 1}) {
		t.Errorf("Sweep of a directory refused as not empty = %+v, %v, want it marked again and removed", got, err)
	}
	if want := "query query query begin exec rollback begin exec query exec commit query query begin exec commit"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
	wantDone(t, rec)

	s, db, rec = openStore(t, fallback, deletingRoot(), files("D"), noDirectory(), violation("fk_owner_directory", sqlate.ErrForeignKeyViolation))
	got, err = s.Sweep(ctx, db, &objectLog{})
	if !errors.Is(err, blobfs.ErrReferenced) || got != (data.SweepResult{}) {
		t.Errorf("Sweep of a directory a consumer references = %+v, %v, want ErrReferenced and nothing counted", got, err)
	}
	if want := "query query query begin exec rollback"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
	wantDone(t, rec)
}

// TestSweepRemark checks the mark a straggler triggers: a branch gone by
// then is done, a mark that fails stops the branch with its error, and an
// active child directory is a straggler as an active file is.
func TestSweepRemark(t *testing.T) {
	ctx := context.Background()
	straggler := fileIn("S", "D", "s.txt", blobfs.StatusAvailable, 1)
	s, db, rec := openStore(t, fallback, deletingRoot(), straggler, sqltest.Response{Affected: 0}, noDirectory())
	got, err := s.Sweep(ctx, db, &objectLog{})
	if err != nil || got != (data.SweepResult{}) {
		t.Errorf("Sweep of a branch gone before its mark = %+v, %v, want nothing counted and no error", got, err)
	}
	if want := "query query begin exec query rollback"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
	wantDone(t, rec)

	errMark := errors.New("the mark failed")
	s, db, rec = openStore(t, fallback, deletingRoot(), straggler, sqltest.Response{Err: errMark})
	got, err = s.Sweep(ctx, db, &objectLog{})
	if !errors.Is(err, errMark) || !strings.Contains(err.Error(), "branch D: mark directory D deleting: ") || got != (data.SweepResult{}) {
		t.Errorf("Sweep under a failing mark = %+v, %v, want the mark's error against the branch", got, err)
	}
	wantDone(t, rec)

	child := directoryIn("C", "D", "c", blobfs.DirectoryStatusActive, 1)
	s, db, rec = openStore(t, fallback, deletingRoot(), files("D"), child, remark()[0], remark()[1], remark()[2], files("D"), child)
	got, err = s.Sweep(ctx, db, &objectLog{})
	if err != nil || got != (data.SweepResult{More: true}) {
		t.Errorf("Sweep over an active child directory = %+v, %v, want the branch marked once and More", got, err)
	}
	if want := "query query query begin exec query exec commit query query"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
	wantDone(t, rec)
}

// TestSweepRemains checks the read a pass makes when its budget runs out
// exactly as its work does: one root, or with StaleOlderThan one stale
// row, past those the pass left, sets More.
func TestSweepRemains(t *testing.T) {
	ctx := context.Background()
	other := directoryIn("E", blobfs.RootID, "e", blobfs.DirectoryStatusDeleting, 2)
	for _, c := range []struct {
		name  string
		opts  []data.SweepOption
		reads []sqltest.Response
		more  bool
	}{
		{"a root", nil, []sqltest.Response{other}, true},
		{"none", nil, []sqltest.Response{noDirectory()}, false},
		{"a stale row", []data.SweepOption{data.StaleOlderThan(time.Hour)}, []sqltest.Response{noDirectory(), files("D", "F")}, true},
		{"no stale row", []data.SweepOption{data.StaleOlderThan(time.Hour)}, []sqltest.Response{noDirectory(), noFile()}, false},
	} {
		s, db, rec := openStore(t, fallback, append([]sqltest.Response{deletingRoot(), files("D"), noDirectory(), {Affected: 1}}, c.reads...)...)
		got, err := s.Sweep(ctx, db, &objectLog{}, append(c.opts, data.Batch(1))...)
		if err != nil || got != (data.SweepResult{Directories: 1, More: c.more}) {
			t.Errorf("%s: Sweep = %+v, %v, want the branch removed and More %v", c.name, got, err, c.more)
		}
		reads := queries(rec)[3:]
		if len(reads) != len(c.reads) || !slices.Equal(reads[0].Args, []any{0, 1}) {
			t.Errorf("%s: the pass read %v after the removal, want one row past those it left", c.name, reads)
		}
		wantDone(t, rec)
	}
}

// TestSweepStale checks the reclaim of pending and deleting stale rows,
// and the skip of rows gone or moved on.
func TestSweepStale(t *testing.T) {
	ctx := context.Background()
	row := func(id string, status blobfs.Status, version int64) blobfs.File {
		return fileRow(id, "D", strings.ToLower(id)+".txt", status, version)
	}
	stale := datatest.FileRows(
		row("P", blobfs.StatusPending, 1), row("L", blobfs.StatusDeleting, 2), row("M", blobfs.StatusDeleting, 2), row("Q", blobfs.StatusPending, 1),
	)
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
	s, db, _ := openStore(t, fallback, deletingRoot(), files("D", "F"), noDirectory(), files("D", "F"))
	objects := &objectLog{err: errStore}
	got, err := s.Sweep(ctx, db, objects, data.StaleOlderThan(time.Hour))
	if !errors.Is(err, errStore) || got != (data.SweepResult{}) {
		t.Errorf("Sweep = %+v, %v, want the store's error and nothing done", got, err)
	}
	if !slices.Equal(objects.keys, []string{"F/f.txt"}) || strings.Count(err.Error(), errStore.Error()) != 1 {
		t.Errorf("the pass deleted %v and reported %v, want F tried once and refused once", objects.keys, err)
	}
}

// The loop's scripts run passes of Batch(1) with the stale reclaim, so a
// pass is a few statements over stale rows.
var loopOpts = []data.SweepOption{data.Batch(1), data.StaleOlderThan(time.Hour)}

// staleRow scripts the read of one deleting file row past the stale age.
func staleRow(id string) sqltest.Response {
	f := fileRow(id, "D", strings.ToLower(id)+".txt", blobfs.StatusDeleting, 3)
	f.UpdatedAt = f.UpdatedAt.Add(-2 * time.Hour)
	return datatest.FileRows(f)
}

// morePass scripts a pass that reclaims the stale row id, spends its
// batch, and finds next beyond it: Stale 1, More.
func morePass(id, next string) []sqltest.Response {
	return []sqltest.Response{noDirectory(), staleRow(id), {Affected: 1}, noDirectory(), staleRow(next)}
}

// lastPass scripts a pass that reclaims the stale row id and finds nothing
// beyond it: Stale 1, no More.
func lastPass(id string) []sqltest.Response {
	return []sqltest.Response{noDirectory(), staleRow(id), {Affected: 1}, noDirectory(), noFile()}
}

// The ops of a pass with More, as morePass and lastPass script it.
const passOps = "query query exec query query"

// passes records what a loop reported, pass by pass.
type passes struct {
	results []data.SweepResult
	errs    []error
}

func (p *passes) report(res data.SweepResult, err error) {
	p.results = append(p.results, res)
	p.errs = append(p.errs, err)
}

// sweepPass is one pass of s's sweep over db with opts, as a consumer
// hands it to the loop.
func sweepPass(s *data.Store, db *sqlate.DB, opts ...data.SweepOption) func(context.Context) (data.SweepResult, error) {
	return func(ctx context.Context) (data.SweepResult, error) {
		return s.Sweep(ctx, db, &objectLog{}, opts...)
	}
}

// TestSweepUntilDone checks the loop runs passes while one reports More,
// reports each, and returns nil at the first that does not; a pass with
// nothing to do is one pass, and a nil report reports nothing.
func TestSweepUntilDone(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback, slices.Concat(morePass("A", "B"), lastPass("B"))...)
	var got passes
	if err := data.SweepUntilDone(ctx, nil, sweepPass(s, db, loopOpts...), got.report); err != nil {
		t.Fatalf("SweepUntilDone = %v, want nil", err)
	}
	want := []data.SweepResult{{Stale: 1, More: true}, {Stale: 1}}
	if !slices.Equal(got.results, want) || !slices.Equal(got.errs, []error{nil, nil}) {
		t.Errorf("reported %+v, %v, want %+v and no error", got.results, got.errs, want)
	}
	if ops(rec) != passOps+" "+passOps {
		t.Errorf("ops = %q, want the two passes", ops(rec))
	}

	s, db, rec = openStore(t, fallback, noDirectory(), noFile())
	got = passes{}
	if err := data.SweepUntilDone(ctx, nil, sweepPass(s, db, loopOpts...), got.report); err != nil || len(got.results) != 1 || got.results[0] != (data.SweepResult{}) {
		t.Errorf("SweepUntilDone with nothing to do = %v after %+v, want one empty pass", err, got.results)
	}
	if ops(rec) != "query query" {
		t.Errorf("ops = %q, want the one pass's reads", ops(rec))
	}

	// A nil report discards each pass's result; the loop still runs until
	// a pass reports no More.
	s, db, rec = openStore(t, fallback, slices.Concat(morePass("A", "B"), lastPass("B"))...)
	if err := data.SweepUntilDone(ctx, nil, sweepPass(s, db, loopOpts...), nil); err != nil {
		t.Fatalf("SweepUntilDone with a nil report = %v, want nil", err)
	}
	if ops(rec) != passOps+" "+passOps {
		t.Errorf("ops = %q, want the two passes", ops(rec))
	}
}

// TestSweepUntilDoneRefusals checks a pass's refusals are reported and
// never end the loop: it goes on while the pass reports More and ends with
// nil when it does not. A pass that refuses its options reports no More,
// so the loop reports it once and ends.
func TestSweepUntilDoneRefusals(t *testing.T) {
	ctx := context.Background()
	refused := sqltest.Response{Err: errors.New("the purge was refused")}
	s, db, rec := openStore(t, fallback,
		// A pass with More: the first row refused, the next reclaimed, a
		// third beyond the batch.
		noDirectory(), staleRow("A"), refused, staleRow("B"), sqltest.Response{Affected: 1}, noDirectory(), staleRow("C"),
		// A pass without: the third row refused, nothing beyond it.
		noDirectory(), staleRow("C"), refused, noFile(),
	)
	var got passes
	if err := data.SweepUntilDone(ctx, nil, sweepPass(s, db, loopOpts...), got.report); err != nil {
		t.Fatalf("SweepUntilDone = %v, want nil despite the refusals", err)
	}
	if len(got.errs) != 2 || !strings.Contains(fmt.Sprint(got.errs[0]), "the purge was refused") || got.results[0] != (data.SweepResult{Stale: 1, More: true}) || got.errs[1] == nil {
		t.Errorf("reported %+v, %v, want both passes' refusals and the first's count", got.results, got.errs)
	}
	if want := "query query exec query exec query query query query exec query"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}

	s, db, rec = openStore(t, fallback)
	got = passes{}
	if err := data.SweepUntilDone(ctx, nil, sweepPass(s, db, data.Batch(0)), got.report); err != nil || len(got.results) != 1 || got.errs[0] == nil || len(rec.Calls()) != 0 {
		t.Errorf("SweepUntilDone with a refused batch = %v after %+v, %v, want the refusal reported once and no statement", err, got.results, got.errs)
	}
}

// TestSweepUntilDoneStops checks the loop's ends: a context ended before a
// pass runs no pass and returns its error, a context ended during a pass
// is returned with the pass unreported, and a closed stop ends the loop
// between passes, the pass in flight finished and reported.
func TestSweepUntilDoneStops(t *testing.T) {
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	s, db, rec := openStore(t, fallback)
	var got passes
	if err := data.SweepUntilDone(ended, nil, sweepPass(s, db), got.report); !errors.Is(err, context.Canceled) || len(got.results) != 0 || len(rec.Calls()) != 0 {
		t.Errorf("SweepUntilDone on an ended context = %v after %d passes, want no pass", err, len(got.results))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, db, rec = openStore(t, fallback, morePass("A", "B")...)
	got = passes{}
	calls := 0
	// The second pass ends the context before it runs, so it reaches no
	// statement.
	pass := sweepPass(s, db, loopOpts...)
	cancelling := func(ctx context.Context) (data.SweepResult, error) {
		if calls++; calls == 2 {
			cancel()
		}
		return pass(ctx)
	}
	if err := data.SweepUntilDone(ctx, nil, cancelling, got.report); !errors.Is(err, context.Canceled) {
		t.Fatalf("SweepUntilDone = %v, want the context's cancellation", err)
	}
	if calls != 2 || len(got.results) != 1 || ops(rec) != passOps {
		t.Errorf("passes begun = %d, reported = %d, ops = %q, want the second begun and unreported", calls, len(got.results), ops(rec))
	}

	stop := make(chan struct{})
	s, db, rec = openStore(t, fallback, morePass("A", "B")...)
	got = passes{}
	report := func(res data.SweepResult, err error) {
		got.report(res, err)
		close(stop)
	}
	if err := data.SweepUntilDone(context.Background(), stop, sweepPass(s, db, loopOpts...), report); err != nil {
		t.Fatalf("SweepUntilDone = %v, want nil once stop closes", err)
	}
	if len(got.results) != 1 || !got.results[0].More || ops(rec) != passOps {
		t.Errorf("reported %+v after %q, want the pass in flight alone, though it reported More", got.results, ops(rec))
	}
}
