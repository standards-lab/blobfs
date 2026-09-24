package data

import (
	"errors"
	"strings"
	"testing"

	"github.com/standards-lab/sqlate"

	"github.com/standards-lab/blobfs"
)

// driverText is a cause with the driver's text, as PostgreSQL reports a
// unique violation, so a test can prove the text never reaches a
// classified message.
const driverText = `ERROR: duplicate key value violates unique constraint "x" (SQLSTATE 23505)`

// wantClassified fails the test unless got is a blobfs.ViolationError
// carrying want over constraint, with in reachable through errors.As, and
// unless its message is want's text followed by the constraint name with
// none of the driver's text.
func wantClassified(t *testing.T, got error, in *sqlate.ConstraintError, constraint string, want error) {
	t.Helper()
	var ce *sqlate.ConstraintError
	if !errors.As(got, &ce) || ce != in {
		t.Errorf("%q: the ConstraintError is not reachable from %v", constraint, got)
	}
	if !errors.Is(got, want) {
		t.Errorf("%q under %v = %v, want %v", constraint, in.Class, got, want)
	}
	var ve *blobfs.ViolationError
	if !errors.As(got, &ve) || ve.Sentinel != want || ve.Constraint != constraint || ve.Err != in {
		t.Errorf("%q: errors.As gives %+v, want the wrapper over %v and %q", constraint, ve, want, constraint)
	}
	wantMessage := want.Error()
	if constraint != "" {
		wantMessage += " (constraint " + constraint + ")"
	}
	if msg := got.Error(); msg != wantMessage {
		t.Errorf("%q: message = %q, want %q", constraint, msg, wantMessage)
	}
	for _, text := range []string{"SQLSTATE", "duplicate key", "driver"} {
		if strings.Contains(got.Error(), text) {
			t.Errorf("%q: the message %q carries the driver's text %q", constraint, got, text)
		}
	}
}

// TestClassifyWrite is the truth table of the write mapping: each of
// blobfs's constraints maps to its sentinel under the class it reports, the
// two primary keys and the root's partial index included, as a
// blobfs.ViolationError whose message names the sentinel and the constraint
// and keeps the sqlate.ConstraintError reachable. A constraint blobfs does
// not own, a class the constraint does not report (a check violation among
// them), and an error that is no violation pass through unchanged, the
// driver's text included.
func TestClassifyWrite(t *testing.T) {
	cause := errors.New(driverText)
	cases := []struct {
		constraint string
		class      error
		want       error
	}{
		{blobfs.ConstraintPrimaryKeyDirectory, sqlate.ErrUniqueViolation, blobfs.ErrIDTaken},
		{blobfs.ConstraintPrimaryKeyFile, sqlate.ErrUniqueViolation, blobfs.ErrIDTaken},
		{blobfs.ConstraintUniqueDirectoryRoot, sqlate.ErrUniqueViolation, blobfs.ErrRootDirectory},
		{blobfs.ConstraintUniqueDirectoryParentName, sqlate.ErrUniqueViolation, blobfs.ErrNameTaken},
		{blobfs.ConstraintUniqueFileDirectoryName, sqlate.ErrUniqueViolation, blobfs.ErrNameTaken},
		{blobfs.ConstraintForeignKeyDirectoryParent, sqlate.ErrForeignKeyViolation, blobfs.ErrNotFound},
		{blobfs.ConstraintForeignKeyFileDirectory, sqlate.ErrForeignKeyViolation, blobfs.ErrNotFound},
		{"uq_bookmark_active", sqlate.ErrUniqueViolation, nil},
		{blobfs.ConstraintUniqueDirectoryRoot, sqlate.ErrCheckViolation, nil},
		{"blobfs_cc_directory_root_name", sqlate.ErrCheckViolation, nil},
	}
	for _, c := range cases {
		in := &sqlate.ConstraintError{Constraint: c.constraint, Class: c.class, Err: cause}
		got := classifyWrite(in)
		if c.want == nil {
			if got != in || !strings.Contains(got.Error(), "SQLSTATE 23505") {
				t.Errorf("%s under %v = %v, want the error unchanged", c.constraint, c.class, got)
			}
			continue
		}
		wantClassified(t, got, in, c.constraint, c.want)
	}
	if got := classifyWrite(cause); got != cause {
		t.Errorf("a plain error = %v, want it unchanged", got)
	}
}

// TestClassifyDelete is the truth table of the delete mapping: blobfs's
// two foreign keys into blobfs_directory mean not empty, a foreign key
// blobfs does not own (a consumer's, whatever its name) means referenced,
// and both are a blobfs.ViolationError whose message names the sentinel
// and the constraint and keeps the sqlate.ConstraintError reachable. A
// unique or check violation, blobfs's own name under a class it does not
// report there, and an error that is no violation pass through unchanged.
func TestClassifyDelete(t *testing.T) {
	cause := errors.New(driverText)
	cases := []struct {
		constraint string
		class      error
		want       error
	}{
		{blobfs.ConstraintForeignKeyDirectoryParent, sqlate.ErrForeignKeyViolation, blobfs.ErrNotEmpty},
		{blobfs.ConstraintForeignKeyFileDirectory, sqlate.ErrForeignKeyViolation, blobfs.ErrNotEmpty},
		{"fk_bookmark_file", sqlate.ErrForeignKeyViolation, blobfs.ErrReferenced},
		{"fk_directory_owner_directory", sqlate.ErrForeignKeyViolation, blobfs.ErrReferenced},
		{"", sqlate.ErrForeignKeyViolation, blobfs.ErrReferenced},
		{blobfs.ConstraintForeignKeyDirectoryParent, sqlate.ErrUniqueViolation, nil},
		{blobfs.ConstraintUniqueFileDirectoryName, sqlate.ErrUniqueViolation, nil},
		{"cc_something", sqlate.ErrCheckViolation, nil},
	}
	for _, c := range cases {
		in := &sqlate.ConstraintError{Constraint: c.constraint, Class: c.class, Err: cause}
		got := classifyDelete(in)
		if c.want == nil {
			if got != in {
				t.Errorf("%q under %v = %v, want the error unchanged", c.constraint, c.class, got)
			}
			continue
		}
		wantClassified(t, got, in, c.constraint, c.want)
		if c.want == blobfs.ErrReferenced && errors.Is(got, blobfs.ErrNotEmpty) {
			t.Errorf("%q: a consumer's key classified as not empty", c.constraint)
		}
	}
	if got := classifyDelete(cause); got != cause {
		t.Errorf("a plain error = %v, want it unchanged", got)
	}
}
