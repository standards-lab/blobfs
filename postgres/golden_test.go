package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"testing"
)

// released pins the sha256 of every migration file by name. A released
// file never changes in text or name: a new migration adds a row here, and
// a changed row is a breaking change to the schema's version line. The
// discipline starts at the module's first release, v0.1.0; the files are
// the spike's, unchanged in text.
var released = map[string]string{
	"0001_directory.up.sql":   "dc9b2f45d590bd5d06f897c40bb8ab9983aed40beb323fc7a44ac83574301b46",
	"0001_directory.down.sql": "7193988c04d57b6b7729c0daa0523ee70e3c920e12baec2897ce720d5e274ba9",
	"0002_file.up.sql":        "bafff9b9d797e61791ecbcdfd5df7db414fb2ddb2259e547b87eaa0eed454712",
	"0002_file.down.sql":      "adbf56e6f4f0ab977bcb8b3e7874bae41fe3eebf7e7a610e9cda98c162f3b14c",
}

// TestGoldenHashes checks every embedded migration file against the
// pinned hashes: a file whose text changed, a file the table does not
// list, and a listed file missing from the directory are each a failure.
func TestGoldenHashes(t *testing.T) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		text, err := fs.ReadFile(migrationFiles, "migrations/"+e.Name())
		if err != nil {
			t.Fatalf("ReadFile %s: %v", e.Name(), err)
		}
		sum := sha256.Sum256(text)
		got := hex.EncodeToString(sum[:])
		want, ok := released[e.Name()]
		switch {
		case !ok:
			t.Errorf("%s is not in the released table; add it with hash %s", e.Name(), got)
		case got != want:
			t.Errorf("%s changed: hash %s, released %s", e.Name(), got, want)
		}
		seen[e.Name()] = true
	}
	for name := range released {
		if !seen[name] {
			t.Errorf("released file %s is missing from the migrations directory", name)
		}
	}
}
