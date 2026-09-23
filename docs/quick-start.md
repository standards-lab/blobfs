# Quick start

A tutorial that builds a working program from an empty directory: a document store that keeps
its tree in PostgreSQL through blobfs and its bytes in Azure Blob Storage through `go-storage`,
emulated locally by Azurite. The program:

- adopts blobfs's migration set beneath a migration set of its own
- builds one pattern catalog and the blobfs store over the PostgreSQL engine
- verifies every statement against the migrated schema
- writes a file, lists its directory, and deletes it, keeping a row of its own beside the file's
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

`blobfs` is the base module: the root package and the persistence package `data`, over `sqlate`
alone. `blobfs/postgres` is the PostgreSQL engine and its migration set. blobfs names no driver
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
description = "Start PostgreSQL and Azurite and wait until they are healthy"
run = "docker compose up -d --wait"

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

`compose.yml`:

```yaml
services:
  postgres:
    image: postgres:18-alpine
    environment:
      POSTGRES_USER: app
      POSTGRES_PASSWORD: app
      POSTGRES_DB: app
    ports:
      - "127.0.0.1:5433:5432"
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U app -d app"]
      interval: 2s
      timeout: 3s
      retries: 15
  azurite:
    image: mcr.microsoft.com/azure-storage/azurite:3.37.0
    command: azurite-blob --blobHost 0.0.0.0 --skipApiVersionCheck
    ports:
      - "127.0.0.1:10000:10000"
    healthcheck:
      test: ["CMD-SHELL", "nc -z 127.0.0.1 10000"]
      interval: 2s
      timeout: 3s
      retries: 15
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
default `schema_version`, and `Up` brings blobfs's set to its head before the program's runs.
Concurrent starters serialize on the migrator's lock.

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

blobfs asks one thing of the object store: whether it accepts a key. The adapter wires
`blobfs.KeyValidator` to the store's own rule, `Capabilities().ValidateKey`, and it is the only
place the two libraries meet. `openObjects` builds the Azure Blob store from the environment and
starts it, which ensures the container exists.

`objects.go`:

```go
package main

import (
	"context"
	"fmt"

	"github.com/standards-lab/go-storage"
	"github.com/standards-lab/go-storage/azureblob"

	"github.com/standards-lab/blobfs"
)

// keyValidator is the adapter: blobfs's one-method interface over the
// object store's own key rule, and the only place the two libraries meet.
type keyValidator struct{ store *storage.Store }

var _ blobfs.KeyValidator = keyValidator{}

