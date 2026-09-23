// Command example composes blobfs with a real object store: PostgreSQL
// through sqlate for the metadata, Azure Blob Storage through go-storage's
// azureblob provider for the bytes. It is the proof that the two libraries
// compose through one small adapter, with no change to either, and it is
// never imported. It runs one file's whole life: a directory, the two-phase
// write, a listing, the two-phase delete, and the provider's key rule
// reaching blobfs through the adapter.
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
	db, err := openDatabase(ctx)
	if err != nil {
		return err
	}
	objects, err := openStorage(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = objects.Shutdown(context.Background()) }()
	keys := keyValidator{objects}

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

	// The write: the pending row, then the object, then the row available.
	body := "quarterly numbers\n"
	file, err := store.Files.Create(ctx, db, keys, dir.ID, "Q3 summary.txt", "text/plain")
	if err != nil {
		return err
	}
	fmt.Printf("pending row %s stores under key %s\n", file.Name, file.Key)
	obj, err := objects.Put(ctx, file.Key, strings.NewReader(body), storage.PutOptions{ContentType: "text/plain", Size: int64(len(body))})
	if err != nil {
		return err
	}
	file, err = store.Files.Complete(ctx, db, file.ID, file.Version, blobfs.Object{Size: obj.Size, ContentType: "text/plain", ETag: obj.ETag})
	if err != nil {
		return err
	}
	fmt.Printf("completed: %s is %s, %d bytes, etag %s\n", file.Name, file.Status, *file.Size, *file.ETag)

	page, err := store.Files.List(ctx, db, dir.ID, query.Directives{}, query.Page{Number: 1, Size: 10})
	if err != nil {
		return err
	}
	fmt.Printf("listed %d of %d files in %s:\n", len(page.Items), page.Total, path)
	for _, f := range page.Items {
		fmt.Printf("  %s (%s)\n", f.Name, f.Status)
	}

	// The delete: the row marked deleting in a transaction, then the
	// object, then the row.
	deleting, err := sqlate.Transact(ctx, db, func(tx *sqlate.Tx) (blobfs.File, error) {
		return store.Files.Delete(ctx, tx, file.ID)
	})
	if err != nil {
		return err
	}
	if err := objects.Delete(ctx, deleting.Key); err != nil {
		return err
	}
	if err := store.Files.Purge(ctx, db, deleting.ID); err != nil {
		return err
	}
	if _, err := objects.Stat(ctx, deleting.Key); !isNotFound(err) {
		return fmt.Errorf("the object outlived its delete: %v", err)
	}
	fmt.Printf("deleted %s: the row is gone and the object with it\n", deleting.Name)

	// The provider's key rule, reached through the adapter. blobfs
	// sanitizes a filename into a key every provider it targets accepts, so
	// a hostile name becomes a valid key, and the rule itself refuses a key
	// blobfs would never build.
	hostile, err := store.Files.Create(ctx, db, keys, dir.ID, "draft...", "text/plain")
	if err != nil {
		return err
	}
	fmt.Printf("the name %q is stored under the key %s\n", hostile.Name, hostile.Key)
	if err := keys.ValidateKey(hostile.ID + "/draft."); err != nil {
		fmt.Printf("the provider refuses a raw key: %v\n", err)
	} else {
		return fmt.Errorf("the provider accepted a key ending in a dot")
	}
	if err := abandonWrite(ctx, db, store, hostile.ID); err != nil {
		return err
	}
	if err := store.Directories.Delete(ctx, db, dir.ID); err != nil {
		return err
	}
	fmt.Printf("removed %s\n", path)
	return nil
}

// abandonWrite removes a pending row whose object was never
// stored: the delete's first step, then its last, with nothing between.
func abandonWrite(ctx context.Context, db *sqlate.DB, store *data.Store, id string) error {
	if _, err := sqlate.Transact(ctx, db, func(tx *sqlate.Tx) (blobfs.File, error) {
		return store.Files.Delete(ctx, tx, id)
	}); err != nil {
		return err
	}
	return store.Files.Purge(ctx, db, id)
}

// keyValidator is the adapter: blobfs's one-method object-store interface
// over the store's own key rule. It is the only place the two libraries
// meet.
type keyValidator struct{ store *storage.Store }

var _ blobfs.KeyValidator = keyValidator{}

func (k keyValidator) ValidateKey(key string) error {
	return k.store.Capabilities().ValidateKey(key)
}

// openDatabase opens the pool, wraps it for sqlate, and brings blobfs's
// migration set up to date.
func openDatabase(ctx context.Context) (*sqlate.DB, error) {
	dsn := os.Getenv("BLOBFS_DSN")
	if dsn == "" {
		return nil, fmt.Errorf("BLOBFS_DSN is not set; run through mise, or set it")
	}
	pool, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
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
	return err != nil && errors.Is(err, storage.ErrNotFound)
}
