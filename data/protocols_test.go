package data_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"
	"github.com/standards-lab/sqlate/sqltest"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data"
	"github.com/standards-lab/blobfs/data/datatest"
)

// The file the protocol tests write and delete: a fixed id, as a seed's,
// in a directory under the root.
const (
	protocolFile = "00000000-0000-7000-8000-00000000000f"
	protocolDir  = "00000000-0000-7000-8000-00000000000d"
	protocolKey  = protocolFile + "/report.txt"
)

// objectStore is an ObjectStore over a map. It accepts every key, records
// its puts, its deletes, and the error of the context each delete ran
// under, and fails a put with putErr and a delete with deleteErr when they
// are set. With
// cancel set, a put cancels the caller's context and fails with its error,
// as a caller that hangs up mid-body.
type objectStore struct {
	accepting
	objects    map[string]string
	puts       int
	lastType   string
	deletes    []string
	deleteCtxs []error
	putErr     error
	deleteErr  error
	cancel     context.CancelFunc
}

func newObjectStore() *objectStore { return &objectStore{objects: map[string]string{}} }

func (o *objectStore) PutObject(ctx context.Context, key string, body io.Reader, contentType string, size int64) (blobfs.Object, error) {
	o.puts++
	o.lastType = contentType
	if o.cancel != nil {
		o.cancel()
		return blobfs.Object{}, ctx.Err()
	}
	if o.putErr != nil {
		return blobfs.Object{}, o.putErr
	}
	b, err := io.ReadAll(body)
	if err != nil {
		return blobfs.Object{}, err
	}
	if int64(len(b)) != size {
		return blobfs.Object{}, fmt.Errorf("read %d bytes of %d", len(b), size)
	}
	o.objects[key] = string(b)
	return blobfs.Object{Size: size, ContentType: contentType, ETag: `"etag"`}, nil
}

func (o *objectStore) DeleteObject(ctx context.Context, key string) error {
	o.deletes = append(o.deletes, key)
	o.deleteCtxs = append(o.deleteCtxs, ctx.Err())
	if o.deleteErr != nil {
		return o.deleteErr
	}
	delete(o.objects, key)
	return nil
}

// stored reports whether the store holds the protocol file's object.
func (o *objectStore) stored() bool {
	_, ok := o.objects[protocolKey]
	return ok
}

// single is the returning dialect's form, where each of the store's
// commands is one statement, so a protocol's script is short.
var single = forms[1]

// protocolRow is the protocol file's row at a status and version; one
// past pending carries the size and entity tag its completion recorded.
func protocolRow(status blobfs.Status, version int64) sqltest.Response {
	f := fileRow(protocolFile, protocolDir, "report.txt", status, version)
	if status != blobfs.StatusPending {
		size, etag := int64(6), `"etag"`
		f.Size, f.ETag = &size, &etag
	}
	return datatest.FileRows(f)
}

// protocolDirectory is the read of the protocol file's directory, active.
func protocolDirectory() sqltest.Response {
	return directoryResponse(protocolDir, blobfs.RootID, "d", 1)
}

// create is a write's first step as a caller's begin runs it: a check of
// its own, then Files.Create.
func create(ctx context.Context, s *data.Store, own func(*sqlate.Tx) error) func(*sqlate.Tx) (blobfs.File, error) {
	return func(tx *sqlate.Tx) (blobfs.File, error) {
		if err := own(tx); err != nil {
			return blobfs.File{}, err
		}
		return s.Files.Create(ctx, tx, accepting{}, protocolDir, "report.txt", "text/plain")
	}
}

func allow(*sqlate.Tx) error { return nil }

// wantDone fails the test unless every scripted response was consumed and
// no rows were left open.
func wantDone(t *testing.T, rec *sqltest.Recorder) {
	t.Helper()
	if rec.Pending() != 0 || rec.RowsLeaked() != 0 {
		t.Errorf("pending = %d, leaked = %d", rec.Pending(), rec.RowsLeaked())
	}
}

// TestWrite checks the write: begin's transaction with the pending row,
// the put under the row's key in the type the row declares, and the
// completion on the pool.
func TestWrite(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single, protocolRow(blobfs.StatusPending, 1), protocolRow(blobfs.StatusAvailable, 2))
	objects := newObjectStore()
	got, err := s.WriteFile(ctx, db, objects, strings.NewReader("report"), 6, create(ctx, s, allow))
	if err != nil || got.Status != blobfs.StatusAvailable || got.Version != 2 {
		t.Fatalf("Write = %+v, %v, want the available row", got, err)
	}
	if objects.lastType != "text/plain" || objects.objects[protocolKey] != "report" {
		t.Errorf("the put stored %q as %q, want the body under the row's key in the row's type", objects.objects[protocolKey], objects.lastType)
	}
	if want := "begin query commit query"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
	wantDone(t, rec)
}

