# Quick start

A tutorial that builds a working program from an empty directory: a document store that keeps
its tree in PostgreSQL through blobfs and its bytes in Azure Blob Storage through `go-storage`,
emulated locally by Azurite. The program:

- adopts blobfs's migration set beneath a migration set of its own
- builds one pattern catalog and the blobfs store over the PostgreSQL engine
- verifies every statement against the migrated schema
- writes a file, lists its directory, and deletes it through blobfs's protocols, keeping a row
  of its own beside the file's
- tests its store without a database, and runs blobfs's conformance suite against a live one

Every file is written in full at the step that needs it, and every block is taken from a program
that compiles, runs against PostgreSQL 18 and Azurite, and passes its tests. The
[concepts](concepts.md) document explains the protocols the program runs, and the
[features](features.md) document states each rule.

## Prerequisites

- [mise](https://mise.jdx.dev/getting-started.html) installs the Go toolchain the project pins
  and runs its tasks.
- [Docker](https://docs.docker.com/get-started/get-docker/) with the Compose plugin runs
  PostgreSQL and Azurite.

## 1. Create the module

```sh
mkdir docstore && cd docstore
go mod init example.com/docstore
go get github.com/standards-lab/blobfs github.com/standards-lab/blobfs/postgres
go get github.com/standards-lab/sqlate github.com/standards-lab/sqlate/postgres
go get github.com/standards-lab/go-storage github.com/standards-lab/go-storage/azureblob
go get github.com/jackc/pgx/v5
go get -tool github.com/standards-lab/sqlate/sqlint/cmd/sqlint
```

`blobfs` is the base module: the root package, the persistence package `data`, and its
conformance suite `data/datatest`, over `sqlate` and `golang.org/x/text` alone. `blobfs/postgres`
is the PostgreSQL engine and its migration set. blobfs names no driver
and no object store; the program brings pgx, sqlate's PostgreSQL dialect, and `go-storage`
itself.

`mise.toml` pins the toolchain, sets the settings the program reads, and names the tasks the
tutorial runs. `go-storage` reads the `APP_STORAGE_*` variables for the prefix `app`; the
account and key are Azurite's published development credentials.

`mise.toml`:

```toml
[tools]
go = "1.27"

[env]
APP_DSN = "postgres://app:app@127.0.0.1:5433/app?sslmode=disable"
# Azurite's published development account; not a secret.
APP_STORAGE_ENDPOINT = "http://127.0.0.1:10000/devstoreaccount1"
APP_STORAGE_ACCOUNT = "devstoreaccount1"
APP_STORAGE_KEY = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="
APP_STORAGE_CONTAINER = "documents"

[tasks.up]
description = "Build and start PostgreSQL and Azurite and wait until they are healthy"
run = "docker compose up -d --wait --build"

[tasks.down]
description = "Stop the stack and drop its volumes"
run = "docker compose down -v"

[tasks.run]
description = "Run the program"
run = "go run ."

[tasks.test]
description = "Run the unit tests; no database needed"
run = "go test ./..."

[tasks.integration]
description = "Run the conformance suite against the stack's PostgreSQL"
run = "go test -tags integration -count=1 ./..."

[tasks.lint]
description = "Check the SQL files against the conventions"
run = "go tool sqlint ."
```

```sh
mise trust && mise install
```

## 2. Start PostgreSQL and Azurite

Each service builds from a Dockerfile of its own, whose `FROM` line is the service's one image
pin and which carries its configuration and its health check, so `compose.yml` adds only the
build context and the port.

`compose.yml`:

```yaml
services:
  postgres:
    build:
      context: compose/postgres
    ports:
      - "127.0.0.1:5433:5432"
  azurite:
    build:
      context: compose/azurite
    ports:
      - "127.0.0.1:10000:10000"
```

`compose/postgres/Dockerfile`:

```dockerfile
FROM postgres:18-alpine

ENV POSTGRES_USER=app \
    POSTGRES_PASSWORD=app \
    POSTGRES_DB=app

HEALTHCHECK --start-period=30s --start-interval=1s --interval=5s --timeout=3s --retries=5 \
    CMD ["pg_isready", "-U", "app", "-d", "app"]
```

`compose/azurite/Dockerfile`:

```dockerfile
FROM mcr.microsoft.com/azure-storage/azurite:3.37.0

# The image's busybox nc shows the blob listener accepts connections.
HEALTHCHECK --start-period=30s --start-interval=1s --interval=5s --timeout=3s --retries=5 \
    CMD ["nc", "-z", "127.0.0.1", "10000"]

# The Azure SDK sends an x-ms-version that Azurite rejects unless the version check is off.
CMD ["azurite-blob", "--blobHost", "0.0.0.0", "--skipApiVersionCheck"]
```

```sh
mise run up
```

## 3. Write the program's migration

The program keeps a note about each file in a table of its own, whose foreign key references
blobfs's `blobfs_file`. Its migrations are a set of its own, declared above blobfs's in the next
step, so blobfs's tables exist before this one references them. The table's objects take the
program's prefix, `app_`, since `blobfs_` belongs to blobfs's set, and its foreign key has no
cascading action.

`migrations/0001_attachment.up.sql`:

```sql
-- The program's own record of a file: a note attached to it. The foreign key
-- references blobfs's table, which blobfs's set has created by the time this
-- set runs, and it has no cascading action.
CREATE TABLE app_attachment (
  file_id uuid NOT NULL,
  note text NOT NULL,
  CONSTRAINT app_pk_attachment PRIMARY KEY (file_id),
  CONSTRAINT app_fk_attachment_file FOREIGN KEY (file_id) REFERENCES blobfs_file (id)
);
```

`migrations/0001_attachment.down.sql`:

```sql
DROP TABLE app_attachment;
```

## 4. Write the database layer

The database layer opens the pool, wraps it with sqlate's PostgreSQL dialect, and runs one
migrator over two sets, declared bottom first: blobfs's, from `postgres.Migrations()`, then the
program's. Each set records its history in its own table, `blobfs_schema_version` and the
default `schema_version`, and `Up` applies every migration of blobfs's set before any of the
program's. Two processes that start at once serialize on the migrator's lock.

`database.go`:

```go
package main

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/migrate"
	sqlatepg "github.com/standards-lab/sqlate/postgres"

	blobfspg "github.com/standards-lab/blobfs/postgres"
)

//go:embed migrations/*.sql
var migrations embed.FS

// openDatabase opens the pool, wraps it with the PostgreSQL dialect, and
// brings both migration sets to their heads: blobfs's first, then the
// program's own, which references blobfs's tables.
func openDatabase(ctx context.Context, dsn string) (*sqlate.DB, error) {
	pool, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db := sqlate.Wrap(pool, sqlatepg.Dialect{})

	blobfsSet, err := blobfspg.Migrations()
	if err != nil {
		return nil, err
	}
	own, err := migrate.Files(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	sets := []migrate.Set{blobfsSet, {Name: "app", Migrations: own}}
	m, err := migrate.New(db, sets, migrate.Options{})
	if err != nil {
		return nil, err
	}
	if err := m.Up(ctx); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}
```

## 5. Write the object-store adapter

blobfs asks three things of the object store: a key check, a put, and a delete. It asks them
through one interface, `data.ObjectStore`, which the store's protocols call (see [the
protocols](features.md#the-protocols)). The adapter wires the three to `go-storage`'s own
methods and is the only place the two libraries meet. `openObjects` builds the Azure Blob
store from the environment and starts it, which ensures the container exists.

`objects.go`:

```go
package main

import (
	"context"
	"fmt"
	"io"

	"github.com/standards-lab/go-storage"
	"github.com/standards-lab/go-storage/azureblob"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data"
)

// objectStore is the adapter: it implements data.ObjectStore over the
// started store's key rule, put, and delete. It is the only place the two
// libraries meet.
type objectStore struct{ store *storage.Store }

var _ data.ObjectStore = objectStore{}

func (o objectStore) ValidateKey(key string) error {
	return o.store.Capabilities().ValidateKey(key)
}

// PutObject stores body under key and reports it in the content type the
// caller declared, since the store's report may differ.
func (o objectStore) PutObject(ctx context.Context, key string, body io.Reader, contentType string, size int64) (blobfs.Object, error) {
	obj, err := o.store.Put(ctx, key, body, storage.PutOptions{ContentType: contentType, Size: size})
	if err != nil {
		return blobfs.Object{}, err
	}
	return blobfs.Object{Size: obj.Size, ContentType: contentType, ETag: obj.ETag}, nil
}

// DeleteObject removes the object under key; a missing object is success.
func (o objectStore) DeleteObject(ctx context.Context, key string) error {
	return o.store.Delete(ctx, key)
}

// openObjects builds the Azure Blob store from the APP_STORAGE_* settings
// and starts it, which ensures the container and probes it.
func openObjects(ctx context.Context) (*storage.Store, error) {
	var cfg storage.Config
	if err := cfg.Finalize("app"); err != nil {
		return nil, fmt.Errorf("storage configuration: %w", err)
	}
	client, err := azureblob.New(cfg)
	if err != nil {
		return nil, err
	}
	objects := storage.New(client, cfg)
	if err := objects.Start(ctx); err != nil {
		return nil, fmt.Errorf("start storage: %w", err)
	}
	return objects, nil
}
```

## 6. Write the statements and the stores

The program's own statements are two commands over its table. They compile against the same
catalog as blobfs's.

`statements/attach.sql`:

```sql
--| tier: standard
-- Records the program's note about an available file, in the transaction
-- that holds the file's row.
INSERT INTO app_attachment (file_id, note)
VALUES ({{file_id:uuid}}, {{note}})
```

`statements/detach.sql`:

```sql
--| tier: standard
-- Removes the program's note about a file, in the transaction that begins
-- the file's delete, ahead of the purge its foreign key would refuse.
DELETE FROM app_attachment
WHERE file_id = {{file_id:uuid}}
```

The stores layer builds the program's one catalog: sqlate's patterns with the PostgreSQL overlay
of the cursor predicate, `sqlatepg.Patterns()`, and blobfs's published patterns,
`data.Patterns()`. `data.New` compiles blobfs's statements against it and installs the
PostgreSQL engine with `data.WithEngine`; the program's own statements compile against the same
catalog. `Verify` prepares both against the migrated schema.

`Upload` runs `Store.WriteFile` and `Delete` runs `Store.RemoveFile`, each with a callback of the
program's inside the protocol's first transaction; [the protocols](features.md#the-protocols)
states what each runs and refuses. Both callbacks first check the program's scope: the file's
directory must lie within the area the caller may reach, which `Directories.IsWithin` answers.

- `Upload`'s `begin` inserts the pending row with `Files.Create` and nothing that references it,
  since a write that fails and cannot clean up leaves a stale row the sweep must be free to
  purge. `Upload` attaches the note once the file is available, in a transaction that first
  holds the file's row, as the [reference-then-delete](concepts.md#reference-then-delete) rule
  requires, so a delete that begins meanwhile waits for the note to commit.
- `Delete`'s `pick` reads the file, checks its scope, removes the note, and returns the file's
  id, so the purge finds no note to refuse it; the program's foreign key stays the backstop.

`stores.go`:

```go
package main

import (
	"context"
	"embed"
	"errors"
	"io"

	"github.com/standards-lab/sqlate"
	sqlatepg "github.com/standards-lab/sqlate/postgres"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data"
	blobfspg "github.com/standards-lab/blobfs/postgres"
)

//go:embed statements/*.sql
var statements embed.FS

// errOutOfScope refuses a directory outside the caller's area.
var errOutOfScope = errors.New("outside the caller's area")

// Stores is the program's persistence: blobfs's store and the program's
// own statements, compiled against one catalog.
type Stores struct {
	Blobfs *data.Store
	stmts  *query.Statements
	attach query.Statement
	detach query.Statement
}

// newStores builds the one catalog, the library's patterns with the
// PostgreSQL overlay and blobfs's, and compiles both sets of statements
// against it for the session's dialect.
func newStores(dialect sqlate.Dialect) (*Stores, error) {
	catalog, err := query.NewCatalog(sqlatepg.Patterns(), data.Patterns())
	if err != nil {
		return nil, err
	}
	store, err := data.New(catalog, dialect, data.WithEngine(blobfspg.Engine))
	if err != nil {
		return nil, err
	}
	stmts, err := catalog.Compile(statements, "statements", dialect)
	if err != nil {
		return nil, err
	}
	return &Stores{
		Blobfs: store,
		stmts:  stmts,
		attach: stmts.Statement("attach"),
		detach: stmts.Statement("detach"),
	}, nil
}

// Verify prepares every statement, blobfs's and the program's, against the
// migrated schema.
func (s *Stores) Verify(ctx context.Context, sess sqlate.Session) error {
	if err := s.Blobfs.Verify(ctx, sess); err != nil {
		return err
	}
	return query.Verify(ctx, sess, s.stmts)
}

// Upload writes size bytes of body as the file name in dirID, which must
// lie within areaID, and attaches the program's note to it once it is
// available.
func (s *Stores) Upload(ctx context.Context, db *sqlate.DB, objects data.ObjectStore, areaID, dirID, name, contentType, note string, body io.Reader, size int64) (blobfs.File, error) {
	file, err := s.Blobfs.WriteFile(ctx, db, objects, body, size, func(tx *sqlate.Tx) (blobfs.File, error) {
		if err := s.within(ctx, tx, dirID, areaID); err != nil {
			return blobfs.File{}, err
		}
		// The pending row, and no row that references it.
		return s.Blobfs.Files.Create(ctx, tx, objects, dirID, name, contentType)
	})
	if err != nil {
		return blobfs.File{}, err
	}
	_, err = sqlate.Transact(ctx, db, func(tx *sqlate.Tx) (int64, error) {
		if err := s.Blobfs.Files.Hold(ctx, tx, file.ID); err != nil {
			return 0, err
		}
		return s.attach.Exec(ctx, tx, query.Args{"file_id": file.ID, "note": note})
	})
	if err != nil {
		// The attach rolled back, so nothing references the file: remove it
		// rather than leave it stored with no note.
		return blobfs.File{}, errors.Join(err, s.Blobfs.RemoveFileID(context.WithoutCancel(ctx), db, objects, file.ID))
	}
	return file, nil
}

// Delete removes the file with id, which must lie within areaID: its note,
// then the row marked deleting, then its object, then its row.
func (s *Stores) Delete(ctx context.Context, db *sqlate.DB, objects data.ObjectDeleter, areaID, id string) error {
	return s.Blobfs.RemoveFile(ctx, db, objects, func(tx *sqlate.Tx) (string, error) {
		file, err := s.Blobfs.Files.Find(ctx, tx, id)
		if err != nil {
			return "", err
		}
		if err := s.within(ctx, tx, file.DirectoryID, areaID); err != nil {
			return "", err
		}
		_, err = s.detach.Exec(ctx, tx, query.Args{"file_id": id})
		return id, err
	})
}

// within is the program's scope check: the directory dirID must lie
// within areaID.
func (s *Stores) within(ctx context.Context, tx *sqlate.Tx, dirID, areaID string) error {
	ok, err := s.Blobfs.Directories.IsWithin(ctx, tx, dirID, areaID)
	if err != nil {
		return err
	}
	if !ok {
		return errOutOfScope
	}
	return nil
}
```

## 7. Write the program

`main.go` runs the sequence: connect and migrate, start the store, compile and verify, then one
file's whole life.

- `Directories.Ensure` returns the `reports` directory under the root, creating it on the first
  run only. The program passes it as both the caller's area and the file's directory.
- The write is `Upload`: the pending row, the put under the row's `Key`, the row completed
  available, then the note.
- The listing resolves the directory by path from the root and reads one page of its files,
  newest first, with the total counted in the page's statement.
- The delete is `Delete`: the note removed and the row marked deleting, then the object delete,
  then the purge.

`main.go`:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	db, err := openDatabase(ctx, os.Getenv("APP_DSN"))
	if err != nil {
		return err
	}
	objects, err := openObjects(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = objects.Shutdown(context.Background()) }()
	objs := objectStore{objects}

	stores, err := newStores(db.Dialect())
	if err != nil {
		return err
	}
	if err := stores.Verify(ctx, db); err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	store := stores.Blobfs

	// A directory under the root, found on every run after the first.
	dir, created, err := store.Directories.Ensure(ctx, db, blobfs.RootID, "reports")
	if err != nil {
		return err
	}
	fmt.Printf("directory reports: id %s, created %t\n", dir.ID, created)

	// The write: the pending row, the object, the row available, the note.
	body := "quarterly numbers\n"
	file, err := stores.Upload(ctx, db, objs, dir.ID, dir.ID, "Q3 summary.txt", "text/plain",
		"for the board", strings.NewReader(body), int64(len(body)))
	if err != nil {
		return err
	}
	fmt.Printf("%s under the key %s: %s, %d bytes\n", file.Name, file.Key, file.Status, *file.Size)

	// A listing: one page of the directory's files, newest first.
	found, err := store.Directories.FindByPath(ctx, db, blobfs.RootID, "reports")
	if err != nil {
		return err
	}
	page, err := store.Files.List(ctx, db, found.ID,
		query.Directives{Sort: []query.Sort{{Field: "created_at", Descending: true}}},
		query.Page{Number: 1, Size: 20})
	if err != nil {
		return err
	}
	path, err := store.Directories.Path(ctx, db, found.ID)
	if err != nil {
		return err
	}
	fmt.Printf("%s: %d of %d files\n", path, len(page.Items), page.Total)
	for _, f := range page.Items {
		fmt.Printf("  %s (%s)\n", f.Name, f.Status)
	}

	// The delete: the note and the row marked deleting, the object, the row.
	if err := stores.Delete(ctx, db, objs, dir.ID, file.ID); err != nil {
		return err
	}
	fmt.Printf("deleted %s\n", file.Name)
	return nil
}
```

```sh
mise run run
```

```
directory reports: id 01a0eda3-d1ae-78cd-8b78-b6b69b343724, created true
Q3 summary.txt under the key 01a0eda3-d1b1-79c4-b56a-7bcbf2cc34e7/Q3 summary.txt: available, 18 bytes
/reports: 1 of 1 files
  Q3 summary.txt (available)
deleted Q3 summary.txt
```

The ids differ on every run. A second run reports `created false` and the same directory id, and
writes the file under a new key, since each write mints a new id.

## 8. Test without a database

A unit test compiles the stores for `sqltest.ReturningDialect`, `sqltest`'s stub dialect with
`RETURNING`, and runs them over `sqltest`'s scripted driver, so it needs no database and each of
blobfs's returning commands is one scripted read. `datatest.FileRows` scripts a read of file
rows in the columns blobfs scans, and a map stands in for the object store. The stores run the
PostgreSQL engine, so the script follows its statements: its hold is a locking read. The first
test scripts the scope check, the pending row, its completion, the hold, and the note, and
proves the pending row committed before the put. The second proves that a directory outside the
area rolls the write back before any byte is stored.

`stores_test.go`:

```go
package main

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/sqltest"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data/datatest"
)

// memory stands in for the object store: it accepts every key, keeps its
// objects in a map, and records the driver's operations at each put.
type memory struct {
	rec     *sqltest.Recorder
	objects map[string]string
	atPut   []sqltest.Op
}

func (*memory) ValidateKey(string) error { return nil }

func (m *memory) PutObject(_ context.Context, key string, body io.Reader, contentType string, size int64) (blobfs.Object, error) {
	m.atPut = m.rec.Ops()
	b, err := io.ReadAll(body)
	if err != nil {
		return blobfs.Object{}, err
	}
	m.objects[key] = string(b)
	return blobfs.Object{Size: size, ContentType: contentType, ETag: `"etag"`}, nil
}

func (m *memory) DeleteObject(_ context.Context, key string) error {
	delete(m.objects, key)
	return nil
}

// open compiles the stores for the stub dialect with RETURNING and opens a
// scripted pool over the responses, with an empty object store.
func open(t *testing.T, responses ...sqltest.Response) (*Stores, *sqlate.DB, *sqltest.Recorder, *memory) {
	t.Helper()
	dialect := sqltest.ReturningDialect{}
	pool, rec := sqltest.Open(t, responses...)
	stores, err := newStores(dialect)
	if err != nil {
		t.Fatalf("newStores: %v", err)
	}
	return stores, sqlate.Wrap(pool, dialect), rec, &memory{rec: rec, objects: map[string]string{}}
}

// within scripts the scope check's answer.
func within(yes bool) sqltest.Response {
	n := int64(0)
	if yes {
		n = 1
	}
	return sqltest.Response{Columns: []string{"matches"}, Rows: [][]driver.Value{{n}}}
}

// fileID is the uploaded file's id.
const fileID = "0199a5d2-4c7e-7000-8000-000000000001"

// file is the uploaded file's row at a status and version.
func file(status blobfs.Status, version int64) sqltest.Response {
	now := time.Now()
	f := blobfs.File{ID: fileID, DirectoryID: blobfs.RootID, Name: "a.txt", Status: status,
		Key: fileID + "/a.txt", ContentType: "text/plain", Version: version, CreatedAt: now, UpdatedAt: now}
	if status == blobfs.StatusAvailable {
		size, etag := int64(4), `"etag"`
		f.Size, f.ETag = &size, &etag
	}
	return datatest.FileRows(f)
}

func TestUpload_CommitsThePendingRowBeforeThePut(t *testing.T) {
	stores, db, rec, objects := open(t,
		within(true),                    // directory_is_within
		file(blobfs.StatusPending, 1),   // create_file
		file(blobfs.StatusAvailable, 2), // complete_file
		sqltest.Response{Columns: []string{"id"}, Rows: [][]driver.Value{{fileID}}}, // hold_file
		sqltest.Response{Affected: 1}, // attach
	)
	got, err := stores.Upload(context.Background(), db, objects, blobfs.RootID, blobfs.RootID,
		"a.txt", "text/plain", "note", strings.NewReader("body"), 4)
	if err != nil || got.Status != blobfs.StatusAvailable {
		t.Fatalf("Upload = %+v, %v", got, err)
	}
	begun := []sqltest.Op{sqltest.OpBegin, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpCommit}
	if !slices.Equal(objects.atPut, begun) {
		t.Errorf("ops at the put = %v, want %v", objects.atPut, begun)
	}
	want := append(begun, sqltest.OpQuery, // the completion, on the pool
		sqltest.OpBegin, sqltest.OpQuery, sqltest.OpExec, sqltest.OpCommit) // the hold and the note
	if got := rec.Ops(); !slices.Equal(got, want) {
		t.Errorf("ops = %v, want %v", got, want)
	}
}

func TestUpload_OutOfScopeStoresNothing(t *testing.T) {
	stores, db, rec, objects := open(t, within(false))
	_, err := stores.Upload(context.Background(), db, objects, blobfs.RootID, blobfs.RootID,
		"a.txt", "text/plain", "note", strings.NewReader("body"), 4)
	if !errors.Is(err, errOutOfScope) {
		t.Fatalf("err = %v, want errOutOfScope", err)
	}
	if objects.atPut != nil {
		t.Errorf("a put reached the store")
	}
	want := []sqltest.Op{sqltest.OpBegin, sqltest.OpQuery, sqltest.OpRollback}
	if got := rec.Ops(); !slices.Equal(got, want) {
		t.Errorf("ops = %v, want %v", got, want)
	}
}
```

```sh
mise run test
```

```
ok  	example.com/docstore
```

## 9. Lint the SQL

`sqlint.toml` names the pattern sources by module path. blobfs's module exports its published
patterns, so a statement of the program's that includes `{{> blobfs.file_columns}}` lints
against the real pattern.

`sqlint.toml`:

```toml
engine = "github.com/standards-lab/sqlate/postgres"

[sources]
sql = "github.com/standards-lab/sqlate"
blobfs = "github.com/standards-lab/blobfs"

[statements]
dirs = ["statements"]

[migrations]
dirs = ["migrations"]
```

```sh
mise run lint
```

```
sqlint: ok
```

## 10. Run the conformance suite

`datatest.Run` is blobfs's conformance suite: every check a store must pass against a live
database, comparing the store under test with the baseline on the same database. The program
runs it over the store as it composes it, the PostgreSQL engine and the overlaid patterns, in a
throwaway database with blobfs's set applied. A program that writes an engine of its own runs
the suite over that engine the same way. The test sits behind a build tag, so the unit tier
never needs the stack.

`conformance_test.go`:

```go
//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/migrate"
	sqlatepg "github.com/standards-lab/sqlate/postgres"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs/data"
	"github.com/standards-lab/blobfs/data/datatest"
	blobfspg "github.com/standards-lab/blobfs/postgres"
)

