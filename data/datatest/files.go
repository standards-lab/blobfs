package datatest

import (
	"errors"
	"testing"

	"github.com/standards-lab/sqlate/query"

	"github.com/standards-lab/blobfs"
)

// files checks the Files handle's reads and the file move.
func (s *suite) files(t *testing.T) {
	t.Run("Find", s.findFile)
	t.Run("Move", s.moveFile)
	t.Run("MoveRefusals", s.moveFileRefusals)
}

// findFile checks Find and FindByName: a row read whatever its status, a
// name found in either spelling, a directory of the same name not found,
// and a missing row, a missing directory, and a refused name.
func (s *suite) findFile(t *testing.T) {
	dir := s.mkdir(t, "find-"+t.Name())
	for _, status := range []blobfs.Status{blobfs.StatusPending, blobfs.StatusAvailable, blobfs.StatusDeleting} {
		id := s.insertFile(t, dir.ID, status.String()+".txt", status)
		got := s.file(t, id)
		if got.ID != id || got.DirectoryID != dir.ID || got.Status != status || got.Key != id+"/"+status.String()+".txt" || got.Version != 1 {
			t.Errorf("Find of the %s row = %+v", status, got)
		}
		if byName, err := s.store.Files.FindByName(s.ctx, s.db, dir.ID, status.String()+".txt"); err != nil || !equalFile(byName, got) {
			t.Errorf("FindByName of the %s row = %+v, %v, want %+v", status, byName, err, got)
		}
	}
	cafe := s.insertFile(t, dir.ID, composed, blobfs.StatusAvailable)
	for _, spelling := range []string{composed, decomposed} {
		if f, err := s.store.Files.FindByName(s.ctx, s.db, dir.ID, spelling); err != nil || f.ID != cafe {
			t.Errorf("FindByName(%q) = %+v, %v, want the row stored composed", spelling, f, err)
		}
	}
	s.mkdirUnder(t, dir.ID, "directory-only")
	if _, err := s.store.Files.FindByName(s.ctx, s.db, dir.ID, "directory-only"); !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("FindByName of a directory's name = %v, want ErrNotFound", err)
	}
	if _, err := s.store.Files.Find(s.ctx, s.db, blobfs.NewID()); !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("Find(missing) = %v, want ErrNotFound", err)
	}
	if _, err := s.store.Files.FindByName(s.ctx, s.db, blobfs.NewID(), "a.txt"); !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("FindByName in a missing directory = %v, want ErrNotFound", err)
	}
	var nameErr *blobfs.NameError
	if _, err := s.store.Files.FindByName(s.ctx, s.db, dir.ID, "a/b"); !errors.As(err, &nameErr) {
		t.Errorf("FindByName(a/b) = %v, want a NameError", err)
	}
}

// moveFile checks a successful file move against the baseline: a move into
// another directory and a rename each advance the version once, stamp
// updated_at, and leave the key as it was; the returned row is the row
// the database holds; a decomposed name is stored composed; a pending row
// moves; and a directory of the same name in the target is no conflict.
func (s *suite) moveFile(t *testing.T) {
	src := s.mkdir(t, "move-src-"+t.Name())
	dst := s.mkdir(t, "move-dst-"+t.Name())
	id := s.insertFile(t, src.ID, "a.txt", blobfs.StatusAvailable)
	before := s.file(t, id)
	moved, err := s.store.Files.Move(s.ctx, s.db, id, dst.ID, "a.txt", before.Version)
	if err != nil {
		t.Fatalf("Move into dst: %v", err)
	}
	if moved.DirectoryID != dst.ID || moved.Name != "a.txt" || moved.Version != before.Version+1 || moved.Key != before.Key ||
		!moved.UpdatedAt.After(before.UpdatedAt) || !moved.CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("the moved row is %+v, want it in dst at the next version with the key %q", moved, before.Key)
	}
	if stored := s.file(t, id); !equalFile(moved, stored) {
		t.Errorf("Move returned\n%+v\nbut the database holds\n%+v", moved, stored)
	}
	if _, err := s.store.Files.FindByName(s.ctx, s.db, src.ID, "a.txt"); !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("the file is still found in src: %v", err)
	}
	renamed, err := s.store.Files.Move(s.ctx, s.db, id, dst.ID, decomposed, moved.Version)
	if err != nil {
		t.Fatalf("Move as a rename: %v", err)
	}
	if renamed.Name != composed || renamed.Key != before.Key || renamed.Version != moved.Version+1 {
		t.Errorf("the renamed row is %+v, want the composed name and the key unchanged", renamed)
	}

	baseID := s.insertFile(t, src.ID, "b.txt", blobfs.StatusAvailable)
	base, err := s.baseline.Files.Move(s.ctx, s.db, baseID, dst.ID, "b.txt", 1)
	if err != nil {
		t.Fatalf("the baseline's Move: %v", err)
	}
	if !sameFileShape(moved, base) {
		t.Errorf("the moved row\n%+v\ndiffers in shape from the baseline's\n%+v", moved, base)
	}

	pending := s.insertFile(t, src.ID, "pending.txt", blobfs.StatusPending)
	if f, err := s.store.Files.Move(s.ctx, s.db, pending, dst.ID, "pending.txt", 1); err != nil || f.Status != blobfs.StatusPending || f.DirectoryID != dst.ID {
		t.Errorf("Move of a pending row = %+v, %v; want it moved and still pending", f, err)
	}
	s.mkdirUnder(t, dst.ID, "shared")
	shared := s.insertFile(t, src.ID, "shared", blobfs.StatusAvailable)
	if _, err := s.store.Files.Move(s.ctx, s.db, shared, dst.ID, "shared", 1); err != nil {
		t.Errorf("Move under the name of a directory = %v, want the move to succeed", err)
	}
}