// TestWriteRefusedBegin checks a begin that fails rolls back, stores
// nothing, and returns its error as it came.
func TestWriteRefusedBegin(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single)
	objects := newObjectStore()
	refused := errors.New("out of scope")
	_, err := s.WriteFile(ctx, db, objects, strings.NewReader("report"), 6, create(ctx, s, func(*sqlate.Tx) error { return refused }))
	if !errors.Is(err, refused) || strings.Contains(err.Error(), "data: ") {
		t.Fatalf("Write = %v, want begin's error as it came", err)
	}
	if objects.puts != 0 || ops(rec) != "begin rollback" {
		t.Errorf("puts = %d, ops = %q, want nothing past the rollback", objects.puts, ops(rec))
	}
}

// TestWriteUnderADeletingName checks a write under a name a deleting row
// holds, a stopped write's or delete's, is refused in begin by that row's
// DeletingError, not as the taken name, with nothing put and no statement
// failed inside the transaction.
func TestWriteUnderADeletingName(t *testing.T) {
	ctx := context.Background()
	const holder = "00000000-0000-7000-8000-0000000000c1"
	s, db, rec := openStore(t, single, noFile(), noFile(), protocolDirectory(),
		fileIn(holder, protocolDir, "report.txt", blobfs.StatusDeleting, 2), protocolDirectory())
	objects := newObjectStore()
	_, err := s.WriteFile(ctx, db, objects, strings.NewReader("report"), 6, create(ctx, s, allow))
	if errors.Is(err, blobfs.ErrNameTaken) {
		t.Errorf("Write = %v, want no ErrNameTaken", err)
	}
	wantDeleting(t, "Write under a deleting row's name", err, false, holder)
	if objects.puts != 0 || ops(rec) != "begin query query query query query rollback" {
		t.Errorf("puts = %d, ops = %q, want nothing put and begin's reads rolled back", objects.puts, ops(rec))
	}
	wantDone(t, rec)
}

// TestWriteFailedPut checks a failed put abandons the write through the
// delete: the row's delete begun at its pending version, the object's
// delete, and the purge. An abandon the store refuses too leaves the row
// deleting and reports both failures.
func TestWriteFailedPut(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single, protocolRow(blobfs.StatusPending, 1), protocolRow(blobfs.StatusDeleting, 2), sqltest.Response{Affected: 1})
	objects := newObjectStore()
	objects.putErr = errors.New("put failed")
	_, err := s.WriteFile(ctx, db, objects, strings.NewReader("report"), 6, create(ctx, s, allow))
	if !errors.Is(err, objects.putErr) || !strings.HasPrefix(err.Error(), "data: write file "+protocolFile+": put the object: ") {
		t.Fatalf("Write = %v, want the put's error, named", err)
	}
	if want := "begin query commit begin query commit exec"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
	if del := rec.Calls()[4]; !strings.HasPrefix(del.SQL, "UPDATE blobfs_file") || !slices.Equal(del.Args, []any{protocolFile, int64(1)}) {
		t.Errorf("the delete is %q %v, want the pending row's, at its version", del.SQL, del.Args)
	}
	if !slices.Equal(objects.deletes, []string{protocolKey}) {
		t.Errorf("deletes = %v, want the abandoned key", objects.deletes)
	}
	wantDone(t, rec)

	s, db, rec = openStore(t, single, protocolRow(blobfs.StatusPending, 1), protocolRow(blobfs.StatusDeleting, 2))
	objects = newObjectStore()
	objects.putErr, objects.deleteErr = errors.New("the store is down"), errors.New("the store is still down")
	_, err = s.WriteFile(ctx, db, objects, strings.NewReader("report"), 6, create(ctx, s, allow))
	if !errors.Is(err, objects.putErr) || !errors.Is(err, objects.deleteErr) || !strings.Contains(err.Error(), "abandon: delete the object: ") {
		t.Fatalf("Write = %v, want the put's error and the abandon's", err)
	}
	if want := "begin query commit begin query commit"; ops(rec) != want {
		t.Errorf("ops = %q, want %q: no purge after a refused object delete", ops(rec), want)
	}
	wantDone(t, rec)
}

