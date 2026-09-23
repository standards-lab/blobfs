package postgres_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/postgres"
)

var (
	createdTable = regexp.MustCompile(`(?i)\bCREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_]*)`)
	createdIndex = regexp.MustCompile(`(?i)\bCREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_]*)`)
	constraint   = regexp.MustCompile(`(?i)\bCONSTRAINT\s+([A-Za-z_][A-Za-z0-9_]*)`)
)

// TestMigrations loads the set and checks its shape: the name and the
// history table the package exports, the two migrations in order with
// strictly increasing versions, each transactional with an up and a down
// text, so a consumer's Reset can revert the set.
func TestMigrations(t *testing.T) {
	set, err := postgres.Migrations()
	if err != nil {
		t.Fatalf("Migrations: %v", err)
	}
	if set.Name != postgres.Source || set.Table != postgres.Table {
		t.Errorf("set = %q under %q, want %q under %q", set.Name, set.Table, postgres.Source, postgres.Table)
	}
	if len(set.Migrations) != 2 {
		t.Fatalf("Migrations returned %d migrations, want 2 (directory, file)", len(set.Migrations))
	}
	last := 0
	for i, name := range []string{"directory", "file"} {
		m := set.Migrations[i]
		if m.Name != name || m.Version != i+1 {
			t.Errorf("migration %d is %d %q, want %d %q", i, m.Version, m.Name, i+1, name)
		}
		if m.Version <= last {
			t.Errorf("version %d follows %d; versions must strictly increase", m.Version, last)
		}
		last = m.Version
		if m.Up == "" || m.Down == "" || !m.Transactional {
			t.Errorf("version %d %s: up %d bytes, down %d bytes, transactional %v; want both texts, in a transaction", m.Version, m.Name, len(m.Up), len(m.Down), m.Transactional)
		}
	}
}

// TestObjectNames scans every up text for the objects it creates (tables,
// indexes, and named constraints) and checks that each name starts with
// the set's name and an underscore, and that Table does too.
func TestObjectNames(t *testing.T) {
	prefix := postgres.Source + "_"
	if !strings.HasPrefix(postgres.Table, prefix) {
		t.Errorf("Table %q does not start with %q", postgres.Table, prefix)
	}
	set, err := postgres.Migrations()
	if err != nil {
		t.Fatalf("Migrations: %v", err)
	}
	for _, m := range set.Migrations {
		var names []string
		for _, re := range []*regexp.Regexp{createdTable, createdIndex, constraint} {
			for _, match := range re.FindAllStringSubmatch(m.Up, -1) {
				names = append(names, match[1])
			}
		}
		if len(names) == 0 {
			t.Errorf("version %d %s creates no named object", m.Version, m.Name)
		}
		for _, name := range names {
			if !strings.HasPrefix(name, prefix) {
				t.Errorf("version %d %s creates %q, which does not start with %q", m.Version, m.Name, name, prefix)
			}
		}
	}
}

// TestConstraintConstants proves that every constraint-name constant the
// root package exports names a constraint or a unique index the DDL
// declares, so the persistence layer's error mapping cannot drift from
// the schema. The scan is over the embedded up texts and needs no engine.
func TestConstraintConstants(t *testing.T) {
	set, err := postgres.Migrations()
	if err != nil {
		t.Fatalf("Migrations: %v", err)
	}
	declared := map[string]bool{}
	for _, m := range set.Migrations {
		for _, re := range []*regexp.Regexp{constraint, createdIndex} {
			for _, match := range re.FindAllStringSubmatch(m.Up, -1) {
				declared[match[1]] = true
			}
		}
	}
	for _, name := range []string{
		blobfs.ConstraintPrimaryKeyDirectory,
		blobfs.ConstraintPrimaryKeyFile,
		blobfs.ConstraintUniqueDirectoryRoot,
		blobfs.ConstraintUniqueDirectoryParentName,
		blobfs.ConstraintUniqueFileDirectoryName,
		blobfs.ConstraintForeignKeyDirectoryParent,
		blobfs.ConstraintForeignKeyFileDirectory,
	} {
		if !strings.HasPrefix(name, postgres.Source+"_") {
			t.Errorf("constant %q does not start with the source prefix", name)
		}
		if !declared[name] {
			t.Errorf("constant %q names no constraint or index in the DDL", name)
		}
	}
}

// TestRootSeed proves the directory migration seeds the root with the
// root package's id and the name /, under the partial unique index that
// allows no second root.
func TestRootSeed(t *testing.T) {
	set, err := postgres.Migrations()
	if err != nil {
		t.Fatalf("Migrations: %v", err)
	}
	seed := "INSERT INTO blobfs_directory (id, name) VALUES ('" + blobfs.RootID + "', '/')"
	if !strings.Contains(set.Migrations[0].Up, seed) {
		t.Errorf("the directory migration does not seed the root with RootID:\n%s", set.Migrations[0].Up)
	}
	if !strings.Contains(set.Migrations[0].Up, "CREATE UNIQUE INDEX "+blobfs.ConstraintUniqueDirectoryRoot) {
		t.Errorf("the directory migration does not create the root's unique index")
	}
}
