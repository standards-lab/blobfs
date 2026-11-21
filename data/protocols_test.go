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

const (
	protocolFile = "00000000-0000-7000-8000-00000000000f"
	protocolDir  = "00000000-0000-7000-8000-00000000000d"
	protocolKey  = protocolFile + "/report.txt"
)

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

func (o *objectStore) stored() bool {
	_, ok := o.objects[protocolKey]
	return ok
}

var single = forms[1]

func protocolRow(status blobfs.Status, version int64) sqltest.Response {
	f := fileRow(protocolFile, protocolDir, "report.txt", status, version)
	if status != blobfs.StatusPending {
		size, etag := int64(6), `"etag"`
		f.Size, f.ETag = &size, &etag
	}
	return datatest.FileRows(f)
}

func created() []sqltest.Response {
	return []sqltest.Response{noFile(), protocolRow(blobfs.StatusPending, 1)}
}

func resumed() []sqltest.Response {
	return []sqltest.Response{protocolRow(blobfs.StatusPending, 1)}
}

func completed() []sqltest.Response {
	return []sqltest.Response{protocolRow(blobfs.StatusAvailable, 2)}
}

func completedWithTheHooksExec() []sqltest.Response {
	return []sqltest.Response{protocolRow(blobfs.StatusAvailable, 2), {Affected: 1}}
}

func foundCompletedByTheOtherWriter() []sqltest.Response {
	return unchangedFile(single, protocolRow(blobfs.StatusAvailable, 2))
}

func readBack() []sqltest.Response {
	return []sqltest.Response{protocolRow(blobfs.StatusAvailable, 2)}
}

func abandoned() []sqltest.Response {
	return []sqltest.Response{protocolRow(blobfs.StatusDeleting, 2), {Affected: 1}}
}

func protocolDirectory() sqltest.Response {
	return directoryResponse(protocolDir, blobfs.RootID, "d", 1)
}

func create(ctx context.Context, s *data.Store, own func(*sqlate.Tx) error) func(*sqlate.Tx) (blobfs.File, error) {
	return func(tx *sqlate.Tx) (blobfs.File, error) {
		if err := own(tx); err != nil {
			return blobfs.File{}, err
		}
		return s.Files.Create(ctx, tx, accepting{}, data.FileSpec{DirectoryID: protocolDir, Name: "report.txt", ContentType: "text/plain"}, data.CreateOptions{})
	}
}

func allow(*sqlate.Tx) error { return nil }

func wantDone(t *testing.T, rec *sqltest.Recorder) {
	t.Helper()
	if rec.Pending() != 0 || rec.RowsLeaked() != 0 {
		t.Errorf("pending = %d, leaked = %d", rec.Pending(), rec.RowsLeaked())
	}
}

func TestWrite(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single, protocolRow(blobfs.StatusPending, 1), protocolRow(blobfs.StatusAvailable, 2))
	objects := newObjectStore()
	got, err := s.WriteFile(ctx, data.WriteFileRequest{DB: db, Objects: objects, Body: strings.NewReader("report"), Size: 6, Begin: create(ctx, s, allow)})
	if err != nil || got.Status != blobfs.StatusAvailable || got.Version != 2 {
		t.Fatalf("Write = %+v, %v, want the available row", got, err)
	}
	if objects.lastType != "text/plain" || objects.objects[protocolKey] != "report" {
		t.Errorf("the put stored %q as %q, want the body under the row's key in the row's type", objects.objects[protocolKey], objects.lastType)
	}
	if want := "begin query commit begin query commit"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
	wantDone(t, rec)
}

func TestWriteRefusedBegin(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single)
	objects := newObjectStore()
	refused := errors.New("out of scope")
	_, err := s.WriteFile(ctx, data.WriteFileRequest{DB: db, Objects: objects, Body: strings.NewReader("report"), Size: 6, Begin: create(ctx, s, func(*sqlate.Tx) error { return refused })})
	if !errors.Is(err, refused) || strings.Contains(err.Error(), "data: ") {
		t.Fatalf("Write = %v, want begin's error as it came", err)
	}
	if objects.puts != 0 || ops(rec) != "begin rollback" {
		t.Errorf("puts = %d, ops = %q, want nothing past the rollback", objects.puts, ops(rec))
	}
}

func TestWriteUnderADeletingName(t *testing.T) {
	ctx := context.Background()
	const holder = "00000000-0000-7000-8000-0000000000c1"
	s, db, rec := openStore(t, single, noFile(), noFile(), protocolDirectory(),
		fileIn(holder, protocolDir, "report.txt", blobfs.StatusDeleting, 2), protocolDirectory())
	objects := newObjectStore()
	_, err := s.WriteFile(ctx, data.WriteFileRequest{DB: db, Objects: objects, Body: strings.NewReader("report"), Size: 6, Begin: create(ctx, s, allow)})
	if errors.Is(err, blobfs.ErrNameTaken) {
		t.Errorf("Write = %v, want no ErrNameTaken", err)
	}
	wantDeleting(t, "Write under a deleting row's name", err, false, holder)
	if objects.puts != 0 || ops(rec) != "begin query query query query query rollback" {
		t.Errorf("puts = %d, ops = %q, want nothing put and begin's reads rolled back", objects.puts, ops(rec))
	}
	wantDone(t, rec)
}