// TestWriteRefusedCompletion checks the writer rule: a completion refused
// because the row is deleting or gone deletes the object under the key
// the write holds and leaves the row to the sweep.
func TestWriteRefusedCompletion(t *testing.T) {
	for _, c := range []struct {
		name string
		read []sqltest.Response
		want error
		ops  string
	}{
		{"Deleting", []sqltest.Response{protocolRow(blobfs.StatusDeleting, 2), protocolDirectory()}, blobfs.ErrDeleting, "begin query commit query query query"},
		{"Removed", []sqltest.Response{noFile()}, blobfs.ErrNotFound, "begin query commit query query"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			s, db, rec := openStore(t, single, append([]sqltest.Response{protocolRow(blobfs.StatusPending, 1), noFile()}, c.read...)...)
			objects := newObjectStore()
			if _, err := s.WriteFile(ctx, db, objects, strings.NewReader("report"), 6, create(ctx, s, allow)); !errors.Is(err, c.want) {
				t.Fatalf("Write = %v, want %v", err, c.want)
			}
			if objects.puts != 1 || objects.stored() || !slices.Equal(objects.deletes, []string{protocolKey}) {
				t.Errorf("puts = %d, deletes = %v, stored = %v, want the object put once, then deleted", objects.puts, objects.deletes, objects.stored())
			}
			if ops(rec) != c.ops {
				t.Errorf("ops = %q, want %q", ops(rec), c.ops)
			}
			wantDone(t, rec)
		})
	}
}

// TestWriteFailedCompletion checks a completion that fails any other way
// abandons the write, its object deleted with the row.
func TestWriteFailedCompletion(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single,
		protocolRow(blobfs.StatusPending, 1),
		sqltest.Response{Err: sqlate.ErrConnectionFailed},
		protocolRow(blobfs.StatusDeleting, 2), sqltest.Response{Affected: 1},
	)
	objects := newObjectStore()
	if _, err := s.WriteFile(ctx, db, objects, strings.NewReader("report"), 6, create(ctx, s, allow)); !errors.Is(err, sqlate.ErrConnectionFailed) {
		t.Fatalf("Write = %v, want the completion's error", err)
	}
	if objects.stored() {
		t.Error("the object outlived the abandoned write")
	}
	if want := "begin query commit query begin query commit exec"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
	wantDone(t, rec)
}

// found is a write's first step whose begin runs Files.Ensure, which
// may find a row that is no longer pending.
func found(ctx context.Context, s *data.Store) func(*sqlate.Tx) (blobfs.File, error) {
	return func(tx *sqlate.Tx) (blobfs.File, error) {
		f, _, err := s.Files.Ensure(ctx, tx, accepting{}, protocolDir, "report.txt", "text/plain")
		return f, err
	}
}

// TestWriteRefusesARowNotPending checks Write writes only a pending row:
// an available row begin found is refused with its TransitionError, and a
// deleting one with its DeletingError wrapping it, each before any put,
// nothing removed.
func TestWriteRefusesARowNotPending(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single, protocolRow(blobfs.StatusAvailable, 2))
	objects := newObjectStore()
	_, err := s.WriteFile(ctx, db, objects, strings.NewReader("report"), 6, found(ctx, s))
	var te *blobfs.TransitionError
	if !errors.As(err, &te) || te.From != blobfs.StatusAvailable || te.To != blobfs.StatusAvailable || errors.Is(err, blobfs.ErrDeleting) ||
		!strings.HasPrefix(err.Error(), "data: write file "+protocolFile+": ") {
		t.Errorf("Write of an available row = %v, want its TransitionError to available, named", err)
	}
	if objects.puts != 0 || len(objects.deletes) != 0 || ops(rec) != "begin query commit" {
		t.Errorf("puts = %d, deletes = %v, ops = %q, want the first transaction alone", objects.puts, objects.deletes, ops(rec))
	}
	wantDone(t, rec)

	s, db, rec = openStore(t, single, protocolRow(blobfs.StatusDeleting, 3), protocolDirectory())
	_, err = s.WriteFile(ctx, db, objects, strings.NewReader("report"), 6, found(ctx, s))
	wantDeleting(t, "Write of a deleting row", err, false, protocolFile)
	if !errors.As(err, &te) || te.From != blobfs.StatusDeleting {
		t.Errorf("Write of a deleting row = %v, want its TransitionError from deleting", err)
	}
	if objects.puts != 0 || len(objects.deletes) != 0 || ops(rec) != "begin query commit query" {
		t.Errorf("puts = %d, deletes = %v, ops = %q, want the directory's read alone past the first transaction", objects.puts, objects.deletes, ops(rec))
	}
	wantDone(t, rec)
}

