package blobfs_test

import (
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/standards-lab/blobfs"
)

var statuses = []blobfs.Status{blobfs.StatusPending, blobfs.StatusAvailable, blobfs.StatusDeleting}

// TestTransitions is the truth table: every pair of statuses, allowed or
// refused, and the two errors a refusal matches.
func TestTransitions(t *testing.T) {
	allowed := map[[2]blobfs.Status]bool{
		{blobfs.StatusPending, blobfs.StatusAvailable}:  true,
		{blobfs.StatusPending, blobfs.StatusDeleting}:   true,
		{blobfs.StatusAvailable, blobfs.StatusDeleting}: true,
		{blobfs.StatusDeleting, blobfs.StatusDeleting}:  true,
	}
	for _, from := range statuses {
		for _, to := range statuses {
			want := allowed[[2]blobfs.Status{from, to}]
			err := blobfs.Transition(from, to)
			if want {
				if err != nil {
					t.Errorf("Transition(%s, %s) = %v, want nil", from, to, err)
				}
				continue
			}
			if !errors.Is(err, blobfs.ErrInvalidTransition) {
				t.Errorf("Transition(%s, %s) = %v, want ErrInvalidTransition", from, to, err)
			}
			if got := errors.Is(err, blobfs.ErrDeleting); got != (from == blobfs.StatusDeleting) {
				t.Errorf("errors.Is(Transition(%s, %s), ErrDeleting) = %v", from, to, got)
			}
		}
	}
	if blobfs.Transition("", blobfs.StatusPending) == nil || blobfs.Transition(blobfs.StatusPending, "") == nil {
		t.Error("a transition involving an unknown status was allowed")
	}
}

func TestStatusMutable(t *testing.T) {
	for _, s := range statuses {
		if got, want := s.Mutable(), s != blobfs.StatusDeleting; got != want {
			t.Errorf("%s.Mutable() = %v, want %v", s, got, want)
		}
	}
}

// TestStatusBindsAsText checks that database/sql's default converter binds a
// Status as its text without a Valuer, which is what lets the entities scan
// and bind through sqlate's tag mapper with no sqlate import here.
func TestStatusBindsAsText(t *testing.T) {
	v, err := driver.DefaultParameterConverter.ConvertValue(blobfs.StatusDeleting)
	if err != nil {
		t.Fatalf("ConvertValue: %v", err)
	}
	if v != "deleting" {
		t.Errorf("ConvertValue(StatusDeleting) = %#v, want \"deleting\"", v)
	}
}

func TestDirectoryStatusMutable(t *testing.T) {
	for _, c := range []struct {
		status  blobfs.DirectoryStatus
		mutable bool
	}{
		{blobfs.DirectoryStatusActive, true},
		{blobfs.DirectoryStatusDeleting, false},
	} {
		if got := c.status.Mutable(); got != c.mutable {
			t.Errorf("%s.Mutable() = %v, want %v", c.status, got, c.mutable)
		}
	}
}

// TestDirectoryStatusBindsAsText checks that a DirectoryStatus binds as its
// text without a Valuer, as a Status does.
func TestDirectoryStatusBindsAsText(t *testing.T) {
	v, err := driver.DefaultParameterConverter.ConvertValue(blobfs.DirectoryStatusDeleting)
	if err != nil {
		t.Fatalf("ConvertValue: %v", err)
	}
	if v != "deleting" {
		t.Errorf("ConvertValue(DirectoryStatusDeleting) = %#v, want \"deleting\"", v)
	}
}
