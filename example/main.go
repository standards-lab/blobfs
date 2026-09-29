// Command example composes blobfs with a real object store: PostgreSQL
// through sqlate for the metadata, Azure Blob Storage through go-storage's
// azureblob provider for the bytes. It is the proof that the two libraries
// compose through one small adapter, with no change to either, and it is
// never imported. It runs one file's whole life through the store's
// protocols: a directory, Store.Write, a listing, Store.Remove, the
// provider's key rule reaching blobfs through the adapter, and a branch
// marked for removal and drained by Store.SweepUntilDone.
//
// It reads BLOBFS_DSN and the BLOBFS_STORAGE_* settings go-storage
// documents; mise.toml sets both for the compose stack, so from the
// repository root:
//
//	mise run up
//	mise run example
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/standards-lab/go-storage"
	"github.com/standards-lab/go-storage/azureblob"
	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/migrate"
	sqlatepg "github.com/standards-lab/sqlate/postgres"
	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data"
	blobfspg "github.com/standards-lab/blobfs/postgres"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	db, pool, err := openDatabase(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = pool.Close() }()
	objects, err := openStorage(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = objects.Shutdown(context.Background()) }()
	objs := objectStore{objects}

	catalog, err := query.NewCatalog(sqlatepg.Patterns(), data.Patterns())
	if err != nil {
		return err
	}
	store, err := data.New(catalog, db.Dialect(), data.WithEngine(blobfspg.Engine))
	if err != nil {
		return err
	}
	if err := store.Verify(ctx, db); err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	fmt.Println("verified the store's statements against the schema")

	// A directory of its own under the root, so repeated runs never meet.
	dir, err := store.Directories.Create(ctx, db, blobfs.RootID, "example-"+time.Now().UTC().Format("20060102T150405.000"))
	if err != nil {
		return err
	}
	path, err := store.Directories.Path(ctx, db, dir.ID)
	if err != nil {
		return err
	}
	fmt.Printf("created directory %s\n", path)

	// The write: the pending row, then the object, then the row available,
	// run end to end by the store through the adapter.
	body := "quarterly numbers\n"
	file, err := store.Write(ctx, db, objs, strings.NewReader(body), int64(len(body)), func(tx *sqlate.Tx) (blobfs.File, error) {
		return store.Files.Create(ctx, tx, objs, dir.ID, "Q3 summary.txt", "text/plain")
	})
	if err != nil {
		return err
	}
	fmt.Printf("wrote %s under key %s: %s, %d bytes, etag %s\n", file.Name, file.Key, file.Status, *file.Size, *file.ETag)

	page, err := store.Files.List(ctx, db, dir.ID, query.Directives{}, query.Page{Number: 1, Size: 10})
	if err != nil {
		return err
	}
	fmt.Printf("listed %d of %d files in %s:\n", len(page.Items), page.Total, path)
	for _, f := range page.Items {
		fmt.Printf("  %s (%s)\n", f.Name, f.Status)
	}

	// The delete: the row marked deleting, then the object, then the row.
	if err := store.Remove(ctx, db, objs, func(*sqlate.Tx) (string, error) { return file.ID, nil }); err != nil {
		return err
	}
	if _, err := objects.Stat(ctx, file.Key); !isNotFound(err) {
		return fmt.Errorf("the object outlived its delete: %v", err)
	}
	fmt.Printf("removed %s: the row is gone and the object with it\n", file.Name)

	// The provider's key rule, reached through the adapter. blobfs
	// sanitizes a filename into a key every provider it targets accepts, so
	// a hostile name becomes a valid key, and the rule itself refuses a key
	// blobfs would never build.
	hostile, err := store.Files.Create(ctx, db, objs, dir.ID, "draft...", "text/plain")
	if err != nil {
		return err
	}
	fmt.Printf("the name %q is stored under the key %s\n", hostile.Name, hostile.Key)
	if err := objs.ValidateKey(hostile.ID + "/draft."); err != nil {
		fmt.Printf("the provider refuses a raw key: %v\n", err)
	} else {
		return fmt.Errorf("the provider accepted a key ending in a dot")
	}
	// A pending row whose object was never stored is removed the same way:
	// deleting a missing object is success.
	if err := store.Remove(ctx, db, objs, func(*sqlate.Tx) (string, error) { return hostile.ID, nil }); err != nil {
		return err
	}

	// A branch: a subdirectory holding three files, marked for removal in
	// one step and drained by the sweep in passes of two records, each pass
	// wrapped as a consumer would hold a gate of its own around it.
	if err := sweepBranch(ctx, db, store, objs, dir.ID); err != nil {
		return err
	}

	if err := store.Directories.Delete(ctx, db, dir.ID); err != nil {
		return err
	}
	fmt.Printf("removed %s\n", path)
	return nil
}