// hangUp is a context the caller abandons once the database has answered
// every scripted statement: from then on it is done and reports
// context.Canceled, so whatever runs after the last statement runs with
// the caller gone.
type hangUp struct {
	context.Context
	rec *sqltest.Recorder
}

var hungUp = func() chan struct{} { c := make(chan struct{}); close(c); return c }()

func (h hangUp) Done() <-chan struct{} {
	if h.rec.Pending() == 0 {
		return hungUp
	}
	return nil
}

func (h hangUp) Err() error {
	if h.rec.Pending() == 0 {
		return context.Canceled
	}
	return nil
}

// TestWriteCleansUpAfterCancellation checks the cleanup outlives the
// caller's context: a put that fails because the caller hung up is still
// abandoned, the row's delete, the object's, and the purge all run; and a
// completion refused as deleting still deletes the object put when the
// caller hangs up before the delete, each delete under a context that is
// not cancelled.
func TestWriteCleansUpAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, db, rec := openStore(t, single, protocolRow(blobfs.StatusPending, 1), protocolRow(blobfs.StatusDeleting, 2), sqltest.Response{Affected: 1})
	objects := newObjectStore()
	objects.cancel = cancel
	if _, err := s.WriteFile(ctx, db, objects, strings.NewReader("report"), 6, create(ctx, s, allow)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Write = %v, want the put's cancellation", err)
	}
	if want := "begin query commit begin query commit exec"; ops(rec) != want {
		t.Errorf("ops = %q, want %q: the abandon run whole", ops(rec), want)
	}
	if !slices.Equal(objects.deletes, []string{protocolKey}) || !slices.Equal(objects.deleteCtxs, []error{nil}) {
		t.Errorf("deletes = %v under %v, want the abandoned key under a live context", objects.deletes, objects.deleteCtxs)
	}
	wantDone(t, rec)

	s, db, rec = openStore(t, single, protocolRow(blobfs.StatusPending, 1), noFile(), protocolRow(blobfs.StatusDeleting, 2), protocolDirectory())
	objects = newObjectStore()
	caller := hangUp{context.Background(), rec}
	// The directory's read is the last statement, so the caller has hung
	// up by the time the refusal is classified; a read the hang-up cuts
	// short leaves the refusal untyped, still ErrDeleting.
	if _, err := s.WriteFile(caller, db, objects, strings.NewReader("report"), 6, create(caller, s, allow)); !errors.Is(err, blobfs.ErrDeleting) {
		t.Fatalf("Write = %v, want ErrDeleting", err)
	}
	if caller.Err() == nil || !slices.Equal(objects.deletes, []string{protocolKey}) || !slices.Equal(objects.deleteCtxs, []error{nil}) || objects.stored() {
		t.Errorf("caller %v, deletes = %v under %v, want the put object deleted under a live context after the hang-up", caller.Err(), objects.deletes, objects.deleteCtxs)
	}
	wantDone(t, rec)
}

// ensure is a seed's first step as its begin runs it: Files.Ensure of the
// file under the fixed id.
func ensure(ctx context.Context, s *data.Store) func(*sqlate.Tx) (blobfs.File, data.WriteOutcome, error) {
	return func(tx *sqlate.Tx) (blobfs.File, data.WriteOutcome, error) {
		return s.Files.Ensure(ctx, tx, accepting{}, protocolDir, "report.txt", "text/plain", data.WithID(protocolFile))
	}
}

