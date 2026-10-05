package blobfs_test

import (
	"errors"
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
