package data_test

import (
	"context"
	"slices"
	"testing"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/sqltest"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/blobfs/data"
)

// TestCreateOptionsMints checks an empty CreateOptions.ID mints a fresh
// canonical id for each insert, for directories and files alike.
func TestCreateOptionsMints(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single, childResponse("A", "a"), childResponse("B", "b"))
	for _, n := range []string{"a", "b"} {
		if _, err := s.Directories.Create(ctx, db, blobfs.RootID, n, data.CreateOptions{}); err != nil {
			t.Fatalf("Create(%q) with no id: %v", n, err)
		}
	}
	s, db, frec := openStore(t, single, protocolRow(blobfs.StatusPending, 1))
	if _, err := s.Files.Create(ctx, db, accepting{}, protocolDir, "report.txt", "text/plain", data.CreateOptions{}); err != nil {
		t.Fatalf("Files.Create with no id: %v", err)
	}
	calls := append(rec.Calls(), frec.Calls()...)
	var minted []any
	for _, call := range calls {
		minted = append(minted, call.Args[0])
	}
	for _, id := range minted {
		s, ok := id.(string)
		if parsed, err := blobfs.ParseID(s); !ok || err != nil || parsed != s {
			t.Errorf("the insert bound %v, want a minted id in canonical form", id)
		}
	}
	if len(minted) != 3 || minted[0] == minted[1] {
		t.Errorf("the inserts bound %v, want a fresh id each", minted)
	}
}

// TestCreateOptionsEnsureFoundKeepsItsID checks a row an Ensure finds is
// returned under its own id, not the one CreateOptions supplied.
func TestCreateOptionsEnsureFoundKeepsItsID(t *testing.T) {
	ctx := context.Background()
	s, db, rec := openStore(t, single, childResponse("D", "docs"))
	d, created, err := s.Directories.Ensure(ctx, db, blobfs.RootID, "docs", data.CreateOptions{ID: blobfs.NewID()})
	if err != nil || created || d.ID != "D" {
		t.Errorf("Ensure of a held name = %+v, %v, %v; want the row found under its own id", d, created, err)
	}
	if got := ops(rec); got != "query" {
		t.Errorf("ops = %q, want the lookup alone", got)
	}

	s, db, rec = openStore(t, single, protocolRow(blobfs.StatusAvailable, 2))
	f, outcome, err := s.Files.Ensure(ctx, db, accepting{}, protocolDir, "report.txt", "text/plain", data.CreateOptions{ID: blobfs.NewID()})
	if err != nil || outcome != data.WritePresent || f.ID != protocolFile {
		t.Errorf("Files.Ensure of a held name = %+v, %v, %v; want the row found under its own id", f, outcome, err)
	}
	if got := ops(rec); got != "query" {
		t.Errorf("ops = %q, want the lookup alone", got)
	}
}

// TestVersionOptions checks each guarded call binds no version for the
// zero VersionOptions, which guards nothing, and binds a set Version.
func TestVersionOptions(t *testing.T) {
	ctx := context.Background()
	objects := newObjectStore()
	for _, c := range []struct {
		name string
		run  func(*data.Store, *sqlate.DB, data.VersionOptions)
	}{
		{"Files.Hold", func(s *data.Store, db *sqlate.DB, o data.VersionOptions) {
			_ = s.Files.Hold(ctx, begin(t, db), "F", o)
		}},
		{"Files.Delete", func(s *data.Store, db *sqlate.DB, o data.VersionOptions) {
			_, _ = s.Files.Delete(ctx, begin(t, db), "F", o)
		}},
		{"Directories.Delete", func(s *data.Store, db *sqlate.DB, o data.VersionOptions) {
			_ = s.Directories.Delete(ctx, db, "D", o)
		}},
		{"Directories.MarkDeleting", func(s *data.Store, db *sqlate.DB, o data.VersionOptions) {
			_, _ = s.Directories.MarkDeleting(ctx, begin(t, db), "D", o)
		}},
		{"Store.RemoveFileID", func(s *data.Store, db *sqlate.DB, o data.VersionOptions) {
			_ = s.RemoveFileID(ctx, db, objects, "F", o)
		}},
		{"Store.RemoveFile", func(s *data.Store, db *sqlate.DB, o data.VersionOptions) {
			_ = s.RemoveFile(ctx, db, objects, func(*sqlate.Tx) (string, error) { return "F", nil }, data.RemoveOptions{Version: o.Version})
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, db, rec := openStore(t, single)
			c.run(s, db, data.VersionOptions{Version: 7})
			guarded := slices.IndexFunc(rec.Calls(), func(call sqltest.Call) bool { return slices.Contains(call.Args, any(int64(7))) })
			if guarded < 0 {
				t.Fatalf("the call at version 7 ran %v, want the version bound", rec.Calls())
			}

			s, db, rec = openStore(t, single)
			c.run(s, db, data.VersionOptions{})
			calls := rec.Calls()
			if len(calls) <= guarded {
				t.Fatalf("the unguarded call ran %v, want the guarded statement", calls)
			}
			if got := calls[guarded].Args; !slices.Contains(got, nil) || slices.ContainsFunc(got, func(a any) bool { _, ok := a.(int64); return ok }) {
				t.Errorf("the unguarded call bound %v, want no version", got)
			}
		})
	}
}