func TestWriteFailedPut(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single, protocolRow(blobfs.StatusPending, 1), protocolRow(blobfs.StatusDeleting, 2), sqltest.Response{Affected: 1})
	objects := newObjectStore()
	objects.putErr = errors.New("put failed")
	_, err := s.WriteFile(ctx, data.WriteFileRequest{DB: db, Objects: objects, Body: strings.NewReader("report"), Size: 6, Begin: create(ctx, s, allow)})
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
	_, err = s.WriteFile(ctx, data.WriteFileRequest{DB: db, Objects: objects, Body: strings.NewReader("report"), Size: 6, Begin: create(ctx, s, allow)})
	if !errors.Is(err, objects.putErr) || !errors.Is(err, objects.deleteErr) || !strings.Contains(err.Error(), "abandon: delete the object: ") {
		t.Fatalf("Write = %v, want the put's error and the abandon's", err)
	}
	if want := "begin query commit begin query commit"; ops(rec) != want {
		t.Errorf("ops = %q, want %q: no purge after a refused object delete", ops(rec), want)
	}
	wantDone(t, rec)
}

func TestWriteRefusedCompletion(t *testing.T) {
	for _, c := range []struct {
		name string
		read []sqltest.Response
		want error
		ops  string
	}{
		{"Deleting", []sqltest.Response{protocolRow(blobfs.StatusDeleting, 2), protocolDirectory()}, blobfs.ErrDeleting, "begin query commit begin query query query rollback"},
		{"Removed", []sqltest.Response{noFile()}, blobfs.ErrNotFound, "begin query commit begin query query rollback"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			s, db, rec := openStore(t, single, append([]sqltest.Response{protocolRow(blobfs.StatusPending, 1), noFile()}, c.read...)...)
			objects := newObjectStore()
			if _, err := s.WriteFile(ctx, data.WriteFileRequest{DB: db, Objects: objects, Body: strings.NewReader("report"), Size: 6, Begin: create(ctx, s, allow)}); !errors.Is(err, c.want) {
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

func TestWriteFailedCompletion(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single,
		protocolRow(blobfs.StatusPending, 1),
		sqltest.Response{Err: sqlate.ErrConnectionFailed},
		protocolRow(blobfs.StatusDeleting, 2), sqltest.Response{Affected: 1},
	)
	objects := newObjectStore()
	if _, err := s.WriteFile(ctx, data.WriteFileRequest{DB: db, Objects: objects, Body: strings.NewReader("report"), Size: 6, Begin: create(ctx, s, allow)}); !errors.Is(err, sqlate.ErrConnectionFailed) {
		t.Fatalf("Write = %v, want the completion's error", err)
	}
	if objects.stored() {
		t.Error("the object outlived the abandoned write")
	}
	if want := "begin query commit begin query rollback begin query commit exec"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
	wantDone(t, rec)
}

type callerKey struct{}

func TestWriteOnComplete(t *testing.T) {
	ctx := context.WithValue(context.Background(), callerKey{}, "caller")
	s, db, rec := openStore(t, single, protocolRow(blobfs.StatusPending, 1), protocolRow(blobfs.StatusAvailable, 2), sqltest.Response{Affected: 1})
	objects := newObjectStore()
	var hooked blobfs.File
	hook := func(hctx context.Context, tx *sqlate.Tx, f blobfs.File) error {
		if hctx.Value(callerKey{}) != "caller" {
			t.Error("the hook ran under another context than the call's")
		}
		hooked = f
		_, err := tx.ExecContext(hctx, "INSERT INTO owner (file_id) VALUES ($1)", f.ID)
		return err
	}
	got, err := s.WriteFile(ctx, data.WriteFileRequest{DB: db, Objects: objects, Body: strings.NewReader("report"), Size: 6, Begin: create(ctx, s, allow), OnComplete: hook})
	if err != nil || got.Status != blobfs.StatusAvailable || got.Version != 2 {
		t.Fatalf("Write = %+v, %v, want the available row", got, err)
	}
	if hooked.ID != protocolFile || hooked.Status != blobfs.StatusAvailable || hooked.Version != 2 {
		t.Errorf("the hook received %+v, want the completed row", hooked)
	}
	if want := "begin query commit begin query exec commit"; ops(rec) != want {
		t.Errorf("ops = %q, want %q: the hook's exec between the completion and its commit", ops(rec), want)
	}
	wantDone(t, rec)
}

func TestWriteOnCompleteRefuses(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single, slices.Concat([]sqltest.Response{protocolRow(blobfs.StatusPending, 1)}, completedWithTheHooksExec(), abandoned())...)
	objects := newObjectStore()
	refused := errors.New("the consumer refuses")
	hook := func(ctx context.Context, tx *sqlate.Tx, f blobfs.File) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO owner (file_id) VALUES ($1)", f.ID); err != nil {
			return err
		}
		return refused
	}
	got, err := s.WriteFile(ctx, data.WriteFileRequest{DB: db, Objects: objects, Body: strings.NewReader("report"), Size: 6, Begin: create(ctx, s, allow), OnComplete: hook})
	if !errors.Is(err, refused) || !strings.HasPrefix(err.Error(), "data: write file "+protocolFile+": the hook: ") || got.ID != "" {
		t.Fatalf("Write = %+v, %v, want the hook's error, named", got, err)
	}
	if want := "begin query commit begin query exec rollback begin query commit exec"; ops(rec) != want {
		t.Errorf("ops = %q, want %q: the completion rolled back, then the abandon", ops(rec), want)
	}
	if del := rec.Calls()[8]; !strings.HasPrefix(del.SQL, "UPDATE blobfs_file") || !slices.Equal(del.Args, []any{protocolFile, int64(1)}) {
		t.Errorf("the delete is %q %v, want the pending row's, at its version", del.SQL, del.Args)
	}
	if objects.stored() || !slices.Equal(objects.deletes, []string{protocolKey}) {
		t.Errorf("deletes = %v, stored = %v, want the put object deleted with the row", objects.deletes, objects.stored())
	}
	wantDone(t, rec)
}

