//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/migrate"
	sqlatepg "github.com/standards-lab/sqlate/postgres"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/postgres"
	"github.com/standards-lab/blobfs/postgres/internal/dbtest"
)

// consumerSet is a consumer's migration set declared above blobfs's: one
// table whose foreign key references blobfs_file, under the default
// history table.
var consumerSet = migrate.Set{
	Name: "consumer",
	Migrations: []migrate.Migration{{
		Version:       1,
		Name:          "bookmark",
		Up:            "CREATE TABLE consumer_bookmark (file_id uuid NOT NULL, CONSTRAINT consumer_fk_bookmark_file FOREIGN KEY (file_id) REFERENCES blobfs_file (id))",
		Down:          "DROP TABLE consumer_bookmark",
		Transactional: true,
	}},
}

// TestMigrationsWithAConsumerSet checks blobfs's set below a consumer's:
// the order of Up, Verify, the refused revert, each set's revert, Reset,
// and a replay from zero.
func TestMigrationsWithAConsumerSet(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Create(t).Session(sqlatepg.Dialect{})
	set, err := postgres.Migrations()
	if err != nil {
		t.Fatalf("Migrations: %v", err)
	}
	m, err := migrate.New(db, []migrate.Set{set, consumerSet}, migrate.Options{})
	if err != nil {
		t.Fatalf("migrate.New: %v", err)
	}
	latest := set.Migrations[len(set.Migrations)-1].Version
	up := func() {
		t.Helper()
		if err := m.Up(ctx); err != nil {
			t.Fatalf("Up: %v", err)
		}
		if err := m.Verify(ctx); err != nil {
			t.Fatalf("Verify: %v", err)
		}
		status, err := m.Status(ctx)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if len(status) != 2 || status[0].Name != postgres.Source || status[0].Table != postgres.Table || status[0].Version != latest ||
			status[1].Name != "consumer" || status[1].Version != 1 || status[0].Dirty || status[1].Dirty {
			t.Fatalf("Status after Up = %+v, want blobfs at %d under %s and the consumer at 1", status, latest, postgres.Table)
		}
		if n := dbtest.Int(ctx, t, db, "SELECT count(*) FROM blobfs_directory WHERE id = $1 AND parent_id IS NULL AND name = '/'", blobfs.RootID); n != 1 {
			t.Errorf("the root is seeded %d times, want once", n)
		}
	}
	up()

	blobfsLayer, ok := m.Layer(postgres.Source)
	if !ok {
		t.Fatal("the migrator has no blobfs layer")
	}
	if err := blobfsLayer.Down(ctx, 1); !errors.Is(err, migrate.ErrAboveApplied) {
		t.Errorf("reverting blobfs below the applied consumer set = %v, want ErrAboveApplied", err)
	}
	if err := m.Down(ctx, 1); err != nil {
		t.Fatalf("Down of the consumer set: %v", err)
	}
	if dbtest.Exists(ctx, t, db, "consumer_bookmark") {
		t.Error("the consumer's table survived its revert")
	}
	if err := blobfsLayer.Down(ctx, len(set.Migrations)); err != nil {
		t.Fatalf("Down of blobfs's set: %v", err)
	}
	for _, table := range []string{"blobfs_directory", "blobfs_file"} {
		if dbtest.Exists(ctx, t, db, table) {
			t.Errorf("%s survived the revert", table)
		}
	}
	up()

	if err := m.Reset(ctx); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	for _, table := range []string{"blobfs_directory", "blobfs_file", "consumer_bookmark", postgres.Table, migrate.DefaultTable} {
		if dbtest.Exists(ctx, t, db, table) {
			t.Errorf("%s survived the reset", table)
		}
	}
	up()
}

func ptr(s string) *string { return &s }

// insertDirectory inserts a directory row through plain SQL, binding both
// nullable columns, so a test can build any combination the root rule
// allows or refuses.
func insertDirectory(ctx context.Context, db *sqlate.DB, parent, name *string) (string, error) {
	id := blobfs.NewID()
	_, err := db.ExecContext(ctx, "INSERT INTO blobfs_directory (id, parent_id, name) VALUES ($1, $2, $3)", id, parent, name)
	return id, err
}

// insertFile inserts a file row through plain SQL.
func insertFile(ctx context.Context, db *sqlate.DB, dir, name, status string) (string, error) {
	id := blobfs.NewID()
	_, err := db.ExecContext(ctx,
		"INSERT INTO blobfs_file (id, directory_id, name, status, key, content_type) VALUES ($1, $2, $3, $4, $5, 'text/plain')",
		id, dir, name, status, id+"/"+blobfs.SanitizeFilename(name))
	return id, err
}

