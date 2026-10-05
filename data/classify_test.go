package data_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/sqltest"

	"github.com/standards-lab/blobfs"
)

// driverText is a cause with the driver's text, as PostgreSQL reports a
// unique violation, so a test can prove the text never reaches a
// classified message.
const driverText = `ERROR: duplicate key value violates unique constraint "x" (SQLSTATE 23505)`

// wantClassified fails the test unless got is a blobfs.ViolationError
// carrying want over constraint, with in reachable through errors.As, and
// unless its message ends in want's text followed by the constraint name,
// with none of the driver's text.
func wantClassified(t *testing.T, what string, got error, in *sqlate.ConstraintError, constraint string, want error) {
	t.Helper()
	var ce *sqlate.ConstraintError
	if !errors.As(got, &ce) || ce != in {
		t.Errorf("%s under %q: the ConstraintError is not reachable from %v", what, constraint, got)
	}
	if !errors.Is(got, want) {
		t.Errorf("%s under %q as %v = %v, want %v", what, constraint, in.Class, got, want)
	}
	var ve *blobfs.ViolationError
	if !errors.As(got, &ve) || ve.Sentinel != want || ve.Constraint != constraint || !errors.Is(ve.Err, in) {
		t.Errorf("%s under %q: errors.As gives %+v, want the wrapper over %v and %q", what, constraint, ve, want, constraint)
	}
	wantMessage := want.Error()
	if constraint != "" {
		wantMessage += " (constraint " + constraint + ")"
	}
	if msg := got.Error(); !strings.HasSuffix(msg, wantMessage) {
		t.Errorf("%s under %q: message = %q, want it to end in %q", what, constraint, msg, wantMessage)
	}
	for _, text := range []string{"SQLSTATE", "duplicate key"} {
		if strings.Contains(got.Error(), text) {
			t.Errorf("%s under %q: the message %q carries the driver's text %q", what, constraint, got, text)
		}
	}
}

// wantUnclassified fails the test unless got carries in, or the plain
// error in stands for, unchanged: no ViolationError, the driver's text
// kept.
func wantUnclassified(t *testing.T, what string, got, in error) {
	t.Helper()
	var ve *blobfs.ViolationError
	if !errors.Is(got, in) || errors.As(got, &ve) || !strings.Contains(got.Error(), "SQLSTATE 23505") {
		t.Errorf("%s = %v, want %v unclassified", what, got, in)
	}
}

// TestWriteClassification is the truth table of a write's mapping, run
// through a directory's create: blobfs's own unique and foreign-key
// constraints become their sentinels, and a consumer's constraint, a
// constraint under another class, and an error that is no constraint's
// are returned as they came.
func TestWriteClassification(t *testing.T) {
	ctx := context.Background()
	cause := errors.New(driverText)
	for _, c := range []struct {
		constraint string
		class      error
		want       error // nil for unclassified
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
	} {
		in := &sqlate.ConstraintError{Constraint: c.constraint, Class: c.class, Err: cause}
		s, db, _ := openStore(t, fallback, sqltest.Response{Err: in})
		_, err := s.Directories.Create(ctx, db, blobfs.RootID, "docs")
		if c.want == nil {
			wantUnclassified(t, "Create under "+c.constraint, err, in)
			continue
		}
		wantClassified(t, "Create", err, in, c.constraint, c.want)
	}
	s, db, _ := openStore(t, fallback, sqltest.Response{Err: cause})
	_, err := s.Directories.Create(ctx, db, blobfs.RootID, "docs")
	wantUnclassified(t, "Create under a plain error", err, cause)
}

// TestDeleteClassification is the truth table of a delete's mapping, run
// through a directory's delete: blobfs's own foreign keys are the
// children that keep a directory, any other foreign key, a nameless one
// included, is a consumer's reference, and a violation under another
// class or an error that is no constraint's is returned as it came.
func TestDeleteClassification(t *testing.T) {
	ctx := context.Background()
	cause := errors.New(driverText)
	for _, c := range []struct {
		constraint string
		class      error
		want       error // nil for unclassified
	}{
		{blobfs.ConstraintForeignKeyDirectoryParent, sqlate.ErrForeignKeyViolation, blobfs.ErrNotEmpty},
		{blobfs.ConstraintForeignKeyFileDirectory, sqlate.ErrForeignKeyViolation, blobfs.ErrNotEmpty},
		{"fk_bookmark_file", sqlate.ErrForeignKeyViolation, blobfs.ErrReferenced},
		{"fk_directory_owner_directory", sqlate.ErrForeignKeyViolation, blobfs.ErrReferenced},
		{"", sqlate.ErrForeignKeyViolation, blobfs.ErrReferenced},
		{blobfs.ConstraintForeignKeyDirectoryParent, sqlate.ErrUniqueViolation, nil},
		{blobfs.ConstraintUniqueFileDirectoryName, sqlate.ErrUniqueViolation, nil},
		{"cc_something", sqlate.ErrCheckViolation, nil},
	} {
		in := &sqlate.ConstraintError{Constraint: c.constraint, Class: c.class, Err: cause}
		s, db, _ := openStore(t, fallback, sqltest.Response{Err: in})
		err := s.Directories.Delete(ctx, db, "D")
		if c.want == nil {
			wantUnclassified(t, "Delete under "+c.constraint, err, in)
			continue
		}
		wantClassified(t, "Delete", err, in, c.constraint, c.want)
		if c.want == blobfs.ErrReferenced && errors.Is(err, blobfs.ErrNotEmpty) {
			t.Errorf("Delete under %q: a consumer's key classified as not empty", c.constraint)
		}
	}
	s, db, _ := openStore(t, fallback, sqltest.Response{Err: cause})
	wantUnclassified(t, "Delete under a plain error", s.Directories.Delete(ctx, db, "D"), cause)
}
