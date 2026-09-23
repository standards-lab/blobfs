//go:build integration

package dbtest

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/standards-lab/sqlate"

	"github.com/standards-lab/blobfs"
)

// Sizes is the shape of a Tree: the depth of the chain, the number of
// other directories, and the files in the big directory and spread over
// the others.
type Sizes struct {
	Depth       int
	Directories int
	BigFiles    int
	OtherFiles  int
}

// Tree is the fixture the cost assertions run on, seeded by SeedTree: a
// chain of directories l1/l2/.../ln below the root for path resolution,
// one directory big whose files were inserted first and so sit on contiguous heap
// pages, and Directories more directories attached at random below the
// chain's first levels, each holding a share of OtherFiles. Every id is a
// blobfs id, and the rows are inserted in bulk through unnest.
type Tree struct {
	Sizes Sizes
	// Chain holds the chain's directories by depth, Chain[0] at depth 1.
	Chain []blobfs.Directory
	// Big is the directory whose files are BigFiles, at depth 1.
	Big blobfs.Directory
	// Others are the other directories; each holds some of OtherFiles.
	Others []blobfs.Directory
}

// Path returns the chain's path at depth, relative to the root:
// l1/l2/... with depth segments.
func (tr Tree) Path(depth int) string {
	names := make([]string, depth)
	for i := range depth {
		names[i] = tr.Chain[i].Name
	}
	return strings.Join(names, "/")
}

// SeedTree inserts a Tree of sizes into db, which holds the migrated
// schema and nothing else, and analyzes both tables, so the planner
// chooses among the fixture's real row counts. It is deterministic under
// a fixed seed: the same sizes give the same names, the same attachment
// of directories, and the same spread of created_at values over the year
// before the seeding, which a sort by created_at needs to be
// non-degenerate. File names are numbered by a permutation, so name
// order and heap order differ.
func SeedTree(ctx context.Context, t testing.TB, db *sqlate.DB, sizes Sizes) Tree {
	t.Helper()
	rng := rand.New(rand.NewPCG(20260921, uint64(sizes.BigFiles)))
	tr := Tree{Sizes: sizes}
	var ids, parents, names []string
	add := func(parent, name string) blobfs.Directory {
		id := blobfs.NewID()
		ids, parents, names = append(ids, id), append(parents, parent), append(names, name)
		return blobfs.Directory{ID: id, ParentID: &parent, Name: name}
	}
	parent := blobfs.RootID
	for i := range sizes.Depth {
		d := add(parent, "l"+strconv.Itoa(i+1))
		tr.Chain = append(tr.Chain, d)
		parent = d.ID
	}
	tr.Big = add(blobfs.RootID, "big")
	// The other directories attach below the chain's first three levels or
	// below one another, so most of them are shallow and the chain stays
	// the deepest path.
	attach := []string{blobfs.RootID, tr.Chain[0].ID, tr.Chain[1].ID, tr.Chain[2].ID}
	for i := range sizes.Directories {
		d := add(attach[rng.IntN(len(attach))], "d"+strconv.Itoa(i))
		tr.Others = append(tr.Others, d)
		if len(attach) < 64 {
			attach = append(attach, d.ID)
		}
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO blobfs_directory (id, parent_id, name) "+
		"SELECT CAST(u.id AS uuid), CAST(u.parent_id AS uuid), u.name FROM unnest($1::text[], $2::text[], $3::text[]) AS u(id, parent_id, name)",
		ids, parents, names); err != nil {
		t.Fatalf("seed directories: %v", err)
	}

	year := time.Now().UTC().Add(-365 * 24 * time.Hour).Truncate(time.Second)
	files := func(dirs []string, n int) {
		perm := rng.Perm(n)
		fids, fdirs, fnames, fkeys, fcreated := make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
		for i := range n {
			id := blobfs.NewID()
			name := fmt.Sprintf("f%06d.txt", perm[i])
			fids[i], fdirs[i], fnames[i], fkeys[i] = id, dirs[rng.IntN(len(dirs))], name, id+"/"+name
			fcreated[i] = year.Add(time.Duration(rng.Int64N(365*24*3600)) * time.Second).Format(time.RFC3339)
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO blobfs_file (id, directory_id, name, status, key, size, content_type, created_at) "+
			"SELECT CAST(u.id AS uuid), CAST(u.directory_id AS uuid), u.name, 'available', u.key, 3, 'text/plain', CAST(u.created_at AS timestamp with time zone) "+
			"FROM unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::text[]) AS u(id, directory_id, name, key, created_at)",
			fids, fdirs, fnames, fkeys, fcreated); err != nil {
			t.Fatalf("seed files: %v", err)
		}
	}
	files([]string{tr.Big.ID}, sizes.BigFiles)
	others := make([]string, 0, len(tr.Others)+len(tr.Chain))
	for _, d := range tr.Chain {
		others = append(others, d.ID)
	}
	for _, d := range tr.Others {
		others = append(others, d.ID)
	}
	files(others, sizes.OtherFiles)
	if _, err := db.ExecContext(ctx, "ANALYZE blobfs_directory, blobfs_file"); err != nil {
		t.Fatalf("analyze: %v", err)
	}
	return tr
}

// HeapBlocks returns the number of distinct heap pages of blobfs_file
// that hold the directory's rows: the pages a read of the whole
// directory must touch.
func HeapBlocks(ctx context.Context, t testing.TB, db *sqlate.DB, directoryID string) int {
	t.Helper()
	return Int(ctx, t, db, "SELECT COUNT(DISTINCT (CAST(CAST(ctid AS text) AS point))[0]) FROM blobfs_file WHERE directory_id = CAST($1 AS uuid)", directoryID)
}

// RelationPages returns the size of the relation, a table or an index, in
// 8 KB pages.
func RelationPages(ctx context.Context, t testing.TB, db *sqlate.DB, relation string) int {
	t.Helper()
	return Int(ctx, t, db, "SELECT pg_relation_size($1) / 8192", relation)
}
