package blobfs_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/standards-lab/blobfs"
)

// TestViolationError proves the wrapper's contract over a unique, a
// foreign-key, and a check constraint: the message is the sentinel's text
// followed by the constraint name and never the driver's text, errors.Is
// matches the sentinel and the cause, and errors.As reaches the wrapper
// with both fields. Without a constraint name the message is the
// sentinel's alone, and without a cause the sentinel is still matched.
func TestViolationError(t *testing.T) {
	for _, tc := range []struct {
		sentinel   error
		constraint string
		driver     string
		want       string
	}{
		{blobfs.ErrNameTaken, blobfs.ConstraintUniqueDirectoryParentName, `ERROR: duplicate key value violates unique constraint "blobfs_uq_directory_parent_name" (SQLSTATE 23505)`, "blobfs: name taken (constraint blobfs_uq_directory_parent_name)"},
		{blobfs.ErrNotFound, blobfs.ConstraintForeignKeyFileDirectory, `ERROR: insert or update on table "blobfs_file" violates foreign key constraint "blobfs_fk_file_directory" (SQLSTATE 23503)`, "blobfs: not found (constraint blobfs_fk_file_directory)"},
		{blobfs.ErrRootDirectory, "blobfs_cc_directory_root_name", `ERROR: new row for relation "blobfs_directory" violates check constraint "blobfs_cc_directory_root_name" (SQLSTATE 23514)`, "blobfs: the root directory (constraint blobfs_cc_directory_root_name)"},
	} {
		cause := errors.New(tc.driver)
		var err error = &blobfs.ViolationError{Sentinel: tc.sentinel, Constraint: tc.constraint, Err: cause}
		if got := err.Error(); got != tc.want {
			t.Errorf("%s: message = %q, want %q", tc.constraint, got, tc.want)
		}
		for _, text := range []string{"SQLSTATE", "duplicate key", "violates"} {
			if strings.Contains(err.Error(), text) {
				t.Errorf("%s: the message %q carries the driver's text %q", tc.constraint, err, text)
			}
		}
		if !errors.Is(err, tc.sentinel) {
			t.Errorf("%s: errors.Is(%v, %v) is false", tc.constraint, err, tc.sentinel)
		}
		if !errors.Is(err, cause) {
			t.Errorf("%s: the cause is not reachable from %v", tc.constraint, err)
		}
		var ve *blobfs.ViolationError
		if !errors.As(err, &ve) || ve.Sentinel != tc.sentinel || ve.Constraint != tc.constraint || ve.Err != cause {
			t.Errorf("%s: errors.As gives %+v", tc.constraint, ve)
		}
	}

	unnamed := &blobfs.ViolationError{Sentinel: blobfs.ErrReferenced, Err: errors.New("driver")}
	if got := unnamed.Error(); got != blobfs.ErrReferenced.Error() {
		t.Errorf("without a constraint name the message is %q, want the sentinel's %q", got, blobfs.ErrReferenced)
	}
	bare := &blobfs.ViolationError{Sentinel: blobfs.ErrNotEmpty, Constraint: blobfs.ConstraintForeignKeyDirectoryParent}
	if !errors.Is(bare, blobfs.ErrNotEmpty) || len(bare.Unwrap()) != 1 {
		t.Errorf("without a cause Unwrap = %v, want the sentinel alone", bare.Unwrap())
	}
}
