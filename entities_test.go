package blobfs_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"uuid"

	"github.com/standards-lab/blobfs"
)

func TestNewID(t *testing.T) {
	seen := make(map[string]bool)
	for range 1000 {
		id := blobfs.NewID()
		if seen[id] {
			t.Fatalf("NewID repeated %q", id)
		}
		seen[id] = true
		u, err := uuid.Parse(id)
		if err != nil {
			t.Fatalf("uuid.Parse(%q): %v", id, err)
		}
		if version := u[6] >> 4; version != 7 {
			t.Fatalf("NewID() = %q is version %d, want 7", id, version)
		}
		if len(id) != 36 {
			t.Fatalf("NewID() = %q has length %d, want the 36-character canonical form", id, len(id))
		}
		if id == blobfs.RootID {
			t.Fatalf("NewID() minted the root's id")
		}
	}
}

// TestRootID fixes the root's well-known id: the nil UUID in canonical
// form, which parses as a uuid and binds to a uuid column as text.
func TestRootID(t *testing.T) {
	u, err := uuid.Parse(blobfs.RootID)
	if err != nil {
		t.Fatalf("uuid.Parse(RootID): %v", err)
	}
	if u != (uuid.UUID{}) {
		t.Errorf("RootID = %q, want the nil UUID", blobfs.RootID)
	}
	if got := u.String(); got != blobfs.RootID {
		t.Errorf("RootID %q is not in canonical form (%q)", blobfs.RootID, got)
	}
}

// TestParseID proves a caller-supplied id is returned in the canonical
// form NewID mints, whatever accepted form it came in, so the row and its
// key agree; and that the nil UUID, the empty string, and text that is no
// UUID are refused as an IDError matching ErrInvalidID, with the id and
// the reason in the message.
func TestParseID(t *testing.T) {
	minted := blobfs.NewID()
	for _, form := range []string{minted, strings.ToUpper(minted), "{" + minted + "}", "urn:uuid:" + minted, strings.ReplaceAll(minted, "-", "")} {
		got, err := blobfs.ParseID(form)
		if err != nil || got != minted {
			t.Errorf("ParseID(%q) = %q, %v; want the canonical form %q", form, got, err, minted)
		}
	}
	for _, id := range []string{blobfs.RootID, "{" + blobfs.RootID + "}", "", "not-a-uuid", minted + "0"} {
		got, err := blobfs.ParseID(id)
		var ie *blobfs.IDError
		if !errors.Is(err, blobfs.ErrInvalidID) || !errors.As(err, &ie) || ie.ID != id || ie.Reason == "" || got != "" {
			t.Errorf("ParseID(%q) = %q, %v; want an IDError carrying the id and a reason", id, got, err)
		}
		if err != nil && !strings.Contains(err.Error(), id) {
			t.Errorf("ParseID(%q) = %q does not name the id", id, err)
		}
	}
	if _, err := blobfs.ParseID(blobfs.RootID); err == nil || !strings.Contains(err.Error(), "root") {
		t.Errorf("ParseID(RootID) = %v, want a reason that names the root", err)
	}
}

// TestIsRoot fixes what makes a directory the root: a nil parent, and
// nothing else.
func TestIsRoot(t *testing.T) {
	parent, name := "p", "n"
	if !(blobfs.Directory{ID: blobfs.RootID}).IsRoot() {
		t.Error("a directory with no parent is not the root")
	}
	if (blobfs.Directory{ID: blobfs.RootID, ParentID: &parent, Name: name}).IsRoot() {
		t.Error("a directory with a parent is the root")
	}
}

// TestEntityTags fixes the scan and binding contract: every exported field
// of Directory and File carries a json tag naming its column, and the
// column the root leaves NULL (a directory's parent_id) and the columns a
// store fills late (a file's size and etag) are pointers, so a NULL scans
// as nil and a nil binds as NULL. A directory's name is never NULL: the
// root's is /.
func TestEntityTags(t *testing.T) {
	nullable := map[string]bool{
		"Directory.ParentID": true,
		"File.Size":          true,
		"File.ETag":          true,
	}
	for _, v := range []any{blobfs.Directory{}, blobfs.File{}} {
		rt := reflect.TypeOf(v)
		for f := range rt.Fields() {
			tag := f.Tag.Get("json")
			if tag == "" || tag != strings.ToLower(tag) {
				t.Errorf("%s.%s has json tag %q, want a lowercase column name", rt.Name(), f.Name, tag)
			}
			key := rt.Name() + "." + f.Name
			if got := f.Type.Kind() == reflect.Pointer; got != nullable[key] {
				t.Errorf("%s is a pointer: %v, want %v", key, got, nullable[key])
			}
		}
	}
}

// TestSentinels fixes that each sentinel is distinct and matches only
// itself, so package data's mapping cannot alias two outcomes.
func TestSentinels(t *testing.T) {
	sentinels := []error{
		blobfs.ErrNotFound,
		blobfs.ErrNameTaken,
		blobfs.ErrInvalidName,
		blobfs.ErrInvalidPath,
		blobfs.ErrInvalidKey,
		blobfs.ErrNotEmpty,
		blobfs.ErrInvalidTransition,
		blobfs.ErrDeleting,
		blobfs.ErrCycle,
		blobfs.ErrRootDirectory,
		blobfs.ErrInvalidID,
		blobfs.ErrIDTaken,
	}
	for i, a := range sentinels {
		for j, b := range sentinels {
			if got := errors.Is(a, b); got != (i == j) {
				t.Errorf("errors.Is(%v, %v) = %v", a, b, got)
			}
		}
		if a.Error() == "" {
			t.Errorf("sentinel %d has an empty message", i)
		}
	}
}