func found(ctx context.Context, s *data.Store) func(*sqlate.Tx) (blobfs.File, error) {
	return func(tx *sqlate.Tx) (blobfs.File, error) {
		f, _, err := s.Files.Ensure(ctx, tx, accepting{}, data.FileSpec{DirectoryID: protocolDir, Name: "report.txt", ContentType: "text/plain"}, data.CreateOptions{})
		return f, err
	}
}

func TestWriteRefusesARowNotPending(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single, protocolRow(blobfs.StatusAvailable, 2))
	objects := newObjectStore()
	_, err := s.WriteFile(ctx, data.WriteFileRequest{DB: db, Objects: objects, Body: strings.NewReader("report"), Size: 6, Begin: found(ctx, s)})
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
	_, err = s.WriteFile(ctx, data.WriteFileRequest{DB: db, Objects: objects, Body: strings.NewReader("report"), Size: 6, Begin: found(ctx, s)})
	wantDeleting(t, "Write of a deleting row", err, false, protocolFile)
	if !errors.As(err, &te) || te.From != blobfs.StatusDeleting {
		t.Errorf("Write of a deleting row = %v, want its TransitionError from deleting", err)
	}
	if objects.puts != 0 || len(objects.deletes) != 0 || ops(rec) != "begin query commit query" {
		t.Errorf("puts = %d, deletes = %v, ops = %q, want the directory's read alone past the first transaction", objects.puts, objects.deletes, ops(rec))
	}
	wantDone(t, rec)
}

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

func TestWriteCleansUpAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, db, rec := openStore(t, single, protocolRow(blobfs.StatusPending, 1), protocolRow(blobfs.StatusDeleting, 2), sqltest.Response{Affected: 1})
	objects := newObjectStore()
	objects.cancel = cancel
	if _, err := s.WriteFile(ctx, data.WriteFileRequest{DB: db, Objects: objects, Body: strings.NewReader("report"), Size: 6, Begin: create(ctx, s, allow)}); !errors.Is(err, context.Canceled) {
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
	if _, err := s.WriteFile(caller, data.WriteFileRequest{DB: db, Objects: objects, Body: strings.NewReader("report"), Size: 6, Begin: create(caller, s, allow)}); !errors.Is(err, blobfs.ErrDeleting) {
		t.Fatalf("Write = %v, want ErrDeleting, typed or not by the directory's read the hang-up may cut short", err)
	}
	if caller.Err() == nil || !slices.Equal(objects.deletes, []string{protocolKey}) || !slices.Equal(objects.deleteCtxs, []error{nil}) || objects.stored() {
		t.Errorf("caller %v, deletes = %v under %v, want the put object deleted under a live context after the hang-up", caller.Err(), objects.deletes, objects.deleteCtxs)
	}
	wantDone(t, rec)
}

func ensure(ctx context.Context, s *data.Store) func(*sqlate.Tx) (blobfs.File, data.WriteOutcome, error) {
	return func(tx *sqlate.Tx) (blobfs.File, data.WriteOutcome, error) {
		return s.Files.Ensure(ctx, tx, accepting{}, data.FileSpec{DirectoryID: protocolDir, Name: "report.txt", ContentType: "text/plain"}, data.CreateOptions{ID: protocolFile})
	}
}

func TestEnsureWrite(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single, noFile(), protocolRow(blobfs.StatusPending, 1), protocolRow(blobfs.StatusAvailable, 2))
	objects := newObjectStore()
	objects.objects[protocolKey] = "stale bytes"
	got, stored, err := s.EnsureFile(ctx, data.EnsureFileRequest{DB: db, Objects: objects, ID: protocolFile, Body: strings.NewReader("report"), Size: 6, Begin: ensure(ctx, s)})
	if err != nil || !stored || got.Status != blobfs.StatusAvailable {
		t.Fatalf("Ensure = %+v, %v, %v, want the file stored", got, stored, err)
	}
	if insert := rec.Calls()[2]; !strings.HasPrefix(insert.SQL, "INSERT INTO blobfs_file") || insert.Args[0] != protocolFile || insert.Args[2] != protocolKey {
		t.Errorf("the insert is %q %v, want the fixed id and its key", insert.SQL, insert.Args)
	}
	if objects.objects[protocolKey] != "report" {
		t.Errorf("object = %q, want the leftover replaced by the write's bytes", objects.objects[protocolKey])
	}
	if want := "begin query query commit begin query commit"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
	wantDone(t, rec)

	s, db, rec = openStore(t, single, protocolRow(blobfs.StatusPending, 1), protocolRow(blobfs.StatusAvailable, 2))
	objects = newObjectStore()
	if _, stored, err := s.EnsureFile(ctx, data.EnsureFileRequest{DB: db, Objects: objects, ID: protocolFile, Body: strings.NewReader("report"), Size: 6, Begin: ensure(ctx, s)}); err != nil || !stored || objects.puts != 1 {
		t.Fatalf("Ensure = %v, %v after %d puts, want the pending row put once and completed", stored, err, objects.puts)
	}
	if want := "begin query commit begin query commit"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
	wantDone(t, rec)
}

