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

type objectLog struct {
	keys []string
	err  error
}

func (o *objectLog) DeleteObject(_ context.Context, key string) error {
	o.keys = append(o.keys, key)
	return o.err
}

func files(dir string, ids ...string) sqltest.Response {
	var rows []blobfs.File
	for _, id := range ids {
		rows = append(rows, fileRow(id, dir, strings.ToLower(id)+".txt", blobfs.StatusDeleting, 2))
	}
	return datatest.FileRows(rows...)
}

func deletingRoot() sqltest.Response {
	return directoryIn("D", blobfs.RootID, "d", blobfs.DirectoryStatusDeleting, 2)
}

func remark() []sqltest.Response {
	return []sqltest.Response{{Affected: 0}, deletingRoot(), {Affected: 1}}
}

func TestSweepRefusesBeforeSQL(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		opts data.SweepOptions
		want string
	}{
		{data.SweepOptions{Batch: -1}, "data: sweep: the batch -1 is below 1"},
		{data.SweepOptions{StaleOlderThan: -time.Hour}, "data: sweep: the stale age -1h0m0s is not positive"},
	} {
		s, db, rec := openStore(t, fallback)
		if _, err := s.Sweep(ctx, db, &objectLog{}, c.opts); err == nil || err.Error() != c.want {
			t.Errorf("Sweep with %+v = %v, want %q", c.opts, err, c.want)
		}
		if n := len(rec.Calls()); n != 0 {
			t.Errorf("the refused %+v ran %d calls", c.opts, n)
		}
	}
}

func TestSweepNothingToDo(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback, noDirectory())
	objects := &objectLog{}
	got, err := s.Sweep(ctx, db, objects, data.SweepOptions{})
	if err != nil || got != (data.SweepResult{}) {
		t.Errorf("Sweep = %+v, %v, want a zero result", got, err)
	}
	if ops := ops(rec); ops != "query" || len(objects.keys) != 0 {
		t.Errorf("the empty pass ran %q and deleted %v, want the one read and nothing", ops, objects.keys)
	}

	s, db, rec = openStore(t, fallback, noDirectory(), noFile())
	start := time.Now()
	got, err = s.Sweep(ctx, db, objects, data.SweepOptions{Batch: 7, StaleOlderThan: time.Hour})
	if err != nil || got != (data.SweepResult{}) {
		t.Errorf("Sweep with StaleOlderThan = %+v, %v, want a zero result", got, err)
	}
	calls := queries(rec)
	if len(calls) != 2 || !slices.Equal(calls[0].Args, []any{0, 7}) {
		t.Fatalf("the pass ran %v, want the roots' read to the batch and the stale read", calls)
	}
	stale := calls[1]
	if len(stale.Args) != 3 || stale.Args[1] != 0 || stale.Args[2] != 7 {
		t.Fatalf("the stale read bound %v, want the instant, offset 0, and the batch", stale.Args)
	}
	before, ok := stale.Args[0].(time.Time)
	if want := start.Add(-time.Hour); !ok || before.Before(want.Add(-time.Minute)) || before.After(want.Add(time.Minute)) {
		t.Errorf("the stale read bound the instant %v, want about %v", stale.Args[0], want)
	}
}

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
	got, err := s.Sweep(ctx, db, objects, data.SweepOptions{OnRemoveDirectory: hook})
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
	if len(remove) != 1 || !slices.Equal(remove[0].Args, []any{"D", int64(2)}) {
		t.Errorf("the removal ran %v, want D's at the version read", remove)
	}
}

func TestSweepBatch(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback, deletingRoot(), files("D", "F", "G"), sqltest.Response{Affected: 1})
	objects := &objectLog{}
	got, err := s.Sweep(ctx, db, objects, data.SweepOptions{Batch: 1})
	if err != nil || got != (data.SweepResult{Files: 1, More: true}) {
		t.Fatalf("Sweep = %+v, %v, want one file and More", got, err)
	}
	if !slices.Equal(objects.keys, []string{"F/f.txt"}) {
		t.Errorf("the pass deleted %v, want F's key alone", objects.keys)
	}
	if want := "query query exec"; ops(rec) != want {
		t.Errorf("ops = %q, want the roots' read, one page, and one purge", ops(rec))
	}

	errStore := errors.New("the store is down")
	s, db, rec = openStore(t, fallback, deletingRoot(), files("D", "F", "G"), files("D", "G"), sqltest.Response{Affected: 1})
	got, err = s.Sweep(ctx, db, &failingKey{key: "F/f.txt", err: errStore}, data.SweepOptions{Batch: 1})
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