// TestConstraints checks every named constraint and index as the engine
// reports its violation. The root's index, which no store operation can
// reach, is proved only here; the others' mapping to sentinels is proved
// through the store in the conformance suite.
func TestConstraints(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Migrated(t).Session(sqlatepg.Dialect{})
	root := blobfs.RootID
	docs, err := insertDirectory(ctx, db, &root, ptr("docs"))
	if err != nil {
		t.Fatalf("insert docs: %v", err)
	}
	aTxt, err := insertFile(ctx, db, docs, "a.txt", "available")
	if err != nil {
		t.Fatalf("insert a.txt: %v", err)
	}
	for _, c := range []struct {
		name       string
		violate    func() error
		class      error
		constraint string
	}{
		{"SecondRoot", func() error { _, err := insertDirectory(ctx, db, nil, ptr("/")); return err }, sqlate.ErrUniqueViolation, blobfs.ConstraintUniqueDirectoryRoot},
		{"RootReinserted", func() error {
			_, err := db.ExecContext(ctx, "INSERT INTO blobfs_directory (id, name) VALUES ($1, '/')", blobfs.RootID)
			return err
		}, sqlate.ErrUniqueViolation, blobfs.ConstraintPrimaryKeyDirectory},
		{"RootUnderAnotherName", func() error { _, err := insertDirectory(ctx, db, nil, ptr("root")); return err }, sqlate.ErrCheckViolation, "blobfs_cc_directory_root_name"},
		{"NonRootNamedSlash", func() error { _, err := insertDirectory(ctx, db, &root, ptr("/")); return err }, sqlate.ErrCheckViolation, "blobfs_cc_directory_root_name"},
		{"EmptyDirectoryName", func() error { _, err := insertDirectory(ctx, db, &root, ptr("")); return err }, sqlate.ErrCheckViolation, "blobfs_cc_directory_name"},
		{"OwnParent", func() error {
			_, err := db.ExecContext(ctx, "INSERT INTO blobfs_directory (id, parent_id, name) VALUES ($1, $1, 'self')", blobfs.NewID())
			return err
		}, sqlate.ErrCheckViolation, "blobfs_cc_directory_parent_not_self"},
		{"DirectoryStatus", func() error {
			_, err := db.ExecContext(ctx, "INSERT INTO blobfs_directory (id, parent_id, name, status) VALUES ($1, $2, 'bogus', 'bogus')", blobfs.NewID(), root)
			return err
		}, sqlate.ErrCheckViolation, "blobfs_cc_directory_status"},
		{"DirectoryNameTaken", func() error { _, err := insertDirectory(ctx, db, &root, ptr("docs")); return err }, sqlate.ErrUniqueViolation, blobfs.ConstraintUniqueDirectoryParentName},
		{"MissingParent", func() error { _, err := insertDirectory(ctx, db, ptr(blobfs.NewID()), ptr("orphan")); return err }, sqlate.ErrForeignKeyViolation, blobfs.ConstraintForeignKeyDirectoryParent},
		{"FileNameTaken", func() error { _, err := insertFile(ctx, db, docs, "a.txt", "pending"); return err }, sqlate.ErrUniqueViolation, blobfs.ConstraintUniqueFileDirectoryName},
		// A row that repeats both the id and the name fails the primary
		// key: PostgreSQL checks a table's unique indexes in creation
		// order, the primary key's first, which is why a caller that loses
		// a race under one supplied id meets ErrIDTaken, not ErrNameTaken.
		{"DirectoryIDAndNameTaken", func() error {
			_, err := db.ExecContext(ctx, "INSERT INTO blobfs_directory (id, parent_id, name) VALUES ($1, $2, 'docs')", docs, root)
			return err
		}, sqlate.ErrUniqueViolation, blobfs.ConstraintPrimaryKeyDirectory},
		{"FileIDAndNameTaken", func() error {
			_, err := db.ExecContext(ctx,
				"INSERT INTO blobfs_file (id, directory_id, name, status, key, content_type) VALUES ($1, $2, 'a.txt', 'pending', $3, 'text/plain')",
				aTxt, docs, aTxt+"/a.txt")
			return err
		}, sqlate.ErrUniqueViolation, blobfs.ConstraintPrimaryKeyFile},
		{"FileStatus", func() error { _, err := insertFile(ctx, db, docs, "b.txt", "bogus"); return err }, sqlate.ErrCheckViolation, "blobfs_cc_file_status"},
		{"EmptyFileName", func() error { _, err := insertFile(ctx, db, docs, "", "pending"); return err }, sqlate.ErrCheckViolation, "blobfs_cc_file_name"},
		{"MissingDirectory", func() error { _, err := insertFile(ctx, db, blobfs.NewID(), "c.txt", "pending"); return err }, sqlate.ErrForeignKeyViolation, blobfs.ConstraintForeignKeyFileDirectory},
		{"DeleteAParent", func() error {
			_, err := db.ExecContext(ctx, "DELETE FROM blobfs_directory WHERE id = $1", blobfs.RootID)
			return err
		}, sqlate.ErrForeignKeyViolation, blobfs.ConstraintForeignKeyDirectoryParent},
		{"DeleteADirectoryWithFiles", func() error {
			_, err := db.ExecContext(ctx, "DELETE FROM blobfs_directory WHERE id = $1", docs)
			return err
		}, sqlate.ErrForeignKeyViolation, blobfs.ConstraintForeignKeyFileDirectory},
	} {
		t.Run(c.name, func(t *testing.T) {
			if name := dbtest.Constraint(t, c.violate(), c.class); name != c.constraint {
				t.Errorf("violated constraint = %q, want %s", name, c.constraint)
			}
		})
	}
	if _, err := insertDirectory(ctx, db, &root, nil); !errors.Is(err, sqlate.ErrNotNullViolation) {
		t.Errorf("a directory with no name = %v, want ErrNotNullViolation", err)
	}
	if _, err := insertDirectory(ctx, db, &docs, ptr("docs")); err != nil {
		t.Errorf("the same name under another parent: %v", err)
	}
	if _, err := insertDirectory(ctx, db, &docs, ptr("a.txt")); err != nil {
		t.Errorf("a directory under a file's name: %v; the name spaces are separate", err)
	}
	if n := dbtest.Int(ctx, t, db, "SELECT count(*) FROM blobfs_directory WHERE parent_id IS NULL"); n != 1 {
		t.Errorf("%d roots after the refusals, want the one seeded", n)
	}
}