func TestEnsureWritePresent(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single, protocolRow(blobfs.StatusAvailable, 2))
	objects := newObjectStore()
	got, stored, err := s.EnsureFile(ctx, data.EnsureFileRequest{DB: db, Objects: objects, ID: protocolFile, Body: strings.NewReader("report"), Size: 6, Begin: ensure(ctx, s)})
	if err != nil || stored || got.ID != protocolFile || objects.puts != 0 || ops(rec) != "begin query commit" {
		t.Errorf("Ensure of an available row = %+v, %v, %v after %d puts and %q, want the row as it stands", got, stored, err, objects.puts, ops(rec))
	}

	s, db, rec = openStore(t, single, protocolRow(blobfs.StatusDeleting, 3), protocolDirectory())
	_, stored, err = s.EnsureFile(ctx, data.EnsureFileRequest{DB: db, Objects: objects, ID: protocolFile, Body: strings.NewReader("report"), Size: 6, Begin: ensure(ctx, s)})
	wantDeleting(t, "Ensure of a deleting row", err, false, protocolFile)
	if stored || objects.puts != 0 || ops(rec) != "begin query commit query" {
		t.Errorf("Ensure of a deleting row stored %v after %d puts and %q, want nothing put", stored, objects.puts, ops(rec))
	}
	wantDone(t, rec)
}