func TestSweepStraggler(t *testing.T) {
	ctx := context.Background()
	straggler := fileIn("S", "D", "s.txt", blobfs.StatusAvailable, 1)
	responses := append([]sqltest.Response{deletingRoot(), straggler}, remark()...)
	s, db, rec := openStore(t, fallback, append(responses,
		files("D", "S"), sqltest.Response{Affected: 1}, noDirectory(), sqltest.Response{Affected: 1})...)
	got, err := s.Sweep(ctx, db, &objectLog{}, data.SweepOptions{})
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
	got, err = s.Sweep(ctx, db, objects, data.SweepOptions{})
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

func TestSweepRefusals(t *testing.T) {
	ctx := context.Background()
	errStore := errors.New("the store is down")
	s, db, rec := openStore(t, fallback, deletingRoot(), files("D", "F"), noDirectory())
	got, err := s.Sweep(ctx, db, &objectLog{err: errStore}, data.SweepOptions{})
	if !errors.Is(err, errStore) || got != (data.SweepResult{}) {
		t.Errorf("Sweep over a failing store = %+v, %v, want the store's error", got, err)
	}
	if want := "query query query"; ops(rec) != want {
		t.Errorf("ops = %q, want the pages of files and directories and no removal", ops(rec))
	}

	errHook := errors.New("the owner row is locked")
	s, db, rec = openStore(t, fallback, deletingRoot(), files("D"), noDirectory())
	got, err = s.Sweep(ctx, db, &objectLog{}, data.SweepOptions{OnRemoveDirectory: func(context.Context, *sqlate.Tx, blobfs.Directory) error { return errHook }})
	if !errors.Is(err, errHook) || got != (data.SweepResult{}) {
		t.Errorf("Sweep under a failing hook = %+v, %v, want the hook's error", got, err)
	}
	if want := "query query query begin rollback"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
}

func TestSweepRemovalOutcomes(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback, deletingRoot(), files("D"), noDirectory(), sqltest.Response{Affected: 0}, noDirectory())
	got, err := s.Sweep(ctx, db, &objectLog{}, data.SweepOptions{})
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
	got, err = s.Sweep(ctx, db, &objectLog{}, data.SweepOptions{})
	if err != nil || got != (data.SweepResult{Directories: 1}) {
		t.Errorf("Sweep of a directory refused as not empty = %+v, %v, want it marked again and removed", got, err)
	}
	if want := "query query query begin exec rollback begin exec query exec commit query query begin exec commit"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
	wantDone(t, rec)

	s, db, rec = openStore(t, fallback, deletingRoot(), files("D"), noDirectory(), violation("fk_owner_directory", sqlate.ErrForeignKeyViolation))
	got, err = s.Sweep(ctx, db, &objectLog{}, data.SweepOptions{})
	if !errors.Is(err, blobfs.ErrReferenced) || got != (data.SweepResult{}) {
		t.Errorf("Sweep of a directory a consumer references = %+v, %v, want ErrReferenced and nothing counted", got, err)
	}
	if want := "query query query begin exec rollback"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
	wantDone(t, rec)
}

func TestSweepRemark(t *testing.T) {
	ctx := context.Background()
	straggler := fileIn("S", "D", "s.txt", blobfs.StatusAvailable, 1)
	s, db, rec := openStore(t, fallback, deletingRoot(), straggler, sqltest.Response{Affected: 0}, noDirectory())
	got, err := s.Sweep(ctx, db, &objectLog{}, data.SweepOptions{})
	if err != nil || got != (data.SweepResult{}) {
		t.Errorf("Sweep of a branch gone before its mark = %+v, %v, want nothing counted and no error", got, err)
	}
	if want := "query query begin exec query rollback"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
	wantDone(t, rec)

	errMark := errors.New("the mark failed")
	s, db, rec = openStore(t, fallback, deletingRoot(), straggler, sqltest.Response{Err: errMark})
	got, err = s.Sweep(ctx, db, &objectLog{}, data.SweepOptions{})
	if !errors.Is(err, errMark) || !strings.Contains(err.Error(), "branch D: mark directory D deleting: ") || got != (data.SweepResult{}) {
		t.Errorf("Sweep under a failing mark = %+v, %v, want the mark's error against the branch", got, err)
	}
	wantDone(t, rec)

	child := directoryIn("C", "D", "c", blobfs.DirectoryStatusActive, 1)
	s, db, rec = openStore(t, fallback, deletingRoot(), files("D"), child, remark()[0], remark()[1], remark()[2], files("D"), child)
	got, err = s.Sweep(ctx, db, &objectLog{}, data.SweepOptions{})
	if err != nil || got != (data.SweepResult{More: true}) {
		t.Errorf("Sweep over an active child directory = %+v, %v, want the branch marked once and More", got, err)
	}
	if want := "query query query begin exec query exec commit query query"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
	wantDone(t, rec)
}