// TestEnsureWrite checks the retry-safe write: a name no row holds is
// created under the fixed id and written over whatever a reset left at
// the key, and a pending row an earlier write left is resumed.
func TestEnsureWrite(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single, noFile(), protocolRow(blobfs.StatusPending, 1), protocolRow(blobfs.StatusAvailable, 2))
	objects := newObjectStore()
	objects.objects[protocolKey] = "stale bytes"
	got, stored, err := s.EnsureFile(ctx, db, objects, protocolFile, strings.NewReader("report"), 6, ensure(ctx, s))
	if err != nil || !stored || got.Status != blobfs.StatusAvailable {
		t.Fatalf("Ensure = %+v, %v, %v, want the file stored", got, stored, err)
	}
	if insert := rec.Calls()[2]; !strings.HasPrefix(insert.SQL, "INSERT INTO blobfs_file") || insert.Args[0] != protocolFile || insert.Args[2] != protocolKey {
		t.Errorf("the insert is %q %v, want the fixed id and its key", insert.SQL, insert.Args)
	}
	if objects.objects[protocolKey] != "report" {
		t.Errorf("object = %q, want the leftover replaced by the write's bytes", objects.objects[protocolKey])
	}
	if want := "begin query query commit query"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
	wantDone(t, rec)

	s, db, rec = openStore(t, single, protocolRow(blobfs.StatusPending, 1), protocolRow(blobfs.StatusAvailable, 2))
	objects = newObjectStore()
	if _, stored, err := s.EnsureFile(ctx, db, objects, protocolFile, strings.NewReader("report"), 6, ensure(ctx, s)); err != nil || !stored || objects.puts != 1 {
		t.Fatalf("Ensure = %v, %v after %d puts, want the pending row put once and completed", stored, err, objects.puts)
	}
	if want := "begin query commit query"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
	wantDone(t, rec)
}

// TestEnsureWritePresent checks an available row under the id is left as
// it stands, nothing put; a deleting one is refused as its own delete's,
// told by a read of its directory.
func TestEnsureWritePresent(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single, protocolRow(blobfs.StatusAvailable, 2))
	objects := newObjectStore()
	got, stored, err := s.EnsureFile(ctx, db, objects, protocolFile, strings.NewReader("report"), 6, ensure(ctx, s))
	if err != nil || stored || got.ID != protocolFile || objects.puts != 0 || ops(rec) != "begin query commit" {
		t.Errorf("Ensure of an available row = %+v, %v, %v after %d puts and %q, want the row as it stands", got, stored, err, objects.puts, ops(rec))
	}

	s, db, rec = openStore(t, single, protocolRow(blobfs.StatusDeleting, 3), protocolDirectory())
	_, stored, err = s.EnsureFile(ctx, db, objects, protocolFile, strings.NewReader("report"), 6, ensure(ctx, s))
	wantDeleting(t, "Ensure of a deleting row", err, false, protocolFile)
	if stored || objects.puts != 0 || ops(rec) != "begin query commit query" {
		t.Errorf("Ensure of a deleting row stored %v after %d puts and %q, want nothing put", stored, objects.puts, ops(rec))
	}
	wantDone(t, rec)
}