func TestEnsureWriteUnderAnotherID(t *testing.T) {
	const other = "00000000-0000-7000-8000-0000000000c1"
	for _, status := range []blobfs.Status{blobfs.StatusPending, blobfs.StatusAvailable} {
		t.Run(string(status), func(t *testing.T) {
			ctx := context.Background()
			s, db, rec := openStore(t, single, fileIn(other, protocolDir, "report.txt", status, 2))
			objects := newObjectStore()
			got, stored, err := s.EnsureFile(ctx, data.EnsureFileRequest{DB: db, Objects: objects, ID: protocolFile, Body: strings.NewReader("report"), Size: 6, Begin: ensure(ctx, s)})
			if !errors.Is(err, blobfs.ErrNameTaken) || stored || got.ID != "" {
				t.Fatalf("Ensure = %+v, %v, %v, want the other row reported as the taken name", got, stored, err)
			}
			if objects.puts != 0 || len(objects.deletes) != 0 || ops(rec) != "begin query commit" {
				t.Errorf("puts = %d, deletes = %v, ops = %q, want the first transaction alone", objects.puts, objects.deletes, ops(rec))
			}
		})
	}

	t.Run(string(blobfs.StatusDeleting), func(t *testing.T) {
		ctx := context.Background()
		s, db, rec := openStore(t, single, fileIn(other, protocolDir, "report.txt", blobfs.StatusDeleting, 2), protocolDirectory())
		objects := newObjectStore()
		got, stored, err := s.EnsureFile(ctx, data.EnsureFileRequest{DB: db, Objects: objects, ID: protocolFile, Body: strings.NewReader("report"), Size: 6, Begin: ensure(ctx, s)})
		if errors.Is(err, blobfs.ErrNameTaken) || stored || got.ID != "" {
			t.Errorf("Ensure = %+v, %v, %v, want the deleting row's refusal", got, stored, err)
		}
		wantDeleting(t, "Ensure over a deleting row under another id", err, false, other)
		if objects.puts != 0 || len(objects.deletes) != 0 || ops(rec) != "begin query commit query" {
			t.Errorf("puts = %d, deletes = %v, ops = %q, want the first transaction and the directory's read, not run again as a concurrent writer's", objects.puts, objects.deletes, ops(rec))
		}
		wantDone(t, rec)
	})

	t.Run("Created", func(t *testing.T) {
		ctx := context.Background()
		createdUnderAMintedID := fileIn(other, protocolDir, "report.txt", blobfs.StatusPending, 1)
		abandonedAtItsVersion := fileIn(other, protocolDir, "report.txt", blobfs.StatusDeleting, 2)
		s, db, rec := openStore(t, single, noFile(), createdUnderAMintedID, abandonedAtItsVersion, sqltest.Response{Affected: 1})
		objects := newObjectStore()
		minted := func(tx *sqlate.Tx) (blobfs.File, data.WriteOutcome, error) {
			return s.Files.Ensure(ctx, tx, accepting{}, data.FileSpec{DirectoryID: protocolDir, Name: "report.txt", ContentType: "text/plain"}, data.CreateOptions{})
		}
		got, stored, err := s.EnsureFile(ctx, data.EnsureFileRequest{DB: db, Objects: objects, ID: protocolFile, Body: strings.NewReader("report"), Size: 6, Begin: minted})
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

func TestEnsureWriteRaces(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single, slices.Concat(created(), foundCompletedByTheOtherWriter(), readBack())...)
	objects := newObjectStore()
	got, stored, err := s.EnsureFile(ctx, data.EnsureFileRequest{DB: db, Objects: objects, ID: protocolFile, Body: strings.NewReader("report"), Size: 6, Begin: ensure(ctx, s)})
	if err != nil || stored || got.Status != blobfs.StatusAvailable || got.Version != 2 {
		t.Fatalf("Ensure = %+v, %v, %v, want the other writer's row, found", got, stored, err)
	}
	if len(objects.deletes) != 0 || ops(rec) != "begin query query commit begin query query rollback query" {
		t.Errorf("deletes = %v, ops = %q, want no abandon", objects.deletes, ops(rec))
	}
	wantDone(t, rec)

	insertLost := []sqltest.Response{noFile(), violation(blobfs.ConstraintPrimaryKeyFile, sqlate.ErrUniqueViolation)}
	s, db, rec = openStore(t, single, slices.Concat(insertLost, resumed(), completed())...)
	if _, stored, err := s.EnsureFile(ctx, data.EnsureFileRequest{DB: db, Objects: newObjectStore(), ID: protocolFile, Body: strings.NewReader("report"), Size: 6, Begin: ensure(ctx, s)}); err != nil || !stored {
		t.Fatalf("Ensure = %v, %v, want the resumed row stored", stored, err)
	}
	if want := "begin query query rollback begin query commit begin query commit"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
	wantDone(t, rec)
}

func TestEnsureWriteFailedPut(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single,
		noFile(), protocolRow(blobfs.StatusPending, 1),
		protocolRow(blobfs.StatusDeleting, 2), sqltest.Response{Affected: 1},
	)
	objects := newObjectStore()
	objects.putErr = errors.New("put failed")
	if _, stored, err := s.EnsureFile(ctx, data.EnsureFileRequest{DB: db, Objects: objects, ID: protocolFile, Body: strings.NewReader("report"), Size: 6, Begin: ensure(ctx, s)}); !errors.Is(err, objects.putErr) || stored {
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

func TestEnsureWriteAbandonAfterTheOtherWriter(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single, slices.Concat(resumed(), foundCompletedByTheOtherWriter(), readBack())...)
	objects := newObjectStore()
	objects.putErr = errors.New("put failed")
	got, stored, err := s.EnsureFile(ctx, data.EnsureFileRequest{DB: db, Objects: objects, ID: protocolFile, Body: strings.NewReader("report"), Size: 6, Begin: ensure(ctx, s)})
	if err != nil || stored || got.Status != blobfs.StatusAvailable || got.Version != 2 {
		t.Fatalf("Ensure = %+v, %v, %v, want the other writer's row, found", got, stored, err)
	}
	if want := "begin query commit begin query query rollback query"; len(objects.deletes) != 0 || ops(rec) != want {
		t.Errorf("deletes = %v, ops = %q, want nothing removed and %q", objects.deletes, ops(rec), want)
	}
	wantDone(t, rec)
}

func TestEnsureWriteRefusedCompletion(t *testing.T) {
	for _, c := range []struct {
		name string
		read []sqltest.Response
		want error
		ops  string
	}{
		{"Deleting", []sqltest.Response{protocolRow(blobfs.StatusDeleting, 2), protocolDirectory()}, blobfs.ErrDeleting, "begin query commit begin query query query rollback"},
		{"Removed", []sqltest.Response{noFile()}, blobfs.ErrNotFound, "begin query commit begin query query rollback"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			s, db, rec := openStore(t, single, append([]sqltest.Response{protocolRow(blobfs.StatusPending, 1), noFile()}, c.read...)...)
			objects := newObjectStore()
			_, stored, err := s.EnsureFile(ctx, data.EnsureFileRequest{DB: db, Objects: objects, ID: protocolFile, Body: strings.NewReader("report"), Size: 6, Begin: ensure(ctx, s)})
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

func TestEnsureWriteOnComplete(t *testing.T) {
	ctx := context.WithValue(context.Background(), callerKey{}, "caller")
	s, db, rec := openStore(t, single, noFile(), protocolRow(blobfs.StatusPending, 1), protocolRow(blobfs.StatusAvailable, 2), sqltest.Response{Affected: 1})
	objects := newObjectStore()
	var hooked blobfs.File
	hook := func(hctx context.Context, tx *sqlate.Tx, f blobfs.File) error {
		if hctx.Value(callerKey{}) != "caller" {
			t.Error("the hook ran under another context than the call's")
		}
		hooked = f
		_, err := tx.ExecContext(hctx, "INSERT INTO owner (file_id) VALUES ($1)", f.ID)
		return err
	}
	got, stored, err := s.EnsureFile(ctx, data.EnsureFileRequest{DB: db, Objects: objects, ID: protocolFile, Body: strings.NewReader("report"), Size: 6, Begin: ensure(ctx, s), OnComplete: hook})
	if err != nil || !stored || got.Status != blobfs.StatusAvailable || got.Version != 2 {
		t.Fatalf("Ensure = %+v, %v, %v, want the file stored", got, stored, err)
	}
	if hooked.ID != protocolFile || hooked.Status != blobfs.StatusAvailable || hooked.Version != 2 {
		t.Errorf("the hook received %+v, want the completed row", hooked)
	}
	if want := "begin query query commit begin query exec commit"; ops(rec) != want {
		t.Errorf("ops = %q, want %q: the hook's exec between the completion and its commit", ops(rec), want)
	}
	wantDone(t, rec)
}

func TestEnsureWriteOnCompleteNotThisCall(t *testing.T) {
	for _, c := range []struct {
		name   string
		script []sqltest.Response
		ops    string
	}{
		{"Present", []sqltest.Response{protocolRow(blobfs.StatusAvailable, 2)}, "begin query commit"},
		{"Lost", slices.Concat(created(), foundCompletedByTheOtherWriter(), readBack()), "begin query query commit begin query query rollback query"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			s, db, rec := openStore(t, single, c.script...)
			ran := false
			hook := func(ctx context.Context, tx *sqlate.Tx, f blobfs.File) error {
				ran = true
				_, err := tx.ExecContext(ctx, "INSERT INTO owner (file_id) VALUES ($1)", f.ID)
				return err
			}
			got, stored, err := s.EnsureFile(ctx, data.EnsureFileRequest{DB: db, Objects: newObjectStore(), ID: protocolFile, Body: strings.NewReader("report"), Size: 6, Begin: ensure(ctx, s), OnComplete: hook})
			if err != nil || stored || got.Status != blobfs.StatusAvailable || got.Version != 2 {
				t.Fatalf("Ensure = %+v, %v, %v, want the available row, not stored", got, stored, err)
			}
			if ran || ops(rec) != c.ops {
				t.Errorf("the hook ran = %v, ops = %q, want no hook and %q", ran, ops(rec), c.ops)
			}
			wantDone(t, rec)
		})
	}
}

func TestEnsureWriteOnCompleteRefuses(t *testing.T) {
	for _, c := range []struct {
		name    string
		refused error
	}{
		{"Plain", errors.New("the consumer refuses")},
		{"WrappingVersionMismatch", fmt.Errorf("the consumer's row moved: %w", query.ErrVersionMismatch)},
		{"WrappingInvalidTransition", fmt.Errorf("the consumer's state: %w", blobfs.ErrInvalidTransition)},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			s, db, rec := openStore(t, single, slices.Concat(resumed(), completedWithTheHooksExec(), abandoned())...)
			objects := newObjectStore()
			hook := func(ctx context.Context, tx *sqlate.Tx, f blobfs.File) error {
				if _, err := tx.ExecContext(ctx, "INSERT INTO owner (file_id) VALUES ($1)", f.ID); err != nil {
					return err
				}
				return c.refused
			}
			got, stored, err := s.EnsureFile(ctx, data.EnsureFileRequest{DB: db, Objects: objects, ID: protocolFile, Body: strings.NewReader("report"), Size: 6, Begin: ensure(ctx, s), OnComplete: hook})
			if !errors.Is(err, c.refused) || !strings.HasPrefix(err.Error(), "data: ensure file "+protocolFile+": the hook: ") || stored || got.ID != "" {
				t.Fatalf("Ensure = %+v, %v, %v, want the hook's error, named", got, stored, err)
			}
			if errors.Is(err, blobfs.ErrNotFound) {
				t.Errorf("Ensure = %v, want no read back's refusal beside the hook's", err)
			}
			if want := "begin query commit begin query exec rollback begin query commit exec"; ops(rec) != want {
				t.Errorf("ops = %q, want %q: the completion rolled back, then the abandon, with no read back to take the hook's error for a lost race", ops(rec), want)
			}
			if del := rec.Calls()[8]; !strings.HasPrefix(del.SQL, "UPDATE blobfs_file") || !slices.Equal(del.Args, []any{protocolFile, int64(1)}) {
				t.Errorf("the delete is %q %v, want the pending row's, at its version", del.SQL, del.Args)
			}
			if objects.stored() || !slices.Equal(objects.deletes, []string{protocolKey}) {
				t.Errorf("deletes = %v, stored = %v, want the put object deleted with the row", objects.deletes, objects.stored())
			}
			wantDone(t, rec)
		})
	}
}

func TestEnsureWriteOnCompleteRefusesAMovedRow(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single, slices.Concat(resumed(), completedWithTheHooksExec(), foundCompletedByTheOtherWriter())...)
	refused := errors.New("the consumer refuses")
	hook := func(ctx context.Context, tx *sqlate.Tx, f blobfs.File) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO owner (file_id) VALUES ($1)", f.ID); err != nil {
			return err
		}
		return refused
	}
	got, stored, err := s.EnsureFile(ctx, data.EnsureFileRequest{DB: db, Objects: newObjectStore(), ID: protocolFile, Body: strings.NewReader("report"), Size: 6, Begin: ensure(ctx, s), OnComplete: hook})
	if !errors.Is(err, refused) || !errors.Is(err, query.ErrVersionMismatch) || stored || got.ID != "" {
		t.Fatalf("Ensure = %+v, %v, %v, want the hook's error and the abandon's", got, stored, err)
	}
	if want := "begin query commit begin query exec rollback begin query query rollback"; ops(rec) != want {
		t.Errorf("ops = %q, want %q: no read back after the abandon to take the hook's failure for a lost race", ops(rec), want)
	}
	wantDone(t, rec)
}