// moveFileRefusals checks the file move's refusals against the baseline,
// each leaving the row unchanged: a name held in the target by a row of
// any status, a deleting one included, is ErrNameTaken under the unique
// constraint; a missing directory is ErrNotFound under the foreign key; a
// stale version is ErrVersionMismatch; a deleting row is ErrDeleting; a
// missing file is ErrNotFound; and a refused name is a NameError.
func (s *suite) moveFileRefusals(t *testing.T) {
	src := s.mkdir(t, "refuse-src-"+t.Name())
	dst := s.mkdir(t, "refuse-dst-"+t.Name())
	mover := s.insertFile(t, src.ID, "mover.txt", blobfs.StatusAvailable)
	s.insertFile(t, dst.ID, "held.txt", blobfs.StatusAvailable)
	s.insertFile(t, dst.ID, "held-deleting.txt", blobfs.StatusDeleting)
	deleting := s.insertFile(t, src.ID, "deleting.txt", blobfs.StatusDeleting)
	missing := blobfs.NewID()
	for _, c := range []struct {
		name       string
		id         string
		dir        string
		file       string
		version    int64
		want       error
		constraint string
		not        error
	}{
		{"NameTaken", mover, dst.ID, "held.txt", 1, blobfs.ErrNameTaken, blobfs.ConstraintUniqueFileDirectoryName, nil},
		{"NameTakenByADeletingRow", mover, dst.ID, "held-deleting.txt", 1, blobfs.ErrNameTaken, blobfs.ConstraintUniqueFileDirectoryName, nil},
		{"MissingDirectory", mover, blobfs.NewID(), "mover.txt", 1, blobfs.ErrNotFound, blobfs.ConstraintForeignKeyFileDirectory, nil},
		{"StaleVersion", mover, dst.ID, "stale.txt", 2, query.ErrVersionMismatch, "", blobfs.ErrDeleting},
		{"Deleting", deleting, dst.ID, "elsewhere.txt", 1, blobfs.ErrDeleting, "", query.ErrVersionMismatch},
		{"MissingFile", missing, dst.ID, "ghost.txt", 1, blobfs.ErrNotFound, "", nil},
		{"RefusedName", mover, dst.ID, "a/b", 1, blobfs.ErrInvalidName, "", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			var before blobfs.File
			if c.id != missing {
				before = s.file(t, c.id)
			}
			_, err := s.store.Files.Move(s.ctx, s.db, c.id, c.dir, c.file, c.version)
			if c.constraint != "" {
				wantViolation(t, err, c.want, c.constraint)
			} else if !errors.Is(err, c.want) || (c.not != nil && errors.Is(err, c.not)) {
				t.Errorf("Move = %v, want %v and not %v", err, c.want, c.not)
			}
			_, base := s.baseline.Files.Move(s.ctx, s.db, c.id, c.dir, c.file, c.version)
			wantSameError(t, err, base)
			if c.id != missing {
				if after := s.file(t, c.id); !equalFile(before, after) {
					t.Errorf("the refused move changed the row to\n%+v\nfrom\n%+v", after, before)
				}
			}
		})
	}
}