func TestSweepRemains(t *testing.T) {
	ctx := context.Background()
	other := directoryIn("E", blobfs.RootID, "e", blobfs.DirectoryStatusDeleting, 2)
	for _, c := range []struct {
		name  string
		stale time.Duration
		reads []sqltest.Response
		more  bool
	}{
		{"a root", 0, []sqltest.Response{other}, true},
		{"none", 0, []sqltest.Response{noDirectory()}, false},
		{"a stale row", time.Hour, []sqltest.Response{noDirectory(), files("D", "F")}, true},
		{"no stale row", time.Hour, []sqltest.Response{noDirectory(), noFile()}, false},
	} {
		s, db, rec := openStore(t, fallback, append([]sqltest.Response{deletingRoot(), files("D"), noDirectory(), {Affected: 1}}, c.reads...)...)
		got, err := s.Sweep(ctx, db, &objectLog{}, data.SweepOptions{Batch: 1, StaleOlderThan: c.stale})
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
	got, err := s.Sweep(ctx, db, objects, data.SweepOptions{StaleOlderThan: time.Hour})
	if err != nil || got != (data.SweepResult{Stale: 3}) {
		t.Fatalf("Sweep = %+v, %v, want three stale rows reclaimed", got, err)
	}
	if !slices.Equal(objects.keys, []string{"P/p.txt", "L/l.txt", "M/m.txt"}) {
		t.Errorf("the pass deleted %v, want P's, L's, and M's keys", objects.keys)
	}
	marks := callsTo(rec, "UPDATE blobfs_file")
	if len(marks) != 2 || !slices.Equal(marks[0].Args, []any{"P", int64(1)}) || !slices.Equal(marks[1].Args, []any{"Q", int64(1)}) {
		t.Errorf("the reclaim ran %v, want the pending rows' deletes at the version read and none for the deleting rows", marks)
	}
	if purges := callsTo(rec, "DELETE FROM blobfs_file"); len(purges) != 3 {
		t.Errorf("the reclaim ran %d purges, want P's, L's, and M's", len(purges))
	}
	if want := "query query begin exec query commit exec exec exec query begin exec query rollback"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
}

func TestSweepRefusedOnce(t *testing.T) {
	ctx := context.Background()
	errStore := errors.New("the store is down")
	s, db, _ := openStore(t, fallback, deletingRoot(), files("D", "F"), noDirectory(), files("D", "F"))
	objects := &objectLog{err: errStore}
	got, err := s.Sweep(ctx, db, objects, data.SweepOptions{StaleOlderThan: time.Hour})
	if !errors.Is(err, errStore) || got != (data.SweepResult{}) {
		t.Errorf("Sweep = %+v, %v, want the store's error and nothing done", got, err)
	}
	if !slices.Equal(objects.keys, []string{"F/f.txt"}) || strings.Count(err.Error(), errStore.Error()) != 1 {
		t.Errorf("the pass deleted %v and reported %v, want F tried once and refused once", objects.keys, err)
	}
}

var loopOpts = data.SweepOptions{Batch: 1, StaleOlderThan: time.Hour}

func staleRow(id string) sqltest.Response {
	f := fileRow(id, "D", strings.ToLower(id)+".txt", blobfs.StatusDeleting, 3)
	f.UpdatedAt = f.UpdatedAt.Add(-2 * time.Hour)
	return datatest.FileRows(f)
}

func morePass(id, next string) []sqltest.Response {
	return []sqltest.Response{noDirectory(), staleRow(id), {Affected: 1}, noDirectory(), staleRow(next)}
}

func lastPass(id string) []sqltest.Response {
	return []sqltest.Response{noDirectory(), staleRow(id), {Affected: 1}, noDirectory(), noFile()}
}

const passOps = "query query exec query query"

type passes struct {
	results []data.SweepResult
	errs    []error
}

func (p *passes) report(res data.SweepResult, err error) {
	p.results = append(p.results, res)
	p.errs = append(p.errs, err)
}

func sweepPass(s *data.Store, db *sqlate.DB, opts data.SweepOptions) func(context.Context) (data.SweepResult, error) {
	return func(ctx context.Context) (data.SweepResult, error) {
		return s.Sweep(ctx, db, &objectLog{}, opts)
	}
}

func TestSweepUntilDone(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback, slices.Concat(morePass("A", "B"), lastPass("B"))...)
	var got passes
	if err := data.SweepUntilDone(ctx, nil, sweepPass(s, db, loopOpts), got.report); err != nil {
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
	if err := data.SweepUntilDone(ctx, nil, sweepPass(s, db, loopOpts), got.report); err != nil || len(got.results) != 1 || got.results[0] != (data.SweepResult{}) {
		t.Errorf("SweepUntilDone with nothing to do = %v after %+v, want one empty pass", err, got.results)
	}
	if ops(rec) != "query query" {
		t.Errorf("ops = %q, want the one pass's reads", ops(rec))
	}

	s, db, rec = openStore(t, fallback, slices.Concat(morePass("A", "B"), lastPass("B"))...)
	if err := data.SweepUntilDone(ctx, nil, sweepPass(s, db, loopOpts), nil); err != nil {
		t.Fatalf("SweepUntilDone with a nil report = %v, want nil", err)
	}
	if ops(rec) != passOps+" "+passOps {
		t.Errorf("ops = %q, want the two passes", ops(rec))
	}
}

func TestSweepUntilDoneRefusals(t *testing.T) {
	ctx := context.Background()
	refused := sqltest.Response{Err: errors.New("the purge was refused")}
	firstRefusedNextReclaimedThirdBeyond := []sqltest.Response{noDirectory(), staleRow("A"), refused, staleRow("B"), sqltest.Response{Affected: 1}, noDirectory(), staleRow("C")}
	thirdRefusedNothingBeyond := []sqltest.Response{noDirectory(), staleRow("C"), refused, noFile()}
	s, db, rec := openStore(t, fallback, slices.Concat(firstRefusedNextReclaimedThirdBeyond, thirdRefusedNothingBeyond)...)
	var got passes
	if err := data.SweepUntilDone(ctx, nil, sweepPass(s, db, loopOpts), got.report); err != nil {
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
	if err := data.SweepUntilDone(ctx, nil, sweepPass(s, db, data.SweepOptions{Batch: -1}), got.report); err != nil || len(got.results) != 1 || got.errs[0] == nil || len(rec.Calls()) != 0 {
		t.Errorf("SweepUntilDone with a refused batch = %v after %+v, %v, want the refusal reported once and no statement", err, got.results, got.errs)
	}
}

func TestSweepUntilDoneStops(t *testing.T) {
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	s, db, rec := openStore(t, fallback)
	var got passes
	if err := data.SweepUntilDone(ended, nil, sweepPass(s, db, data.SweepOptions{}), got.report); !errors.Is(err, context.Canceled) || len(got.results) != 0 || len(rec.Calls()) != 0 {
		t.Errorf("SweepUntilDone on an ended context = %v after %d passes, want no pass", err, len(got.results))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, db, rec = openStore(t, fallback, morePass("A", "B")...)
	got = passes{}
	calls := 0
	pass := sweepPass(s, db, loopOpts)
	cancelsBeforeTheSecondPass := func(ctx context.Context) (data.SweepResult, error) {
		if calls++; calls == 2 {
			cancel()
		}
		return pass(ctx)
	}
	if err := data.SweepUntilDone(ctx, nil, cancelsBeforeTheSecondPass, got.report); !errors.Is(err, context.Canceled) {
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
	if err := data.SweepUntilDone(context.Background(), stop, sweepPass(s, db, loopOpts), report); err != nil {
		t.Fatalf("SweepUntilDone = %v, want nil once stop closes", err)
	}
	if len(got.results) != 1 || !got.results[0].More || ops(rec) != passOps {
		t.Errorf("reported %+v after %q, want the pass in flight alone, though it reported More", got.results, ops(rec))
	}
}
