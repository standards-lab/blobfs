package postgres_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/standards-lab/blobfs/postgres"
)

// released pins every released migration as Migrations returns it: its
// name, the sha256 of its up and down texts, and whether it runs in a
// transaction. A released migration never changes: a new migration adds a
// row here, and a changed row is a breaking change to the schema's
// version line. The texts are the files' bodies without their "--|"
// header, which Transactional carries. The pins start at the module's
// first release, v0.1.0, and hold through postgres/v0.3.0.
var released = map[int]struct {
	name, up, down string
	transactional  bool
}{
	1: {"directory", "f5f3d03a3ec5013c6ea12d5deeff5637800e9a38a0e02952f2b65a98b89cde2d", "7193988c04d57b6b7729c0daa0523ee70e3c920e12baec2897ce720d5e274ba9", true},
	2: {"file", "e47ca67c3d3c36ea31cd50423de535109e0c729c9b191ff2a3b50216650fdd0a", "adbf56e6f4f0ab977bcb8b3e7874bae41fe3eebf7e7a610e9cda98c162f3b14c", true},
	3: {"directory_status", "aaf3bfdc482e830a01c8683659e7316643a59c7c9a6096664f4ee0e35aa1af93", "950b599bfb04b903289b0cdef7c78f24889c050f086c82c8d8dfc6569ffb0b23", true},
}

// sum is the hex sha256 of text.
func sum(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])
}

// TestReleasedMigrations checks the migration set against the pins: a
// released migration whose text, name, or transaction changed, a
// migration the table does not list, and a listed migration missing from
// the set are each a failure.
func TestReleasedMigrations(t *testing.T) {
	set, err := postgres.Migrations()
	if err != nil {
		t.Fatalf("Migrations: %v", err)
	}
	seen := map[int]bool{}
	for _, m := range set.Migrations {
		seen[m.Version] = true
		want, ok := released[m.Version]
		if !ok {
			t.Errorf("version %d %s is not in the released table; add it with up %s and down %s", m.Version, m.Name, sum(m.Up), sum(m.Down))
			continue
		}
		if m.Name != want.name || sum(m.Up) != want.up || sum(m.Down) != want.down || m.Transactional != want.transactional {
			t.Errorf("released version %d changed: %s up %s down %s transactional %v, released %s up %s down %s transactional %v",
				m.Version, m.Name, sum(m.Up), sum(m.Down), m.Transactional, want.name, want.up, want.down, want.transactional)
		}
	}
	for version, want := range released {
		if !seen[version] {
			t.Errorf("released version %d %s is missing from the set", version, want.name)
		}
	}
}
