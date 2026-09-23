package data_test

import (
	"context"
	"database/sql/driver"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/standards-lab/sqlate/sqltest"

	"github.com/standards-lab/blobfs"
)

// TestFindByPathChecks proves the path checks run before any SQL: a
// leading slash, which the library does not read as a path from the root,
// an empty segment, a trailing slash, and a segment ValidateName refuses
// (., .., an over-long name) are ErrInvalidPath, the refused segment
// matches ErrInvalidName as well, and nothing reaches the driver.
func TestFindByPathChecks(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback)
	long := strings.Repeat("x", blobfs.MaxNameLength+1)
	for _, path := range []string{"/", "/a", "/a/b", "a//b", "a/", ".", "..", "a/..", "../a", "./a", "a/.", long, "a/" + long} {
		if _, err := s.Directories.FindByPath(ctx, db, blobfs.RootID, path); !errors.Is(err, blobfs.ErrInvalidPath) {
			t.Errorf("FindByPath(%q) = %v, want ErrInvalidPath", path, err)
		}
	}
	for _, path := range []string{".", "..", "a/..", "a/", long} {
		if _, err := s.Directories.FindByPath(ctx, db, blobfs.RootID, path); !errors.Is(err, blobfs.ErrInvalidName) {
			t.Errorf("FindByPath(%q) = %v, want ErrInvalidName as well", path, err)
		}
	}
	if calls := rec.Calls(); len(calls) != 0 {
		t.Errorf("path checks reached the driver with %d calls", len(calls))
	}
}

// TestFindByPathWalks proves the baseline's reads: the start by id and
// then one directory_by_name read per segment, each bound to the directory
// the previous read returned and the normalized name; the empty path is
// the start read alone; a start that no directory holds is ErrNotFound
// with no further read and no prefix; and a missing segment is ErrNotFound
// naming the prefix that failed, the walk stopping there.
func TestFindByPathWalks(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, fallback,
		// b/c/café from A: the start, then three children.
		childResponse("A", "a"),
		directoryResponse("B", "A", "b", 1),
		directoryResponse("C", "B", "c", 1),
		directoryResponse("D", "C", nfcName, 1),
		// The empty path from A: the start alone.
		childResponse("A", "a"),
		// A start no directory holds.
		noDirectory(),
		// b/missing/deeper from A: the start, b, then no row.
		childResponse("A", "a"),
		directoryResponse("B", "A", "b", 1),
		noDirectory(),
	)
	d, err := s.Directories.FindByPath(ctx, db, "A", "b/c/"+nfdName)
	if err != nil || d.ID != "D" || d.Name != nfcName {
		t.Fatalf("FindByPath(A, b/c/café) = %+v, %v, want D", d, err)
	}
	if d, err := s.Directories.FindByPath(ctx, db, "A", ""); err != nil || d.ID != "A" {
		t.Errorf("FindByPath(A, \"\") = %+v, %v, want the start", d, err)
	}
	_, err = s.Directories.FindByPath(ctx, db, "F", "b")
	if !errors.Is(err, blobfs.ErrNotFound) || strings.Contains(err.Error(), " at ") {
		t.Errorf("FindByPath(F, b) = %v, want ErrNotFound with no prefix", err)
	}
	_, err = s.Directories.FindByPath(ctx, db, "A", "b/missing/deeper")
	if !errors.Is(err, blobfs.ErrNotFound) || !strings.HasSuffix(err.Error(), "from A at b/missing: "+blobfs.ErrNotFound.Error()) {
		t.Errorf("FindByPath(A, b/missing/deeper) = %v, want ErrNotFound naming the failing prefix", err)
	}

	calls := rec.Calls()
	want := [][]any{
		{"A"}, {"A", "b"}, {"B", "c"}, {"C", nfcName},
		{"A"},
		{"F"},
		{"A"}, {"A", "b"}, {"B", "missing"},
	}
	if len(calls) != len(want) {
		t.Fatalf("the resolutions ran %d queries, want %d", len(calls), len(want))
	}
	for i, args := range want {
		if !slices.Equal(calls[i].Args, args) {
			t.Errorf("query %d bound %v, want %v", i, calls[i].Args, args)
		}
	}
	if !strings.HasSuffix(calls[1].SQL, "WHERE d.parent_id = CAST($1 AS uuid) AND d.name = $2") {
		t.Errorf("a step is not directory_by_name:\n%s", calls[1].SQL)
	}
}

// TestPathComposes proves the path is composed from the ancestor chain
// root first: the root alone is /, a chain is the names below the root
// joined by slashes, no chain is ErrNotFound, and a chain that does not
// reach the root is refused. The statement runs once per call, whatever
// the depth.
func TestPathComposes(t *testing.T) {
	ctx := context.Background()
	cols := []string{"parent_id", "name"}
	s, db, rec := openStore(t, fallback,
		sqltest.Response{Columns: cols, Rows: [][]driver.Value{{nil, "/"}}},
		sqltest.Response{Columns: cols, Rows: [][]driver.Value{{nil, "/"}, {blobfs.RootID, "a"}, {"a", "b"}, {"b", nfcName}}},
		sqltest.Response{Columns: cols},
		sqltest.Response{Columns: cols, Rows: [][]driver.Value{{"x", "a"}}},
	)
	for i, want := range []string{"/", "/a/b/" + nfcName} {
		got, err := s.Directories.Path(ctx, db, "id")
		if err != nil || got != want {
			t.Errorf("Path %d = %q, %v, want %q", i, got, err, want)
		}
	}
	if _, err := s.Directories.Path(ctx, db, "missing"); !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("Path of a missing directory = %v, want ErrNotFound", err)
	}
	if _, err := s.Directories.Path(ctx, db, "detached"); err == nil || !strings.Contains(err.Error(), "does not reach the root") {
		t.Errorf("Path of a detached chain = %v, want the refusal", err)
	}
	if n := len(rec.SQL(sqltest.OpQuery)); n != 4 {
		t.Errorf("four paths ran %d queries, want 4 (one recursive statement each)", n)
	}
}