func TestEnsureWriteRefusesAnID(t *testing.T) {
	for _, id := range []string{"seed", ""} {
		t.Run(fmt.Sprintf("%q", id), func(t *testing.T) {
			ctx := context.Background()
			s, db, rec := openStore(t, single)
			if _, _, err := s.EnsureFile(ctx, data.EnsureFileRequest{DB: db, Objects: newObjectStore(), ID: id, Body: strings.NewReader("report"), Size: 6, Begin: ensure(ctx, s)}); !errors.Is(err, blobfs.ErrInvalidID) {
				t.Errorf("Ensure under the id %q = %v, want ErrInvalidID", id, err)
			}
			if len(rec.Calls()) != 0 {
				t.Errorf("the refused id ran %q", ops(rec))
			}
		})
	}
}

func TestWriteRequestsPanic(t *testing.T) {
	ctx := context.Background()
	type call struct {
		begun   *int
		objects *objectStore
		run     func()
	}
	write := func(s *data.Store, db *sqlate.DB, field string) call {
		c := call{begun: new(int), objects: newObjectStore()}
		req := data.WriteFileRequest{DB: db, Objects: c.objects, Body: strings.NewReader("report"), Size: 6, Begin: func(tx *sqlate.Tx) (blobfs.File, error) {
			*c.begun++
			return create(ctx, s, allow)(tx)
		}}
		switch field {
		case "DB":
			req.DB = nil
		case "Objects":
			req.Objects = nil
		case "Body":
			req.Body = nil
		case "Begin":
			req.Begin = nil
		}
		c.run = func() { _, _ = s.WriteFile(ctx, req) }
		return c
	}
	ensureCall := func(s *data.Store, db *sqlate.DB, field string) call {
		c := call{begun: new(int), objects: newObjectStore()}
		req := data.EnsureFileRequest{DB: db, Objects: c.objects, ID: protocolFile, Body: strings.NewReader("report"), Size: 6, Begin: func(tx *sqlate.Tx) (blobfs.File, data.WriteOutcome, error) {
			*c.begun++
			return ensure(ctx, s)(tx)
		}}
		switch field {
		case "DB":
			req.DB = nil
		case "Objects":
			req.Objects = nil
		case "Body":
			req.Body = nil
		case "Begin":
			req.Begin = nil
		}
		c.run = func() { _, _, _ = s.EnsureFile(ctx, req) }
		return c
	}
	for _, c := range []struct {
		request string
		field   string
		call    func(*data.Store, *sqlate.DB, string) call
	}{
		{"WriteFileRequest", "DB", write},
		{"WriteFileRequest", "Objects", write},
		{"WriteFileRequest", "Body", write},
		{"WriteFileRequest", "Begin", write},
		{"EnsureFileRequest", "DB", ensureCall},
		{"EnsureFileRequest", "Objects", ensureCall},
		{"EnsureFileRequest", "Body", ensureCall},
		{"EnsureFileRequest", "Begin", ensureCall},
	} {
		t.Run(c.request+"/"+c.field, func(t *testing.T) {
			s, db, rec := openStore(t, single)
			call := c.call(s, db, c.field)
			want := "data: " + c.request + "." + c.field + " is nil"
			func() {
				defer func() {
					if got := recover(); got != want {
						t.Errorf("the call panicked with %v, want %q", got, want)
					}
				}()
				call.run()
			}()
			if len(rec.Calls()) != 0 || *call.begun != 0 || call.objects.puts != 0 || len(call.objects.deletes) != 0 {
				t.Errorf("the refused request ran %q, began %d times, put %d times, and deleted %v, want no I/O", ops(rec), *call.begun, call.objects.puts, call.objects.deletes)
			}
		})
	}
}