// TestConformance runs blobfs's conformance suite over the store as the
// program composes it: the PostgreSQL engine and the overlaid patterns.
func TestConformance(t *testing.T) {
	db := migrated(t)
	catalog, err := query.NewCatalog(sqlatepg.Patterns(), data.Patterns())
	if err != nil {
		t.Fatal(err)
	}
	datatest.Run(t, db, catalog, blobfspg.Engine)
}

// migrated creates a throwaway database on the server APP_DSN names,
// applies blobfs's migration set, and drops the database when the test
// ends.
func migrated(t *testing.T) *sqlate.DB {
	t.Helper()
	ctx := context.Background()
	admin, err := sql.Open("pgx", os.Getenv("APP_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	var b [4]byte
	_, _ = rand.Read(b[:])
	name := "conformance_" + hex.EncodeToString(b[:])
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(ctx, "DROP DATABASE "+name+" WITH (FORCE)") })

	u, err := url.Parse(os.Getenv("APP_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	pool, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	db := sqlate.Wrap(pool, sqlatepg.Dialect{})

	set, err := blobfspg.Migrations()
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.New(db, []migrate.Set{set}, migrate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	return db
}
```

```sh
mise run integration
```

```
ok  	example.com/docstore
```

## 11. Clean up

```sh
mise run down
```

The finished tree:

```
docstore/
├── compose/
│   ├── azurite/
│   │   └── Dockerfile
│   └── postgres/
│       └── Dockerfile
├── compose.yml
├── conformance_test.go
├── database.go
├── go.mod
├── main.go
├── migrations/
│   ├── 0001_attachment.down.sql
│   └── 0001_attachment.up.sql
├── mise.toml
├── objects.go
├── sqlint.toml
├── statements/
│   ├── attach.sql
│   └── detach.sql
├── stores.go
└── stores_test.go
```

From here, the [features](features.md) document covers every operation and refusal the program
skipped, and the [glossary](glossary.md) defines the terms.