// TestEnsureWriteUnderAnotherID checks a row found by the name under
// another id, an upload of the same name, is left as it stands, nothing
// put over its object and nothing removed, and reported as the taken
// name, or as its DeletingError when it is deleting; a row a begin without
// WithID(id) created under another id is abandoned at its version, nothing
// put, and reported naming both ids.
func TestEnsureWriteUnderAnotherID(t *testing.T) {
	const other = "00000000-0000-7000-8000-0000000000c1"
	for _, status := range []blobfs.Status{blobfs.StatusPending, blobfs.StatusAvailable} {
		t.Run(string(status), func(t *testing.T) {
			ctx := context.Background()
			s, db, rec := openStore(t, single, fileIn(other, protocolDir, "report.txt", status, 2))
			objects := newObjectStore()
			got, stored, err := s.EnsureFile(ctx, db, objects, protocolFile, strings.NewReader("report"), 6, ensure(ctx, s))
			if !errors.Is(err, blobfs.ErrNameTaken) || stored || got.ID != "" {
				t.Fatalf("Ensure = %+v, %v, %v, want the other row reported as the taken name", got, stored, err)
			}
			if objects.puts != 0 || len(objects.deletes) != 0 || ops(rec) != "begin query commit" {
				t.Errorf("puts = %d, deletes = %v, ops = %q, want the first transaction alone", objects.puts, objects.deletes, ops(rec))
			}
		})
	}

	// A deleting row under another id is refused by its delete, not as the
	// taken name, and is not run again as a concurrent writer's.
	t.Run(string(blobfs.StatusDeleting), func(t *testing.T) {
		ctx := context.Background()
		s, db, rec := openStore(t, single, fileIn(other, protocolDir, "report.txt", blobfs.StatusDeleting, 2), protocolDirectory())
		objects := newObjectStore()
		got, stored, err := s.EnsureFile(ctx, db, objects, protocolFile, strings.NewReader("report"), 6, ensure(ctx, s))
		if errors.Is(err, blobfs.ErrNameTaken) || stored || got.ID != "" {
			t.Errorf("Ensure = %+v, %v, %v, want the deleting row's refusal", got, stored, err)
		}
		wantDeleting(t, "Ensure over a deleting row under another id", err, false, other)
		if objects.puts != 0 || len(objects.deletes) != 0 || ops(rec) != "begin query commit query" {
			t.Errorf("puts = %d, deletes = %v, ops = %q, want the first transaction and the directory's read", objects.puts, objects.deletes, ops(rec))
		}
		wantDone(t, rec)
	})

	t.Run("Created", func(t *testing.T) {
		ctx := context.Background()
		s, db, rec := openStore(t, single,
			noFile(), fileIn(other, protocolDir, "report.txt", blobfs.StatusPending, 1), // created under a minted id
			fileIn(other, protocolDir, "report.txt", blobfs.StatusDeleting, 2), sqltest.Response{Affected: 1}, // abandoned
		)
		objects := newObjectStore()
		minted := func(tx *sqlate.Tx) (blobfs.File, data.WriteOutcome, error) {
			return s.Files.Ensure(ctx, tx, accepting{}, protocolDir, "report.txt", "text/plain")
		}
		got, stored, err := s.EnsureFile(ctx, db, objects, protocolFile, strings.NewReader("report"), 6, minted)
		if err == nil || errors.Is(err, blobfs.ErrNameTaken) || stored || got.ID != "" ||
			!strings.Contains(err.Error(), other) || !strings.Contains(err.Error(), protocolFile) {
			t.Fatalf("Ensure = %+v, %v, %v, want a refusal naming both ids", got, stored, err)
		}
		if want := "begin query query commit begin query commit exec"; objects.puts != 0 || ops(rec) != want {
			t.Errorf("puts = %d, ops = %q, want nothing put and %q", objects.puts, ops(rec), want)
		}
		if del := rec.Calls()[5]; !slices.Equal(del.Args, []any{other, int64(1)}) {
			t.Errorf("the delete bound %v, want the created row at its version", del.Args)
		}
		wantDone(t, rec)
	})
}

// TestEnsureWriteRaces checks two writers sharing a row: a completion the
// other writer won is no failure, abandons nothing, and returns the row
// as found; a lost insert runs begin again in a fresh transaction, which
// resumes the other's pending row.
func TestEnsureWriteRaces(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single,
		noFile(), protocolRow(blobfs.StatusPending, 1), // created
		noFile(), protocolRow(blobfs.StatusAvailable, 2), // the completion matches no pending row at 1
		protocolRow(blobfs.StatusAvailable, 2), // the row read back
	)
	objects := newObjectStore()
	got, stored, err := s.EnsureFile(ctx, db, objects, protocolFile, strings.NewReader("report"), 6, ensure(ctx, s))
	if err != nil || stored || got.Status != blobfs.StatusAvailable || got.Version != 2 {
		t.Fatalf("Ensure = %+v, %v, %v, want the other writer's row, found", got, stored, err)
	}
	if len(objects.deletes) != 0 || ops(rec) != "begin query query commit query query query" {
		t.Errorf("deletes = %v, ops = %q, want no abandon", objects.deletes, ops(rec))
	}
	wantDone(t, rec)

	s, db, rec = openStore(t, single,
		noFile(), violation(blobfs.ConstraintPrimaryKeyFile, sqlate.ErrUniqueViolation), // the lookup finds nothing, the insert loses
		protocolRow(blobfs.StatusPending, 1),   // the retry finds the other's pending row
		protocolRow(blobfs.StatusAvailable, 2), // and completes it
	)
	if _, stored, err := s.EnsureFile(ctx, db, newObjectStore(), protocolFile, strings.NewReader("report"), 6, ensure(ctx, s)); err != nil || !stored {
		t.Fatalf("Ensure = %v, %v, want the resumed row stored", stored, err)
	}
	if want := "begin query query rollback begin query commit query"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
	wantDone(t, rec)
}