type absentBody struct{}

func (*absentBody) Read([]byte) (int, error) { return 0, io.EOF }

func TestWriteRequestsTypedNil(t *testing.T) {
	ctx := context.Background()
	errBegin := errors.New("the caller's scope refuses")
	refuse := func(*sqlate.Tx) error { return errBegin }
	refuseEnsure := func(*sqlate.Tx) (blobfs.File, data.WriteOutcome, error) {
		return blobfs.File{}, data.WriteCreated, errBegin
	}
	var objects *objectStore
	var body *absentBody
	s, db, rec := openStore(t, single)
	if _, err := s.WriteFile(ctx, data.WriteFileRequest{DB: db, Objects: objects, Body: body, Begin: create(ctx, s, refuse)}); !errors.Is(err, errBegin) {
		t.Errorf("WriteFile with typed-nil Objects and Body = %v, want begin's refusal", err)
	}
	if _, _, err := s.EnsureFile(ctx, data.EnsureFileRequest{DB: db, Objects: objects, ID: protocolFile, Body: body, Begin: refuseEnsure}); !errors.Is(err, errBegin) {
		t.Errorf("EnsureFile with typed-nil Objects and Body = %v, want begin's refusal", err)
	}
	if want := "begin rollback begin rollback"; ops(rec) != want {
		t.Errorf("ops = %q, want %q: each begin's transaction", ops(rec), want)
	}
}

func TestRemove(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single, sqltest.Response{Affected: 1}, protocolRow(blobfs.StatusDeleting, 3), sqltest.Response{Affected: 1})
	objects := newObjectStore()
	objects.objects[protocolKey] = "report"
	err := s.RemoveFile(ctx, db, objects, func(tx *sqlate.Tx) (string, error) {
		_, err := tx.ExecContext(ctx, "DELETE FROM owner WHERE file_id = $1", protocolFile)
		return protocolFile, err
	}, data.RemoveOptions{Version: 2})
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
	if err := s.RemoveFile(ctx, db, objects, func(*sqlate.Tx) (string, error) { return "", refused }, data.RemoveOptions{}); !errors.Is(err, refused) || strings.Contains(err.Error(), "data: ") {
		t.Fatalf("Remove = %v, want pick's error as it came", err)
	}
	if ops(rec) != "begin rollback" || len(objects.deletes) != 1 {
		t.Errorf("ops = %q, deletes = %v, want nothing deleted", ops(rec), objects.deletes)
	}

	s, db, _ = openStore(t, single, unchangedFile(single, fileIn(protocolFile, protocolDir, "report.txt", blobfs.StatusAvailable, 3))...)
	err = s.RemoveFile(ctx, db, objects, func(*sqlate.Tx) (string, error) { return protocolFile, nil }, data.RemoveOptions{Version: 2})
	if !errors.Is(err, query.ErrVersionMismatch) || !strings.HasPrefix(err.Error(), "data: remove file "+protocolFile+": ") {
		t.Errorf("Remove at a stale version = %v, want ErrVersionMismatch, named", err)
	}
}