// sweepBranch writes a branch of three files under parentID, marks it
// deleting, and drains it with Store.SweepUntilDone, reporting each pass.
func sweepBranch(ctx context.Context, db *sqlate.DB, store *data.Store, objs objectStore, parentID string) error {
	branch, err := store.Directories.Create(ctx, db, parentID, "archive")
	if err != nil {
		return err
	}
	for _, name := range []string{"jan.txt", "feb.txt", "mar.txt"} {
		body := "month " + name + "\n"
		if _, err := store.Write(ctx, db, objs, strings.NewReader(body), int64(len(body)), func(tx *sqlate.Tx) (blobfs.File, error) {
			return store.Files.Create(ctx, tx, objs, branch.ID, name, "text/plain")
		}); err != nil {
			return err
		}
	}
	marked, err := sqlate.Transact(ctx, db, func(tx *sqlate.Tx) (data.Marked, error) {
		return store.Directories.MarkDeleting(ctx, tx, branch.ID)
	})
	if err != nil {
		return err
	}
	fmt.Printf("marked archive deleting: %d directory, %d files\n", marked.Directories, marked.Files)

	passes := 0
	around := data.AroundPass(func(ctx context.Context, pass func(context.Context) (data.SweepResult, error)) (data.SweepResult, error) {
		passes++
		return pass(ctx)
	})
	report := func(res data.SweepResult, err error) {
		fmt.Printf("  pass %d: %d files, %d directories, more %t, err %v\n", passes, res.Files, res.Directories, res.More, err)
	}
	if err := store.SweepUntilDone(ctx, db, objs, nil, report, data.Batch(2), around); err != nil {
		return err
	}
	if _, err := store.Directories.Find(ctx, db, branch.ID); !errors.Is(err, blobfs.ErrNotFound) {
		return fmt.Errorf("the branch outlived its sweep: %v", err)
	}
	fmt.Printf("swept archive in %d passes\n", passes)
	return nil
}

// objectStore is the adapter: blobfs's object-store interfaces over the
// started store, its key rule, its put, and its delete. It is the only
// place the two libraries meet.
type objectStore struct{ store *storage.Store }

var (
	_ blobfs.KeyValidator = objectStore{}
	_ data.ObjectStore    = objectStore{}
)

func (o objectStore) ValidateKey(key string) error {
	return o.store.Capabilities().ValidateKey(key)
}

// PutObject stores body under key and reports it in the content type the
// caller declared.
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

// openDatabase opens the pool, wraps it for sqlate, and brings blobfs's
// migration set up to date. It returns the pool beside the wrapper, since
// sqlate's wrapper does not own the pool: the caller closes it.
func openDatabase(ctx context.Context) (*sqlate.DB, *sql.DB, error) {
	dsn := os.Getenv("BLOBFS_DSN")
	if dsn == "" {
		return nil, nil, fmt.Errorf("BLOBFS_DSN is not set; run through mise, or set it")
	}
	pool, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, nil, err
	}
	db, err := migrated(ctx, pool)
	if err != nil {
		_ = pool.Close()
		return nil, nil, err
	}
	return db, pool, nil
}

// migrated wraps pool for sqlate and applies blobfs's migration set.
func migrated(ctx context.Context, pool *sql.DB) (*sqlate.DB, error) {
	db := sqlate.Wrap(pool, sqlatepg.Dialect{})
	set, err := blobfspg.Migrations()
	if err != nil {
		return nil, err
	}
	m, err := migrate.New(db, []migrate.Set{set}, migrate.Options{})
	if err != nil {
		return nil, err
	}
	if err := m.Up(ctx); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

// openStorage builds the Azure Blob store from the BLOBFS_STORAGE_*
// settings and starts it, which ensures the container and probes it.
func openStorage(ctx context.Context) (*storage.Store, error) {
	var cfg storage.Config
	if err := cfg.Finalize("blobfs"); err != nil {
		return nil, fmt.Errorf("storage configuration: %w", err)
	}
	client, err := azureblob.New(cfg)
	if err != nil {
		return nil, err
	}
	store := storage.New(client, cfg)
	if err := store.Start(ctx); err != nil {
		return nil, fmt.Errorf("start storage: %w", err)
	}
	return store, nil
}

func isNotFound(err error) bool {
	return errors.Is(err, storage.ErrNotFound)
}