// TestEnsureWriteFailedPut checks a failed put abandons the created row
// as Write abandons it, but only at the pending version it holds.
func TestEnsureWriteFailedPut(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single,
		noFile(), protocolRow(blobfs.StatusPending, 1),
		protocolRow(blobfs.StatusDeleting, 2), sqltest.Response{Affected: 1},
	)
	objects := newObjectStore()
	objects.putErr = errors.New("put failed")
	if _, stored, err := s.EnsureFile(ctx, db, objects, protocolFile, strings.NewReader("report"), 6, ensure(ctx, s)); !errors.Is(err, objects.putErr) || stored {
		t.Fatalf("Ensure = %v, %v, want the put's error", stored, err)
	}
	if want := "begin query query commit begin query commit exec"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
	if del := rec.Calls()[5]; !strings.HasPrefix(del.SQL, "UPDATE blobfs_file") || !slices.Equal(del.Args, []any{protocolFile, int64(1)}) {
		t.Errorf("the delete is %q %v, want the pending row's, at its version", del.SQL, del.Args)
	}
	wantDone(t, rec)
}

// TestEnsureWriteAbandonAfterTheOtherWriter checks a failed put whose
// abandon, at the pending version, finds the row the other writer
// completed: nothing removed, and the row read back and returned as found.
func TestEnsureWriteAbandonAfterTheOtherWriter(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single, slices.Concat(
		[]sqltest.Response{protocolRow(blobfs.StatusPending, 1)},      // resumed
		unchangedFile(single, protocolRow(blobfs.StatusAvailable, 2)), // the abandon at 1 finds it completed
		[]sqltest.Response{protocolRow(blobfs.StatusAvailable, 2)},    // the row read back
	)...)
	objects := newObjectStore()
	objects.putErr = errors.New("put failed")
	got, stored, err := s.EnsureFile(ctx, db, objects, protocolFile, strings.NewReader("report"), 6, ensure(ctx, s))
	if err != nil || stored || got.Status != blobfs.StatusAvailable || got.Version != 2 {
		t.Fatalf("Ensure = %+v, %v, %v, want the other writer's row, found", got, stored, err)
	}
	if want := "begin query commit begin query query rollback query"; len(objects.deletes) != 0 || ops(rec) != want {
		t.Errorf("deletes = %v, ops = %q, want nothing removed and %q", objects.deletes, ops(rec), want)
	}
	wantDone(t, rec)
}

// TestEnsureWriteRefusedCompletion checks a completion refused as
// deleting or not found is returned as it came, the object put deleted
// again, with no read back as if another writer had won.
func TestEnsureWriteRefusedCompletion(t *testing.T) {
	for _, c := range []struct {
		name string
		read []sqltest.Response
		want error
		ops  string
	}{
		{"Deleting", []sqltest.Response{protocolRow(blobfs.StatusDeleting, 2), protocolDirectory()}, blobfs.ErrDeleting, "begin query commit query query query"},
		{"Removed", []sqltest.Response{noFile()}, blobfs.ErrNotFound, "begin query commit query query"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			s, db, rec := openStore(t, single, append([]sqltest.Response{protocolRow(blobfs.StatusPending, 1), noFile()}, c.read...)...)
			objects := newObjectStore()
			_, stored, err := s.EnsureFile(ctx, db, objects, protocolFile, strings.NewReader("report"), 6, ensure(ctx, s))
			if !errors.Is(err, c.want) || stored {
				t.Fatalf("Ensure = %v, %v, want %v", stored, err, c.want)
			}
			if c.want == blobfs.ErrDeleting {
				wantDeleting(t, "Ensure across a delete", err, false, protocolFile)
				if errors.Is(err, blobfs.ErrNotFound) {
					t.Errorf("Ensure across a delete = %v, want no ErrNotFound beside it", err)
				}
			}
			if !slices.Equal(objects.deletes, []string{protocolKey}) || objects.stored() || ops(rec) != c.ops {
				t.Errorf("deletes = %v, ops = %q, want the object deleted and %q", objects.deletes, ops(rec), c.ops)
			}
			wantDone(t, rec)
		})
	}
}

// TestEnsureWriteRefusesAnID checks an id blobfs.ParseID refuses is
// refused before any SQL.
func TestEnsureWriteRefusesAnID(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single)
	if _, _, err := s.EnsureFile(ctx, db, newObjectStore(), "seed", strings.NewReader("report"), 6, ensure(ctx, s)); !errors.Is(err, blobfs.ErrInvalidID) {
		t.Errorf("Ensure under a malformed id = %v, want ErrInvalidID", err)
	}
	if len(rec.Calls()) != 0 {
		t.Errorf("the refused id ran %q", ops(rec))
	}
}