func TestRemoveOnDelete(t *testing.T) {
	ctx := context.WithValue(context.Background(), callerKey{}, "caller")
	s, db, rec := openStore(t, single, protocolRow(blobfs.StatusDeleting, 3), sqltest.Response{Affected: 1}, sqltest.Response{Affected: 1})
	objects := newObjectStore()
	objects.objects[protocolKey] = "report"
	var hooked blobfs.File
	hook := func(hctx context.Context, tx *sqlate.Tx, f blobfs.File) error {
		if hctx.Value(callerKey{}) != "caller" {
			t.Error("the hook ran under another context than the call's")
		}
		hooked = f
		_, err := tx.ExecContext(hctx, "DELETE FROM owner WHERE file_id = $1", f.ID)
		return err
	}
	err := s.RemoveFile(ctx, db, objects, func(*sqlate.Tx) (string, error) { return protocolFile, nil }, data.RemoveOptions{Version: 2, OnDelete: hook})
	if err != nil {
		t.Fatalf("Remove = %v", err)
	}
	if hooked.ID != protocolFile || hooked.Status != blobfs.StatusDeleting || hooked.Version != 3 {
		t.Errorf("the hook received %+v, want the row marked deleting", hooked)
	}
	if want := "begin query exec commit exec"; ops(rec) != want {
		t.Errorf("ops = %q, want %q: the hook's exec between the delete and its commit", ops(rec), want)
	}
	if del := rec.Calls()[1]; !slices.Equal(del.Args, []any{protocolFile, int64(2)}) {
		t.Errorf("the delete bound %v, want the file at the guarded version", del.Args)
	}
	if objects.stored() {
		t.Error("the object outlived its remove")
	}
	wantDone(t, rec)
}

func TestRemoveOnDeleteRetried(t *testing.T) {
	ctx := context.Background()
	responses := append(unchangedFile(single, protocolRow(blobfs.StatusDeleting, 3)), sqltest.Response{Affected: 1})
	s, db, rec := openStore(t, single, responses...)
	objects := newObjectStore()
	objects.objects[protocolKey] = "report"
	ran := 0
	hook := func(context.Context, *sqlate.Tx, blobfs.File) error {
		ran++
		return nil
	}
	if err := s.RemoveFile(ctx, db, objects, func(*sqlate.Tx) (string, error) { return protocolFile, nil }, data.RemoveOptions{OnDelete: hook}); err != nil {
		t.Fatalf("Remove retried = %v", err)
	}
	if ran != 0 {
		t.Errorf("the retry ran the hook %d times, want none", ran)
	}
	if want := "begin query query commit exec"; ops(rec) != want {
		t.Errorf("ops = %q, want %q", ops(rec), want)
	}
	if objects.stored() || !slices.Equal(objects.deletes, []string{protocolKey}) {
		t.Errorf("deletes = %v, stored = %v, want the object deleted", objects.deletes, objects.stored())
	}
	wantDone(t, rec)
}

func TestRemoveOnDeleteRefuses(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single, protocolRow(blobfs.StatusDeleting, 3), sqltest.Response{Affected: 1})
	objects := newObjectStore()
	objects.objects[protocolKey] = "report"
	refused := errors.New("the consumer refuses")
	hook := func(ctx context.Context, tx *sqlate.Tx, f blobfs.File) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM owner WHERE file_id = $1", f.ID); err != nil {
			return err
		}
		return refused
	}
	err := s.RemoveFile(ctx, db, objects, func(*sqlate.Tx) (string, error) { return protocolFile, nil }, data.RemoveOptions{OnDelete: hook})
	if !errors.Is(err, refused) || !strings.HasPrefix(err.Error(), "data: remove file "+protocolFile+": the hook: ") {
		t.Fatalf("Remove = %v, want the hook's error, named", err)
	}
	if want := "begin query exec rollback"; ops(rec) != want {
		t.Errorf("ops = %q, want %q: the delete rolled back, nothing purged", ops(rec), want)
	}
	if !objects.stored() || len(objects.deletes) != 0 {
		t.Errorf("deletes = %v, stored = %v, want the object in place", objects.deletes, objects.stored())
	}
	wantDone(t, rec)
}

func TestRemoveFileID(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single, protocolRow(blobfs.StatusDeleting, 3), sqltest.Response{Affected: 1}, noFile(), noFile())
	objects := newObjectStore()
	objects.objects[protocolKey] = "report"
	if err := s.RemoveFileID(ctx, db, objects, protocolFile, data.VersionOptions{Version: 2}); err != nil {
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
	err := s.RemoveFileID(ctx, db, objects, protocolFile, data.VersionOptions{})
	if !errors.Is(err, blobfs.ErrNotFound) || !strings.HasPrefix(err.Error(), "data: remove file "+protocolFile+": ") {
		t.Errorf("RemoveFileID retried after it finished = %v, want ErrNotFound, named", err)
	}
	wantDone(t, rec)
}

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