func (k keyValidator) ValidateKey(key string) error {
	return k.store.Capabilities().ValidateKey(key)
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
-- Records the program's note about a file, in the transaction that inserts
-- the file's pending row.
INSERT INTO app_attachment (file_id, note)
VALUES ({{file_id:uuid}}, {{note}})
```

`statements/detach.sql`:

```sql
--| tier: standard
-- Removes the program's note about a file, in the transaction that begins
-- the file's delete.
DELETE FROM app_attachment
WHERE file_id = {{file_id:uuid}}
```

The stores layer builds the program's one catalog: sqlate's patterns with the PostgreSQL overlay
of the cursor predicate, `sqlatepg.Patterns()`, and blobfs's published patterns,
`data.Patterns()`. `data.New` compiles blobfs's statements against it and installs the
PostgreSQL engine with `data.WithEngine`; the program's own statements compile against the same
catalog. `Verify` prepares both against the migrated schema.

`BeginUpload` and `BeginDelete` are the first step of each protocol, each in one transaction
with the program's own row. The pending row and its note commit together before any byte reaches
the store. The note is removed in the transaction that marks the file deleting, so the program's
foreign key never refuses the purge.

`stores.go`:

```go
package main

import (
	"context"
	"embed"

	"github.com/standards-lab/sqlate"
	sqlatepg "github.com/standards-lab/sqlate/postgres"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data"
	blobfspg "github.com/standards-lab/blobfs/postgres"
)

//go:embed statements/*.sql
var statements embed.FS

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

// BeginUpload is the first step of an upload: the file's pending row and
// the program's note about it, committed together before any byte reaches
// the store.
func (s *Stores) BeginUpload(ctx context.Context, db *sqlate.DB, keys blobfs.KeyValidator, dirID, name, contentType, note string) (blobfs.File, error) {
	return sqlate.Transact(ctx, db, func(tx *sqlate.Tx) (blobfs.File, error) {
		file, err := s.Blobfs.Files.Create(ctx, tx, keys, dirID, name, contentType)
		if err != nil {
			return blobfs.File{}, err
		}
		_, err = s.attach.Exec(ctx, tx, query.Args{"file_id": file.ID, "note": note})
		return file, err
	})
}

// BeginDelete is the first step of a delete: the program's note removed
// and the file's row marked deleting, in one transaction. It returns the
// row with the key to delete the object under.
func (s *Stores) BeginDelete(ctx context.Context, db *sqlate.DB, id string) (blobfs.File, error) {
	return sqlate.Transact(ctx, db, func(tx *sqlate.Tx) (blobfs.File, error) {
		if _, err := s.detach.Exec(ctx, tx, query.Args{"file_id": id}); err != nil {
			return blobfs.File{}, err
		}
		return s.Blobfs.Files.Delete(ctx, tx, id)
	})
}
```

## 7. Write the program

`main.go` runs the sequence: connect and migrate, start the store, compile and verify, then one
file's whole life.

- `Directories.Ensure` returns the `reports` directory under the root, creating it on the first
  run only.
- The write is `BeginUpload`, the put under the row's `Key`, and `Files.Complete` with what the
  store reported, guarded by the pending row's version. The program passes the content type it
  declared, since the store's report may differ.
- The listing resolves the directory by path from the root and reads one page of its files,
  newest first, with the total counted in the page's statement.
- The delete is `BeginDelete`, the object delete, and `Files.Purge`.

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

	"github.com/standards-lab/go-storage"
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
	keys := keyValidator{objects}

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

	// The write: the pending row, then the object, then the row available.
	body := "quarterly numbers\n"
	file, err := stores.BeginUpload(ctx, db, keys, dir.ID, "Q3 summary.txt", "text/plain", "for the board")
	if err != nil {
		return err
	}
	fmt.Printf("pending: %s under the key %s\n", file.Name, file.Key)
	obj, err := objects.Put(ctx, file.Key, strings.NewReader(body),
		storage.PutOptions{ContentType: "text/plain", Size: int64(len(body))})
	if err != nil {
		return err
	}
	file, err = store.Files.Complete(ctx, db, file.ID, file.Version,
		blobfs.Object{Size: obj.Size, ContentType: "text/plain", ETag: obj.ETag})
	if err != nil {
		return err
	}
	fmt.Printf("%s: %s, %d bytes\n", file.Name, file.Status, *file.Size)

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

	// The delete: the row marked deleting, then the object, then the row.
	deleting, err := stores.BeginDelete(ctx, db, file.ID)
	if err != nil {
		return err
	}
	if err := objects.Delete(ctx, deleting.Key); err != nil {
		return err
	}
	if err := store.Files.Purge(ctx, db, deleting.ID); err != nil {
		return err
	}
	fmt.Printf("deleted %s\n", deleting.Name)
	return nil
}
```

```sh
mise run run
```

```
directory reports: id 01a0d043-c5e8-7dba-afb7-955416e54350, created true
pending: Q3 summary.txt under the key 01a0d043-c5ef-7a28-ae5d-570c1d4979b4/Q3 summary.txt
Q3 summary.txt: available, 18 bytes
/reports: 1 of 1 files
  Q3 summary.txt (available)
deleted Q3 summary.txt
```

The ids differ on every run. A second run reports `created false` and the same directory id, and
writes the file under a new key, since each write mints a new id.

## 8. Test without a database

A unit test compiles the stores for `sqltest`'s stub dialect and runs them over its scripted
driver, so it needs no database. Under the stub dialect a returning command runs its fallback,
the command and then its read; a test that wants the single-statement form uses
`sqltest.ReturningDialect`. The first test scripts the insert, the read of the pending row, and
the note, and proves the three ran in one transaction. The second proves a key the store refuses
stops the write before any SQL.

`stores_test.go`:

```go
package main

import (
	"context"
	"database/sql/driver"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/sqltest"

	"github.com/standards-lab/blobfs"
)

// acceptAll and refuseAll stand in for the object store's key rule.
type acceptAll struct{}

func (acceptAll) ValidateKey(string) error { return nil }

type refuseAll struct{}

func (refuseAll) ValidateKey(string) error { return errors.New("refused") }

// open compiles the stores for sqltest's stub dialect and opens a scripted
// pool over the responses.
func open(t *testing.T, responses ...sqltest.Response) (*Stores, *sqlate.DB, *sqltest.Recorder) {
	t.Helper()
	pool, rec := sqltest.Open(t, responses...)
	stores, err := newStores(sqltest.Dialect{})
	if err != nil {
		t.Fatalf("newStores: %v", err)
	}
	return stores, sqlate.Wrap(pool, sqltest.Dialect{}), rec
}

// fileColumns is a file row's column list, in the order blobfs.File scans.
var fileColumns = []string{"id", "directory_id", "name", "status", "key", "size",
	"content_type", "etag", "version", "created_at", "updated_at"}

func TestBeginUpload_WritesTheRowAndTheNoteInOneTransaction(t *testing.T) {
	now := time.Now()
	id := "0199a5d2-4c7e-7000-8000-000000000001"
	stores, db, rec := open(t,
		sqltest.Response{Affected: 1}, // create_file
		sqltest.Response{Columns: fileColumns, Rows: [][]driver.Value{{ // file_by_id
			id, blobfs.RootID, "a.txt", "pending", id + "/a.txt", nil,
			"text/plain", nil, int64(1), now, now,
		}}},
		sqltest.Response{Affected: 1}, // attach
	)
	file, err := stores.BeginUpload(context.Background(), db, acceptAll{}, blobfs.RootID, "a.txt", "text/plain", "note")
	if err != nil || file.Status != blobfs.StatusPending {
		t.Fatalf("BeginUpload = %+v, %v", file, err)
	}
	want := []sqltest.Op{sqltest.OpBegin, sqltest.OpExec, sqltest.OpQuery, sqltest.OpExec, sqltest.OpCommit}
	if got := rec.Ops(); !slices.Equal(got, want) {
		t.Errorf("ops = %v, want %v", got, want)
	}
}

func TestBeginUpload_RefusedKeyRunsNoSQL(t *testing.T) {
	stores, db, rec := open(t)
	_, err := stores.BeginUpload(context.Background(), db, refuseAll{}, blobfs.RootID, "a.txt", "text/plain", "note")
	if !errors.Is(err, blobfs.ErrInvalidKey) {
		t.Fatalf("err = %v, want blobfs.ErrInvalidKey", err)
	}
	for _, op := range rec.Ops() {
		if op == sqltest.OpExec || op == sqltest.OpQuery {
			t.Errorf("a statement reached the driver: %v", rec.Ops())
		}
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