// TestRemove checks the delete: pick in the transaction that begins the
// delete, under the version guard, then the object, then the purge on the
// pool; a pick that fails rolls back and deletes nothing.
func TestRemove(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single, sqltest.Response{Affected: 1}, protocolRow(blobfs.StatusDeleting, 3), sqltest.Response{Affected: 1})
	objects := newObjectStore()
	objects.objects[protocolKey] = "report"
	err := s.RemoveFile(ctx, db, objects, func(tx *sqlate.Tx) (string, error) {
		_, err := tx.ExecContext(ctx, "DELETE FROM owner WHERE file_id = $1", protocolFile)
		return protocolFile, err
	}, data.AtVersion(2))
	if err != nil {
		t.Fatalf("Remove = %v", err)
	}
	if objects.stored() {
		t.Error("the object outlived its remove")
	}
	if want := "begin exec query commit exec"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
	if del := rec.Calls()[2]; !slices.Equal(del.Args, []any{protocolFile, int64(2)}) {
		t.Errorf("the delete bound %v, want the file at the guarded version", del.Args)
	}
	wantDone(t, rec)

	s, db, rec = openStore(t, single)
	refused := errors.New("out of scope")
	if err := s.RemoveFile(ctx, db, objects, func(*sqlate.Tx) (string, error) { return "", refused }); !errors.Is(err, refused) || strings.Contains(err.Error(), "data: ") {
		t.Fatalf("Remove = %v, want pick's error as it came", err)
	}
	if ops(rec) != "begin rollback" || len(objects.deletes) != 1 {
		t.Errorf("ops = %q, deletes = %v, want nothing deleted", ops(rec), objects.deletes)
	}

	s, db, _ = openStore(t, single, unchangedFile(single, fileIn(protocolFile, protocolDir, "report.txt", blobfs.StatusAvailable, 3))...)
	err = s.RemoveFile(ctx, db, objects, func(*sqlate.Tx) (string, error) { return protocolFile, nil }, data.AtVersion(2))
	if !errors.Is(err, query.ErrVersionMismatch) || !strings.HasPrefix(err.Error(), "data: remove file "+protocolFile+": ") {
		t.Errorf("Remove at a stale version = %v, want ErrVersionMismatch, named", err)
	}
}

// TestRemoveFileID checks the delete of a file by id: the delete under the
// version guard in a transaction of its own, then the object, then the
// purge; a retry after it finished finds no row, ErrNotFound.
func TestRemoveFileID(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single, protocolRow(blobfs.StatusDeleting, 3), sqltest.Response{Affected: 1}, noFile(), noFile())
	objects := newObjectStore()
	objects.objects[protocolKey] = "report"
	if err := s.RemoveFileID(ctx, db, objects, protocolFile, data.AtVersion(2)); err != nil {
		t.Fatalf("RemoveFileID = %v", err)
	}
	if objects.stored() {
		t.Error("the object outlived its remove")
	}
	if want := "begin query commit exec"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
	if del := rec.Calls()[1]; !slices.Equal(del.Args, []any{protocolFile, int64(2)}) {
		t.Errorf("the delete bound %v, want the file at the guarded version", del.Args)
	}
	err := s.RemoveFileID(ctx, db, objects, protocolFile)
	if !errors.Is(err, blobfs.ErrNotFound) || !strings.HasPrefix(err.Error(), "data: remove file "+protocolFile+": ") {
		t.Errorf("RemoveFileID retried after it finished = %v, want ErrNotFound, named", err)
	}
	wantDone(t, rec)
}

// TestPurge checks the delete's tail: the object, then the row; an object
// delete that fails leaves the row.
func TestPurge(t *testing.T) {
	ctx := context.Background()
	file := fileRow(protocolFile, protocolDir, "report.txt", blobfs.StatusDeleting, 2)
	s, db, rec := openStore(t, single, sqltest.Response{Affected: 1})
	objects := newObjectStore()
	objects.objects[protocolKey] = "report"
	if err := s.PurgeFile(ctx, db, objects, file); err != nil || objects.stored() || ops(rec) != "exec" {
		t.Errorf("Purge = %v, stored = %v, ops = %q, want the object and the row gone", err, objects.stored(), ops(rec))
	}

	s, db, rec = openStore(t, single)
	objects.deleteErr = errors.New("the store is down")
	err := s.PurgeFile(ctx, db, objects, file)
	if !errors.Is(err, objects.deleteErr) || !strings.HasPrefix(err.Error(), "data: purge file "+protocolFile+": delete the object: ") || len(rec.Calls()) != 0 {
		t.Errorf("Purge over a failing store = %v after %q, want the store's error and no purge", err, ops(rec))
	}
}
